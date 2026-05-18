// Package peersnapshot implements constraint.WorldStateSnapshot by querying a
// trusted endorsing peer via Fabric's gRPC endorser protocol.
//
// This is the concrete realisation of the Init operation described in
// Paper 3, §III.A and Definition 1: the orderer fetches π(S_world) from a
// trusted peer rather than replaying the block log, giving the correct current
// state regardless of block history length or intermediate transaction format.
//
// # Protocol
//
// The snapshot is obtained by sending a ProcessProposal request to the peer's
// Endorser gRPC service, invoking the directed chaincode's GetAllResources
// function. The peer simulates the chaincode call against its local world state
// and returns the result in the proposal response payload.
//
// # Configuration
//
// The following environment variables configure this package:
//
//	FABRIC_CONSTRAINT_PEER_ADDR   peer address, e.g. "peer0.org1.example.com:7051"
//	FABRIC_CONSTRAINT_PEER_TLSCA  path to PEM-encoded TLS CA cert for the peer
//	FABRIC_CONSTRAINT_READER_CERT path to PEM-encoded reader identity certificate
//	FABRIC_CONSTRAINT_READER_KEY  path to PEM-encoded reader identity private key
//	FABRIC_CONSTRAINT_READER_MSP  MSP ID of the reader identity, e.g. "Org1MSP"
//	FABRIC_CONSTRAINT_CHANNEL     channel ID (already used to activate constraint ordering)
//	FABRIC_CONSTRAINT_CHAINCODE   chaincode name, default "directed"
package peersnapshot

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"time"

	"crypto/rand"
	"crypto/sha256"

	cb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	mspproto "github.com/hyperledger/fabric-protos-go-apiv2/msp"
	pb "github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric/orderer/consensus/etcdraft/constraint"
	"github.com/hyperledger/fabric/protoutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/proto"
)

const defaultChaincodeID = "directed"

// Config holds the parameters required to contact the trusted peer.
type Config struct {
	PeerAddr    string // "peer0.org1.example.com:7051"
	PeerTLSCA  []byte // PEM-encoded TLS CA certificate of the peer
	ReaderCert []byte // PEM-encoded application MSP certificate for query signing
	ReaderKey  []byte // PEM-encoded private key corresponding to ReaderCert
	ReaderMSP  string // MSP ID of the reader identity, e.g. "Org1MSP"
	ChannelID  string
	ChaincodeID string // defaults to "directed"
}

// FromEnv builds a Config from environment variables.
// Returns an error if any required variable is absent or its file cannot be read.
func FromEnv(channelID string) (Config, error) {
	cfg := Config{
		PeerAddr:    os.Getenv("FABRIC_CONSTRAINT_PEER_ADDR"),
		ReaderMSP:   os.Getenv("FABRIC_CONSTRAINT_READER_MSP"),
		ChannelID:   channelID,
		ChaincodeID: defaultChaincodeID,
	}
	if cc := os.Getenv("FABRIC_CONSTRAINT_CHAINCODE"); cc != "" {
		cfg.ChaincodeID = cc
	}

	for _, v := range []struct{ name, val string }{
		{"FABRIC_CONSTRAINT_PEER_ADDR", cfg.PeerAddr},
		{"FABRIC_CONSTRAINT_READER_MSP", cfg.ReaderMSP},
	} {
		if v.val == "" {
			return Config{}, fmt.Errorf("environment variable %s is required for world state snapshot", v.name)
		}
	}

	readFile := func(envVar string) ([]byte, error) {
		path := os.Getenv(envVar)
		if path == "" {
			return nil, fmt.Errorf("environment variable %s is required", envVar)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", envVar, err)
		}
		return b, nil
	}

	var err error
	if cfg.PeerTLSCA, err = readFile("FABRIC_CONSTRAINT_PEER_TLSCA"); err != nil {
		return Config{}, err
	}
	if cfg.ReaderCert, err = readFile("FABRIC_CONSTRAINT_READER_CERT"); err != nil {
		return Config{}, err
	}
	if cfg.ReaderKey, err = readFile("FABRIC_CONSTRAINT_READER_KEY"); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// PeerEndorserSnapshot implements constraint.WorldStateSnapshot by invoking
// the directed chaincode's GetAllResources function on a trusted peer.
type PeerEndorserSnapshot struct {
	cfg    Config
	conn   *grpc.ClientConn
	client pb.EndorserClient
	signer *readerSigner
}

// New dials the configured peer and returns a PeerEndorserSnapshot ready to
// use. Call Close() when the snapshot is no longer needed.
func New(cfg Config) (*PeerEndorserSnapshot, error) {
	tlsPool := x509.NewCertPool()
	if !tlsPool.AppendCertsFromPEM(cfg.PeerTLSCA) {
		return nil, fmt.Errorf("failed to parse peer TLS CA certificate")
	}

	creds := credentials.NewTLS(&tls.Config{
		RootCAs:    tlsPool,
		MinVersion: tls.VersionTLS12,
	})

	conn, err := grpc.NewClient(cfg.PeerAddr,
		grpc.WithTransportCredentials(creds),
		grpc.WithBlock(),
		grpc.WithContextDialer(nil),
	)
	if err != nil {
		return nil, fmt.Errorf("dial peer %s: %w", cfg.PeerAddr, err)
	}

	signer, err := newReaderSigner(cfg.ReaderCert, cfg.ReaderKey, cfg.ReaderMSP)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("load reader identity: %w", err)
	}

	return &PeerEndorserSnapshot{
		cfg:    cfg,
		conn:   conn,
		client: pb.NewEndorserClient(conn),
		signer: signer,
	}, nil
}

// GetByRange implements constraint.WorldStateSnapshot.
//
// For this implementation the namespace, startKey, and endKey parameters
// select the chaincode to invoke (namespace) but the range bounds are not
// forwarded to the peer query: GetAllResources returns all primary-key entries
// regardless of range. The interface parameters are retained for compatibility
// with the generic WorldStateSnapshot contract; a future implementation could
// honour the range for use cases with large state.
func (p *PeerEndorserSnapshot) GetByRange(namespace, _, _ string) ([]constraint.KeyValue, error) {
	chaincodeID := namespace
	if chaincodeID == "" {
		chaincodeID = p.cfg.ChaincodeID
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Build the chaincode invocation spec for GetAllResources.
	cis := &pb.ChaincodeInvocationSpec{
		ChaincodeSpec: &pb.ChaincodeSpec{
			Type:        pb.ChaincodeSpec_GOLANG,
			ChaincodeId: &pb.ChaincodeID{Name: chaincodeID},
			Input:       &pb.ChaincodeInput{Args: [][]byte{[]byte("GetAllResources")}},
		},
	}

	serializedIdentity, err := p.signer.Serialize()
	if err != nil {
		return nil, fmt.Errorf("serialize reader identity: %w", err)
	}

	proposal, _, err := protoutil.CreateProposalFromCIS(
		cb.HeaderType_ENDORSER_TRANSACTION,
		p.cfg.ChannelID,
		cis,
		serializedIdentity,
	)
	if err != nil {
		return nil, fmt.Errorf("create proposal: %w", err)
	}

	proposalBytes, err := proto.Marshal(proposal)
	if err != nil {
		return nil, fmt.Errorf("marshal proposal: %w", err)
	}

	sig, err := p.signer.Sign(proposalBytes)
	if err != nil {
		return nil, fmt.Errorf("sign proposal: %w", err)
	}

	signedProposal := &pb.SignedProposal{
		ProposalBytes: proposalBytes,
		Signature:     sig,
	}

	resp, err := p.client.ProcessProposal(ctx, signedProposal)
	if err != nil {
		return nil, fmt.Errorf("ProcessProposal: %w", err)
	}
	if resp.Response == nil || resp.Response.Status != 200 {
		msg := ""
		if resp.Response != nil {
			msg = resp.Response.Message
		}
		return nil, fmt.Errorf("peer returned non-200 status: %s", msg)
	}

	// The chaincode returns a JSON array of Resource objects.
	// Convert each to a KeyValue with Key=resource.ID, Value=resource JSON.
	type resourceID struct {
		ID string `json:"id"`
	}
	var resources []json.RawMessage
	if err := json.Unmarshal(resp.Response.Payload, &resources); err != nil {
		return nil, fmt.Errorf("unmarshal GetAllResources response: %w", err)
	}

	kvs := make([]constraint.KeyValue, 0, len(resources))
	for _, raw := range resources {
		var r resourceID
		if err := json.Unmarshal(raw, &r); err != nil || r.ID == "" {
			continue
		}
		kvs = append(kvs, constraint.KeyValue{Key: r.ID, Value: raw})
	}
	return kvs, nil
}

// Close releases the gRPC connection to the peer.
func (p *PeerEndorserSnapshot) Close() error {
	return p.conn.Close()
}

// =============================================================================
// readerSigner — minimal signing identity for proposal authentication.
// Uses the reader identity's ECDSA private key to produce the proposal
// signature, and wraps the certificate as a SerializedIdentity so the peer
// can verify the identity against the channel's application policies.
// =============================================================================

type readerSigner struct {
	mspID   string
	certPEM []byte
	privKey *ecdsa.PrivateKey
}

func newReaderSigner(certPEM, keyPEM []byte, mspID string) (*readerSigner, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("failed to decode reader private key PEM")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		// Try PKCS8.
		raw, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err2 != nil {
			return nil, fmt.Errorf("parse reader private key: %w (PKCS8: %v)", err, err2)
		}
		var ok bool
		key, ok = raw.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("reader private key is not ECDSA")
		}
	}
	return &readerSigner{mspID: mspID, certPEM: certPEM, privKey: key}, nil
}

// Sign produces a DER-encoded ECDSA signature over SHA-256(msg).
func (s *readerSigner) Sign(msg []byte) ([]byte, error) {
	h := sha256.Sum256(msg)
	r, sv, err := ecdsa.Sign(rand.Reader, s.privKey, h[:])
	if err != nil {
		return nil, err
	}
	// DER-encode the (r, s) pair.
	return asn1MarshalECDSA(r, sv)
}

// Serialize returns the msp.SerializedIdentity protobuf bytes expected by
// Fabric's identity verification pipeline.
func (s *readerSigner) Serialize() ([]byte, error) {
	identity := &mspproto.SerializedIdentity{
		Mspid:   s.mspID,
		IdBytes: s.certPEM,
	}
	return proto.Marshal(identity)
}

// asn1MarshalECDSA encodes the ECDSA signature (r, s) as a DER SEQUENCE.
func asn1MarshalECDSA(r, s *big.Int) ([]byte, error) {
	// ASN.1 DER: SEQUENCE { INTEGER r, INTEGER s }
	rBytes := asn1PadInt(r)
	sBytes := asn1PadInt(s)
	inner := append(asn1WrapInt(rBytes), asn1WrapInt(sBytes)...)
	return append([]byte{0x30, byte(len(inner))}, inner...), nil
}

func asn1PadInt(n *big.Int) []byte {
	b := n.Bytes()
	if len(b) == 0 || b[0]&0x80 != 0 {
		b = append([]byte{0x00}, b...)
	}
	return b
}

func asn1WrapInt(b []byte) []byte {
	return append([]byte{0x02, byte(len(b))}, b...)
}
