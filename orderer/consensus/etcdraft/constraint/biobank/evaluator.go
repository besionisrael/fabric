// Package biobank provides the biobank instantiation of the generic
// constraint.Evaluator interface. It encodes the three usage constraints
// from Paper 2, §III.A applied to directed traceability of biological specimens:
//
//   - C_auth:       the proposing agent is the current holder (enforced in chaincode; checked here for cache consistency)
//   - C_excl:       at most one holder per resource (local; enforced in chaincode)
//   - C_global:     at most K_max distinct agents simultaneously hold resources
//     in the same SubsetGroup (global; cannot be enforced reliably by
//     the chaincode alone — this is the constraint that M_L violates and
//     M_D guarantees, per Paper 2 Theorems 1–2 and Paper 3 Theorem 1)
//
// The Evaluator tracks only the state required to evaluate C_global: for each
// SubsetGroup, the set of agents currently holding at least one resource in
// that group, and the MaxConcurrent limit defined by any resource in the group.
//
// C_auth and C_excl are enforced by the chaincode at endorsement time. The
// orderer does not re-evaluate them (it has no access to per-resource holder
// information beyond what its cache tracks). Only C_global requires the
// orderer's global view, which is precisely why it is the focus of Paper 3.
package biobank

import (
	"encoding/json"
	"fmt"

	cb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	pb "github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric/orderer/consensus/etcdraft/constraint"
	"google.golang.org/protobuf/proto"
)

// directedChaincodeID is the chaincode namespace queried by Init.
// Must match the chaincode name deployed on the channel.
const directedChaincodeID = "directed"

// GroupState tracks the C_global constraint for one SubsetGroup.
type GroupState struct {
	// Holders is the set of agent IDs currently holding at least one resource
	// in this group. |Holders| ≤ MaxConcurrent must hold after each admission.
	Holders map[string]struct{}

	// MaxConcurrent is the K_max bound for this group, as set on the first
	// resource registered in the group. 0 means no limit.
	MaxConcurrent int
}

// resourceSnapshot is a minimal struct for deserializing the directed chaincode's
// Resource JSON from the world state snapshot. Only the fields required to
// evaluate C_global are read; the full Trace is ignored for efficiency.
type resourceSnapshot struct {
	ID            string `json:"id"`
	CurrentHolder string `json:"currentHolder"`
	SubsetGroup   string `json:"subsetGroup"`
	Status        string `json:"status"`
	Conditions    struct {
		MaxConcurrent int `json:"maxConcurrent,omitempty"`
	} `json:"conditions"`
}

// BioankCacheState implements constraint.CacheState for the biobank use case.
//
// It maintains two projections of the ledger state:
//  1. resources: resourceID → ResourceEntry (current holder, group, max concurrent)
//  2. groups:    subsetGroup → GroupState  (current holders set, K_max)
//
// Both are derived from the world state snapshot at Init time and kept
// up-to-date by Apply after each admitted transaction.
type BioankCacheState struct {
	// resources maps resourceID → constraint-relevant fields.
	resources map[string]*ResourceEntry

	// groups maps subsetGroup → concurrent holder state.
	groups map[string]*GroupState
}

// ResourceEntry is the projection of a chaincode Resource onto the fields
// needed to evaluate C_global.
type ResourceEntry struct {
	CurrentHolder string
	SubsetGroup   string
	MaxConcurrent int
	Status        string
}

// Clone returns a deep copy suitable for use as a speculative state.
func (s *BioankCacheState) Clone() constraint.CacheState {
	c := &BioankCacheState{
		resources: make(map[string]*ResourceEntry, len(s.resources)),
		groups:    make(map[string]*GroupState, len(s.groups)),
	}
	for id, r := range s.resources {
		rc := *r
		c.resources[id] = &rc
	}
	for g, gs := range s.groups {
		holders := make(map[string]struct{}, len(gs.Holders))
		for h := range gs.Holders {
			holders[h] = struct{}{}
		}
		c.groups[g] = &GroupState{Holders: holders, MaxConcurrent: gs.MaxConcurrent}
	}
	return c
}

// =============================================================================
// BioankEvaluator — implements constraint.Evaluator
// =============================================================================

// BioankEvaluator is the biobank instantiation of the constraint-aware ordering
// service's Evaluate component (Paper 3, Definition 1, operation 2).
type BioankEvaluator struct{}

// New returns a ready-to-use BioankEvaluator.
func New() *BioankEvaluator { return &BioankEvaluator{} }

// Init builds the initial constraint cache from a point-in-time world state
// snapshot, implementing the Init operation of Definition 1 (Paper 3).
//
// It reads all resources in the directed chaincode namespace from the snapshot
// and projects each active resource onto the GroupState map, implementing
// π : S_world → S. This approach reads the current net state directly, rather
// than reconstructing it from the transaction log — consistent with the paper's
// description of Init taking a world state snapshot rather than a block iterator.
//
// The snapshot is obtained by querying a trusted peer's endorser (see the
// peersnapshot package); the orderer calls GetAllResources on the directed
// chaincode, which returns all active resources as a JSON array.
func (e *BioankEvaluator) Init(snapshot constraint.WorldStateSnapshot) (constraint.CacheState, error) {
	s := &BioankCacheState{
		resources: make(map[string]*ResourceEntry),
		groups:    make(map[string]*GroupState),
	}

	// Query the full constraint-relevant projection of the world state.
	// The namespace is the directed chaincode ID; startKey/endKey are empty to
	// request all entries (the peer snapshot implementation maps this to a
	// GetAllResources chaincode call, which returns primary-key entries only).
	kvs, err := snapshot.GetByRange(directedChaincodeID, "", "")
	if err != nil {
		return nil, fmt.Errorf("world state snapshot query failed: %w", err)
	}

	for _, kv := range kvs {
		var res resourceSnapshot
		if err := json.Unmarshal(kv.Value, &res); err != nil {
			continue // skip malformed entries
		}
		if res.Status != "active" || res.SubsetGroup == "" || res.Conditions.MaxConcurrent == 0 {
			continue // not subject to C_global
		}
		s.resources[res.ID] = &ResourceEntry{
			CurrentHolder: res.CurrentHolder,
			SubsetGroup:   res.SubsetGroup,
			MaxConcurrent: res.Conditions.MaxConcurrent,
			Status:        res.Status,
		}
		addHolderToGroup(s, res.SubsetGroup, res.CurrentHolder, res.Conditions.MaxConcurrent)
	}
	return s, nil
}

// Evaluate implements Conf(s, tx) for the biobank constraint set.
//
// Only C_global is evaluated here, because:
//   - C_auth and C_excl are local constraints, enforced by the chaincode at
//     endorsement time. The orderer's cache tracks holders but does not
//     re-evaluate auth (it does not have access to permission records).
//   - C_global is the global constraint that requires the orderer's global
//     view of concurrent holders across a SubsetGroup. It is the structural
//     gap that M_L cannot close and M_D resolves (Paper 2, Thm. 2; Paper 3, §III.A).
//
// Returns true (admit) if the transaction does not violate C_global.
// Returns false (reject) if admitting the transaction would cause
// |S_sub ∪ {toAgent}| > K_max for any affected SubsetGroup.
func (e *BioankEvaluator) Evaluate(s constraint.CacheState, tx *constraint.TxView) bool {
	cs := s.(*BioankCacheState)

	switch tx.Function {
	case "RegisterResource":
		return e.evalRegister(cs, tx)
	case "Transfer":
		return e.evalTransfer(cs, tx)
	default:
		// Use, Publish, Revoke, GetResource, GetTrace: no C_global impact.
		return true
	}
}

// evalRegister checks C_global for a RegisterResource transaction.
// The registering agent becomes the initial holder; if the resource belongs
// to a group with a MaxConcurrent limit, the group count must not overflow.
func (e *BioankEvaluator) evalRegister(s *BioankCacheState, tx *constraint.TxView) bool {
	if len(tx.Args) < 5 {
		return true // malformed: let chaincode reject it
	}
	// args: [resourceID, resourceType, agentID, subsetGroup, conditionsJSON]
	agentID := string(tx.Args[2])
	subsetGroup := string(tx.Args[3])
	conditionsJSON := tx.Args[4]

	if subsetGroup == "" {
		return true // no group → C_global not applicable
	}

	var cond struct {
		MaxConcurrent int `json:"maxConcurrent"`
	}
	if err := json.Unmarshal(conditionsJSON, &cond); err != nil || cond.MaxConcurrent == 0 {
		return true // no concurrent limit → C_global not applicable
	}

	return e.wouldExceed(s, subsetGroup, agentID, cond.MaxConcurrent)
}

// evalTransfer checks C_global for a Transfer transaction.
// The new holder (toAgent) may increase the concurrent holder count if they
// do not already hold another resource in the same group.
func (e *BioankEvaluator) evalTransfer(s *BioankCacheState, tx *constraint.TxView) bool {
	if len(tx.Args) < 3 {
		return true // malformed: let chaincode reject it
	}
	// args: [resourceID, agentID, transferJSON]
	resourceID := string(tx.Args[0])
	resource, ok := s.resources[resourceID]
	if !ok || resource.SubsetGroup == "" || resource.MaxConcurrent == 0 {
		return true // resource not in a constrained group
	}

	var req struct {
		ToAgent string `json:"toAgent"`
	}
	if err := json.Unmarshal(tx.Args[2], &req); err != nil || req.ToAgent == "" {
		return true // malformed: let chaincode reject it
	}

	return e.wouldExceed(s, resource.SubsetGroup, req.ToAgent, resource.MaxConcurrent)
}

// wouldExceed returns true (admit) if adding newAgent to the group would NOT
// exceed the maxConcurrent limit, i.e. |S_sub ∪ {newAgent}| ≤ maxConcurrent.
// Returns false (reject) otherwise.
func (e *BioankEvaluator) wouldExceed(s *BioankCacheState, group, newAgent string, maxConcurrent int) bool {
	gs, exists := s.groups[group]
	if !exists {
		return true // group not yet in cache → first resource in this group
	}
	// If newAgent already holds a resource in this group, the count does not increase.
	if _, alreadyHolder := gs.Holders[newAgent]; alreadyHolder {
		return true
	}
	return len(gs.Holders)+1 <= maxConcurrent
}

// Apply advances the cache state after tx has been admitted: δ(s, tx).
// It updates the resources and groups maps to reflect the transaction's effect.
func (e *BioankEvaluator) Apply(s constraint.CacheState, tx *constraint.TxView) constraint.CacheState {
	cs := s.(*BioankCacheState)
	// Apply operates on the speculative copy, which is already a Clone().
	applyToCache(cs, tx)
	return cs
}

// TxParser returns the BioankTxParser for this evaluator.
func (e *BioankEvaluator) TxParser() constraint.TxParser {
	return &BioankTxParser{}
}

// =============================================================================
// applyToCache — shared state transition function δ(s, tx)
// Used by both Init (replaying committed blocks) and Apply (speculative updates).
// =============================================================================

func applyToCache(s *BioankCacheState, tx *constraint.TxView) {
	switch tx.Function {
	case "RegisterResource":
		applyRegister(s, tx)
	case "Transfer":
		applyTransfer(s, tx)
	case "Revoke", "Publish":
		applyTerminate(s, tx)
	}
}

func applyRegister(s *BioankCacheState, tx *constraint.TxView) {
	if len(tx.Args) < 5 {
		return
	}
	resourceID := string(tx.Args[0])
	agentID := string(tx.Args[2])
	subsetGroup := string(tx.Args[3])
	conditionsJSON := tx.Args[4]

	var cond struct {
		MaxConcurrent int `json:"maxConcurrent"`
	}
	_ = json.Unmarshal(conditionsJSON, &cond)

	s.resources[resourceID] = &ResourceEntry{
		CurrentHolder: agentID,
		SubsetGroup:   subsetGroup,
		MaxConcurrent: cond.MaxConcurrent,
		Status:        "active",
	}
	if subsetGroup != "" && cond.MaxConcurrent > 0 {
		addHolderToGroup(s, subsetGroup, agentID, cond.MaxConcurrent)
	}
}

func applyTransfer(s *BioankCacheState, tx *constraint.TxView) {
	if len(tx.Args) < 3 {
		return
	}
	resourceID := string(tx.Args[0])
	resource, ok := s.resources[resourceID]
	if !ok {
		return
	}

	var req struct {
		ToAgent string `json:"toAgent"`
	}
	if err := json.Unmarshal(tx.Args[2], &req); err != nil || req.ToAgent == "" {
		return
	}

	if resource.SubsetGroup != "" && resource.MaxConcurrent > 0 {
		removeHolderFromGroup(s, resource.SubsetGroup, resource.CurrentHolder, resourceID)
		addHolderToGroup(s, resource.SubsetGroup, req.ToAgent, resource.MaxConcurrent)
	}
	resource.CurrentHolder = req.ToAgent
}

func applyTerminate(s *BioankCacheState, tx *constraint.TxView) {
	if len(tx.Args) < 1 {
		return
	}
	resourceID := string(tx.Args[0])
	resource, ok := s.resources[resourceID]
	if !ok {
		return
	}
	if resource.SubsetGroup != "" {
		removeHolderFromGroup(s, resource.SubsetGroup, resource.CurrentHolder, resourceID)
	}
	resource.Status = "inactive"
}

// addHolderToGroup adds an agent to a group's holder set, creating the group
// entry if it does not exist.
func addHolderToGroup(s *BioankCacheState, group, agent string, maxConcurrent int) {
	gs, ok := s.groups[group]
	if !ok {
		gs = &GroupState{Holders: make(map[string]struct{}), MaxConcurrent: maxConcurrent}
		s.groups[group] = gs
	}
	gs.Holders[agent] = struct{}{}
}

// removeHolderFromGroup removes an agent from a group's holder set only if
// they hold no other active resource in the group.
func removeHolderFromGroup(s *BioankCacheState, group, agent, exceptResourceID string) {
	// Check if the agent holds any other resource in this group besides the one being transferred/terminated.
	for id, r := range s.resources {
		if id == exceptResourceID {
			continue
		}
		if r.SubsetGroup == group && r.CurrentHolder == agent && r.Status == "active" {
			return // agent still holds another resource in this group
		}
	}
	gs, ok := s.groups[group]
	if !ok {
		return
	}
	delete(gs.Holders, agent)
}

// =============================================================================
// BioankTxParser — implements constraint.TxParser
// =============================================================================

// BioankTxParser extracts a TxView from a Fabric transaction envelope by
// unwrapping the protobuf layers to reach the ChaincodeInvocationSpec.
type BioankTxParser struct{}

// Parse extracts a TxView from the envelope, or returns nil if the envelope
// is not a chaincode invocation (e.g. a config transaction).
func (p *BioankTxParser) Parse(env *cb.Envelope) (*constraint.TxView, error) {
	if env == nil {
		return nil, nil
	}

	// Unwrap Payload.
	payload := &cb.Payload{}
	if err := proto.Unmarshal(env.Payload, payload); err != nil {
		return nil, fmt.Errorf("unmarshal payload: %w", err)
	}
	if payload.Header == nil {
		return nil, nil
	}

	// Check channel header type: only ENDORSER_TRANSACTION carries chaincode calls.
	chdr := &cb.ChannelHeader{}
	if err := proto.Unmarshal(payload.Header.ChannelHeader, chdr); err != nil {
		return nil, fmt.Errorf("unmarshal channel header: %w", err)
	}
	if cb.HeaderType(chdr.Type) != cb.HeaderType_ENDORSER_TRANSACTION {
		return nil, nil // config tx: admit unconditionally
	}

	// Unwrap Transaction → TransactionAction → ChaincodeActionPayload.
	tx := &pb.Transaction{}
	if err := proto.Unmarshal(payload.Data, tx); err != nil {
		return nil, fmt.Errorf("unmarshal transaction: %w", err)
	}
	if len(tx.Actions) == 0 {
		return nil, nil
	}

	cap := &pb.ChaincodeActionPayload{}
	if err := proto.Unmarshal(tx.Actions[0].Payload, cap); err != nil {
		return nil, fmt.Errorf("unmarshal chaincode action payload: %w", err)
	}

	// Unwrap ChaincodeProposalPayload → ChaincodeInvocationSpec.
	cpp := &pb.ChaincodeProposalPayload{}
	if err := proto.Unmarshal(cap.ChaincodeProposalPayload, cpp); err != nil {
		return nil, fmt.Errorf("unmarshal chaincode proposal payload: %w", err)
	}

	cis := &pb.ChaincodeInvocationSpec{}
	if err := proto.Unmarshal(cpp.Input, cis); err != nil {
		return nil, fmt.Errorf("unmarshal chaincode invocation spec: %w", err)
	}
	if cis.ChaincodeSpec == nil || cis.ChaincodeSpec.Input == nil {
		return nil, nil
	}

	rawArgs := cis.ChaincodeSpec.Input.Args
	if len(rawArgs) == 0 {
		return nil, nil
	}

	tv := &constraint.TxView{
		TxID:      chdr.TxId,
		ChannelID: chdr.ChannelId,
		Function:  string(rawArgs[0]),
	}
	if len(rawArgs) > 1 {
		tv.Args = rawArgs[1:]
	}
	return tv, nil
}

