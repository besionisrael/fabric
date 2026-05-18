// Package constraint implements the constraint-aware ordering service defined
// in Paper 3, §III. It provides the generic ConstraintEvaluator interface
// (Definition 1), the stable/speculative CacheState pair (Definition 2), and
// the block construction loop described by Algorithm 1.
//
// The package is use-case agnostic: it knows nothing about biobank specimens,
// material transfer agreements, or any domain semantics. Callers supply a
// concrete Evaluator implementation that encodes the constraint set C for their
// use case. The biobank instantiation lives in the sibling package
// orderer/consensus/etcdraft/constraint/biobank.
package constraint

import (
	"github.com/hyperledger/fabric-protos-go-apiv2/common"
)

// TxView is the constraint-relevant projection of a single transaction envelope.
// The Evaluator receives a TxView rather than a raw Envelope so that the
// parsing logic is isolated and the Evaluator stays domain-focused.
//
// Implementations of TxParser (below) extract these fields from the Fabric
// envelope protobuf. Fields not relevant to a given constraint set may be nil.
type TxView struct {
	// TxID is the transaction identifier.
	TxID string

	// Function is the chaincode function name (e.g. "Transfer", "RegisterResource").
	Function string

	// Args are the chaincode function arguments, in the same order as the
	// chaincode's Invoke receives them. Args[0] is the first argument after
	// the function name.
	Args [][]byte

	// ChannelID identifies the channel this transaction targets.
	ChannelID string
}

// CacheState is the constraint-relevant projection π(S_world) of the ledger
// world state onto the variables required to evaluate the constraint set C.
//
// CacheState is an opaque interface so that the cache representation can vary
// across Evaluator implementations without changing the orderer's block
// construction loop. The biobank implementation stores the concurrent holder
// count per SubsetGroup; a different use case might store something else.
//
// The orderer's block construction loop maintains two CacheState instances:
//   - stable:     corresponds to the last Raft-committed block (Definition 2, I1)
//   - speculative: the working copy for the batch under construction (Def. 2, I2)
//
// Both are instances of the same underlying type, created and managed by the
// Evaluator that supplies them.
type CacheState interface {
	// Clone returns a deep copy of this state, used to initialise the
	// speculative state from the stable state at the start of each batch.
	Clone() CacheState
}

// WorldStateSnapshot is a point-in-time, read-only projection of the peer
// world state onto the key-value entries of a given chaincode namespace.
// It is the input to the Init operation of Definition 1 (Paper 3): the
// snapshot π(S_world) from which the initial stable cache state is built.
//
// Using the world state snapshot rather than replaying the block log has two
// advantages. First, it gives the correct result for constraints that depend
// on the net current state (e.g. the set of active holders) regardless of
// whether prior transactions have been amended or superseded. Second, it is
// proportional in cost to the current state size rather than the log length,
// which matters for long-lived channels.
type WorldStateSnapshot interface {
	// GetByRange returns all (key, value) pairs in the given chaincode
	// namespace whose keys fall in the range [startKey, endKey).
	// An empty startKey means the beginning of the namespace; an empty
	// endKey means the end. Returns an error if the peer is unreachable
	// or if the query fails.
	GetByRange(namespace, startKey, endKey string) ([]KeyValue, error)
}

// KeyValue is a single entry returned by WorldStateSnapshot.GetByRange.
type KeyValue struct {
	Key   string
	Value []byte
}

// Evaluator is the generic interface for a constraint evaluator as defined in
// Paper 3, Definition 1 (the Evaluate operation of the constraint-aware
// ordering service O).
//
// An Evaluator encodes a specific constraint set C and exposes three
// operations that the orderer's block construction loop calls:
//
//  1. Init — builds the initial stable CacheState from a world state snapshot.
//  2. Evaluate — implements Conf(s, u) for a single transaction.
//  3. Apply — advances the cache state after a transaction is admitted.
//
// The compliance guarantee of Theorem 1 (Paper 3) follows from the orderer
// calling these three operations in the sequential order prescribed by
// Algorithm 1, under conditions C1–C3 (Proposition 1).
type Evaluator interface {
	// Init builds the initial stable cache state from a point-in-time snapshot
	// of the peer world state, applying the projection π : S_world → S.
	// It is called once at orderer startup (or after a leader change) and
	// corresponds to the Init operation of Definition 1 (Paper 3).
	//
	// The returned CacheState becomes the initial stable state s_stable.
	Init(snapshot WorldStateSnapshot) (CacheState, error)

	// Evaluate implements Conf(s, u_t): it returns true if and only if
	// transaction tx is admissible in state s, i.e. all constraints in C are
	// satisfied. It must not modify s.
	//
	// This is called for every transaction in the batch under construction,
	// against the current speculative state (condition C2, Proposition 1).
	Evaluate(s CacheState, tx *TxView) bool

	// Apply advances the cache state after transaction tx has been admitted:
	//   s' = δ(s, tx)
	// It is called immediately after Evaluate returns true, to produce the
	// next speculative state s^spec_i from s^spec_{i-1} (Definition 2).
	//
	// Apply must be the only path through which s is mutated.
	Apply(s CacheState, tx *TxView) CacheState

	// TxParser extracts a TxView from a raw Fabric envelope. The orderer calls
	// this once per transaction before calling Evaluate. Returning nil signals
	// that the envelope is not a chaincode invocation subject to constraint
	// evaluation (e.g. a config transaction); such envelopes bypass evaluation
	// and are admitted unconditionally.
	TxParser() TxParser
}

// TxParser extracts a TxView from a raw Fabric transaction envelope.
// Each Evaluator implementation supplies its own parser so that field
// extraction is co-located with the constraint semantics that depend on it.
type TxParser interface {
	Parse(env *common.Envelope) (*TxView, error)
}

// EvaluationResult records the outcome of evaluating a single transaction
// during block construction.
type EvaluationResult struct {
	TX       *TxView
	Admitted bool   // true if Conf returned 1
	Reason   string // non-empty when Admitted == false
}
