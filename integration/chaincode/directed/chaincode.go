package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/hyperledger/fabric-chaincode-go/v2/shim"
	pb "github.com/hyperledger/fabric-protos-go-apiv2/peer"
)

// DirectedTraceability is the chaincode implementation of the directed
// traceability protocol (M_D). It enforces local constraints (C_auth,
// C_conditions, C_propagation, C_status, C_expiry, C_transfers) at
// endorsement time. The global concurrent constraint (C_global) cannot
// be enforced reliably here — it is evaluated by the constraint-aware
// orderer or the ZooKeeper coordinator in Paper 3's three architectures.
type DirectedTraceability struct{}

// Init is called on chaincode instantiation and upgrade.
func (cc *DirectedTraceability) Init(stub shim.ChaincodeStubInterface) *pb.Response {
	return shim.Success(nil)
}

// Invoke dispatches chaincode calls.
func (cc *DirectedTraceability) Invoke(stub shim.ChaincodeStubInterface) *pb.Response {
	fn, args := stub.GetFunctionAndParameters()
	switch fn {
	case FnRegisterResource:
		return cc.registerResource(stub, args)
	case FnTransfer:
		return cc.transfer(stub, args)
	case FnUse:
		return cc.use(stub, args)
	case FnPublish:
		return cc.publish(stub, args)
	case FnRevoke:
		return cc.revoke(stub, args)
	case FnGetResource:
		return cc.getResource(stub, args)
	case FnGetTrace:
		return cc.getTrace(stub, args)
	case FnListByGroup:
		return cc.listByGroup(stub, args)
	case FnGetAllResources:
		return cc.getAllResources(stub)
	default:
		return shim.Error(fmt.Sprintf("unknown function: %s", fn))
	}
}

// =============================================================================
// RegisterResource — creates a new traceable resource.
//
// Args: [resourceID, resourceType, agentID, subsetGroup, conditionsJSON]
//
//   - resourceID:     globally unique identifier for this resource.
//   - resourceType:   domain label (e.g. "specimen", "dataset", "funds").
//   - agentID:        the registering agent, who becomes origin and initial holder.
//   - subsetGroup:    group key for C_global evaluation; empty = no group constraint.
//   - conditionsJSON: JSON-encoded Conditions struct defining governance rules.
//
// The agent becomes both origin and currentHolder. The C_global constraint
// (MaxConcurrent) is set here but enforced by the orderer, not this function.
// =============================================================================
func (cc *DirectedTraceability) registerResource(stub shim.ChaincodeStubInterface, args []string) *pb.Response {
	if len(args) != 5 {
		return shim.Error("RegisterResource expects 5 args: resourceID, resourceType, agentID, subsetGroup, conditionsJSON")
	}
	id, resourceType, agentID, subsetGroup, conditionsJSON := args[0], args[1], args[2], args[3], args[4]

	if id == "" || agentID == "" {
		return shim.Error("resourceID and agentID must not be empty")
	}

	existing, err := stub.GetState(id)
	if err != nil {
		return shim.Error(fmt.Sprintf("failed to read state: %v", err))
	}
	if existing != nil {
		return shim.Error(fmt.Sprintf("resource %s already exists", id))
	}

	var cond Conditions
	if conditionsJSON != "" {
		if err := json.Unmarshal([]byte(conditionsJSON), &cond); err != nil {
			return shim.Error(fmt.Sprintf("invalid conditionsJSON: %v", err))
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	resource := &Resource{
		ID:            id,
		Type:          resourceType,
		Origin:        agentID,
		CurrentHolder: agentID,
		SubsetGroup:   subsetGroup,
		Conditions:    cond,
		Status:        StatusActive,
		TransferCount: 0,
		Trace: []TraceEntry{{
			TxID:       stub.GetTxID(),
			Timestamp:  now,
			Action:     ActionRegister,
			Agent:      agentID,
			Conditions: cond,
		}},
		CreatedAt: now,
		UpdatedAt: now,
	}

	return cc.putResource(stub, resource)
}

// =============================================================================
// Transfer — transfers a resource to another agent under narrowed conditions.
//
// Args: [resourceID, agentID, transferJSON]
//
//   - resourceID:   the resource to transfer.
//   - agentID:      the current holder performing the transfer.
//   - transferJSON: JSON-encoded TransferRequest {toAgent, newConditions}.
//
// Local constraints enforced here (C_auth, C_conditions, C_propagation,
// C_status, C_expiry, C_transfers). C_global is enforced by the orderer.
// =============================================================================
func (cc *DirectedTraceability) transfer(stub shim.ChaincodeStubInterface, args []string) *pb.Response {
	if len(args) != 3 {
		return shim.Error("Transfer expects 3 args: resourceID, agentID, transferJSON")
	}
	resourceID, agentID, transferJSON := args[0], args[1], args[2]

	resource, resp := cc.loadActive(stub, resourceID)
	if resp != nil {
		return resp
	}

	// C_auth: only the current holder may transfer.
	if resource.CurrentHolder != agentID {
		return shim.Error(fmt.Sprintf("C_auth: agent %s is not the current holder of %s", agentID, resourceID))
	}

	// C_conditions: transfer must be an allowed action.
	if resp := cc.checkActionAllowed(resource, ActionTransfer); resp != nil {
		return resp
	}

	var req TransferRequest
	if err := json.Unmarshal([]byte(transferJSON), &req); err != nil {
		return shim.Error(fmt.Sprintf("invalid transferJSON: %v", err))
	}
	if req.ToAgent == "" {
		return shim.Error("toAgent must not be empty")
	}

	// C_transfers: check transfer count cap.
	if resource.Conditions.MaxTransfers > 0 && resource.TransferCount >= resource.Conditions.MaxTransfers {
		return shim.Error(fmt.Sprintf("C_transfers: resource %s has reached its maximum transfer count (%d)",
			resourceID, resource.Conditions.MaxTransfers))
	}

	// C_expiry: check expiry.
	if resp := cc.checkExpiry(resource); resp != nil {
		return resp
	}

	// C_propagation: new conditions must not relax the current conditions.
	if resp := cc.checkConditionPropagation(resource.Conditions, req.NewConditions); resp != nil {
		return resp
	}

	now := time.Now().UTC().Format(time.RFC3339)
	resource.Trace = append(resource.Trace, TraceEntry{
		TxID:       stub.GetTxID(),
		Timestamp:  now,
		Action:     ActionTransfer,
		Agent:      agentID,
		ToAgent:    req.ToAgent,
		Conditions: req.NewConditions,
	})
	resource.CurrentHolder = req.ToAgent
	resource.Conditions = req.NewConditions
	resource.TransferCount++
	resource.UpdatedAt = now

	return cc.putResource(stub, resource)
}

// =============================================================================
// Use — records a declared use of the resource by the current holder.
//
// Args: [resourceID, agentID, action, purpose]
//
//   - resourceID: the resource being used.
//   - agentID:    must be the current holder.
//   - action:     declared action (must be in AllowedActions, or "use" if empty).
//   - purpose:    declared purpose (must be in AllowedPurposes if set).
// =============================================================================
func (cc *DirectedTraceability) use(stub shim.ChaincodeStubInterface, args []string) *pb.Response {
	if len(args) != 4 {
		return shim.Error("Use expects 4 args: resourceID, agentID, action, purpose")
	}
	resourceID, agentID, action, purpose := args[0], args[1], args[2], args[3]

	resource, resp := cc.loadActive(stub, resourceID)
	if resp != nil {
		return resp
	}

	if resource.CurrentHolder != agentID {
		return shim.Error(fmt.Sprintf("C_auth: agent %s is not the current holder of %s", agentID, resourceID))
	}
	if resp := cc.checkActionAllowed(resource, ActionUse); resp != nil {
		return resp
	}
	if resp := cc.checkPurposeAllowed(resource, purpose); resp != nil {
		return resp
	}
	if resp := cc.checkExpiry(resource); resp != nil {
		return resp
	}

	now := time.Now().UTC().Format(time.RFC3339)
	resource.Trace = append(resource.Trace, TraceEntry{
		TxID:       stub.GetTxID(),
		Timestamp:  now,
		Action:     ActionUse,
		Agent:      agentID,
		Purpose:    purpose,
		Ref:        action,
		Conditions: resource.Conditions,
	})
	resource.UpdatedAt = now

	return cc.putResource(stub, resource)
}

// =============================================================================
// Publish — closes the directed trace with a verifiable publication record.
//
// Args: [resourceID, agentID, publicationRef, purpose]
//
//   - resourceID:     the resource referenced in the publication.
//   - agentID:        must be the current holder.
//   - publicationRef: external publication identifier (DOI, arXiv ID, etc.).
//   - purpose:        declared purpose of the publication.
//
// After Publish, the resource status transitions to "published". The
// publicationRef anchors the blockchain ID in the external record, closing
// the loop described in Paper 1 (specimen traceability in publications).
// =============================================================================
func (cc *DirectedTraceability) publish(stub shim.ChaincodeStubInterface, args []string) *pb.Response {
	if len(args) != 4 {
		return shim.Error("Publish expects 4 args: resourceID, agentID, publicationRef, purpose")
	}
	resourceID, agentID, pubRef, purpose := args[0], args[1], args[2], args[3]

	resource, resp := cc.loadActive(stub, resourceID)
	if resp != nil {
		return resp
	}

	if resource.CurrentHolder != agentID {
		return shim.Error(fmt.Sprintf("C_auth: agent %s is not the current holder of %s", agentID, resourceID))
	}
	if resp := cc.checkActionAllowed(resource, ActionPublish); resp != nil {
		return resp
	}
	if resp := cc.checkPurposeAllowed(resource, purpose); resp != nil {
		return resp
	}

	now := time.Now().UTC().Format(time.RFC3339)
	resource.Trace = append(resource.Trace, TraceEntry{
		TxID:       stub.GetTxID(),
		Timestamp:  now,
		Action:     ActionPublish,
		Agent:      agentID,
		Purpose:    purpose,
		Ref:        pubRef,
		Conditions: resource.Conditions,
	})
	resource.Status = StatusPublished
	resource.UpdatedAt = now

	return cc.putResource(stub, resource)
}

// =============================================================================
// Revoke — revokes a resource, ending its lifecycle.
//
// Args: [resourceID, agentID, reason]
//
// Only the origin agent may revoke a resource. Revocation removes the resource
// from active circulation and records the reason in the trace.
// =============================================================================
func (cc *DirectedTraceability) revoke(stub shim.ChaincodeStubInterface, args []string) *pb.Response {
	if len(args) != 3 {
		return shim.Error("Revoke expects 3 args: resourceID, agentID, reason")
	}
	resourceID, agentID, reason := args[0], args[1], args[2]

	resource, resp := cc.loadActive(stub, resourceID)
	if resp != nil {
		return resp
	}

	if resource.Origin != agentID {
		return shim.Error(fmt.Sprintf("C_auth: only origin agent %s may revoke resource %s", resource.Origin, resourceID))
	}

	now := time.Now().UTC().Format(time.RFC3339)
	resource.Trace = append(resource.Trace, TraceEntry{
		TxID:       stub.GetTxID(),
		Timestamp:  now,
		Action:     ActionRevoke,
		Agent:      agentID,
		Purpose:    reason,
		Conditions: resource.Conditions,
	})
	resource.Status = StatusRevoked
	resource.UpdatedAt = now

	return cc.putResource(stub, resource)
}

// =============================================================================
// GetResource — returns the full resource record as JSON.
// Args: [resourceID]
// =============================================================================
func (cc *DirectedTraceability) getResource(stub shim.ChaincodeStubInterface, args []string) *pb.Response {
	if len(args) != 1 {
		return shim.Error("GetResource expects 1 arg: resourceID")
	}
	data, err := stub.GetState(args[0])
	if err != nil {
		return shim.Error(fmt.Sprintf("failed to read state: %v", err))
	}
	if data == nil {
		return shim.Error(fmt.Sprintf("resource %s not found", args[0]))
	}
	return shim.Success(data)
}

// =============================================================================
// GetTrace — returns the directed trace of a resource as JSON.
// Args: [resourceID]
// =============================================================================
func (cc *DirectedTraceability) getTrace(stub shim.ChaincodeStubInterface, args []string) *pb.Response {
	if len(args) != 1 {
		return shim.Error("GetTrace expects 1 arg: resourceID")
	}
	data, err := stub.GetState(args[0])
	if err != nil {
		return shim.Error(fmt.Sprintf("failed to read state: %v", err))
	}
	if data == nil {
		return shim.Error(fmt.Sprintf("resource %s not found", args[0]))
	}
	var resource Resource
	if err := json.Unmarshal(data, &resource); err != nil {
		return shim.Error(fmt.Sprintf("failed to unmarshal resource: %v", err))
	}
	traceBytes, err := json.Marshal(resource.Trace)
	if err != nil {
		return shim.Error(fmt.Sprintf("failed to marshal trace: %v", err))
	}
	return shim.Success(traceBytes)
}

// =============================================================================
// ListByGroup — returns all resources in a given subsetGroup as JSON.
// Args: [subsetGroup]
// Used by the orderer cache initialisation and the ZooKeeper baseline.
// =============================================================================
func (cc *DirectedTraceability) listByGroup(stub shim.ChaincodeStubInterface, args []string) *pb.Response {
	if len(args) != 1 {
		return shim.Error("ListByGroup expects 1 arg: subsetGroup")
	}
	group := args[0]

	// Use composite key: "group~id" → resource JSON
	iter, err := stub.GetStateByPartialCompositeKey("group~id", []string{group})
	if err != nil {
		return shim.Error(fmt.Sprintf("range query failed: %v", err))
	}
	defer iter.Close()

	var results []*Resource
	for iter.HasNext() {
		kv, err := iter.Next()
		if err != nil {
			return shim.Error(fmt.Sprintf("iterator error: %v", err))
		}
		var r Resource
		if err := json.Unmarshal(kv.Value, &r); err != nil {
			continue
		}
		results = append(results, &r)
	}

	out, err := json.Marshal(results)
	if err != nil {
		return shim.Error(fmt.Sprintf("marshal error: %v", err))
	}
	return shim.Success(out)
}

// =============================================================================
// GetAllResources — returns all resources as a JSON array (query only).
//
// No args required. Called by the constraint-aware orderer at startup to
// initialize the constraint state cache from the current world state (Paper 3,
// §III.A, Definition 1 — Init operation). Returns only primary-key entries;
// composite-key index entries (subsetGroup~id) are excluded automatically
// because this function scans the primary keyspace.
//
// This function gives the orderer a point-in-time read of π(S_world): the
// projection of the full ledger state onto the constraint-relevant variables
// (currentHolder, subsetGroup, maxConcurrent, status for each resource).
// =============================================================================
func (cc *DirectedTraceability) getAllResources(stub shim.ChaincodeStubInterface) *pb.Response {
	// Fabric primary keys are plain strings; composite keys created via
	// CreateCompositeKey start with the U+0000 null byte. A range scan over
	// ["", "") returns both. We scan only the range [" ", "~"] which covers
	// all printable ASCII keys and excludes the null-prefix composite entries.
	// Adjust the range if resource IDs use characters outside this range.
	iter, err := stub.GetStateByRange(" ", "~")
	if err != nil {
		return shim.Error(fmt.Sprintf("GetStateByRange failed: %v", err))
	}
	defer iter.Close()

	var results []*Resource
	for iter.HasNext() {
		kv, err := iter.Next()
		if err != nil {
			return shim.Error(fmt.Sprintf("iterator error: %v", err))
		}
		var r Resource
		if err := json.Unmarshal(kv.Value, &r); err != nil {
			continue // skip malformed entries
		}
		results = append(results, &r)
	}

	out, err := json.Marshal(results)
	if err != nil {
		return shim.Error(fmt.Sprintf("marshal error: %v", err))
	}
	return shim.Success(out)
}

// =============================================================================
// Internal helpers
// =============================================================================

// loadActive retrieves a resource and verifies it is in "active" status.
func (cc *DirectedTraceability) loadActive(stub shim.ChaincodeStubInterface, id string) (*Resource, *pb.Response) {
	data, err := stub.GetState(id)
	if err != nil {
		r := shim.Error(fmt.Sprintf("failed to read state: %v", err))
		return nil, r
	}
	if data == nil {
		r := shim.Error(fmt.Sprintf("resource %s not found", id))
		return nil, r
	}
	var resource Resource
	if err := json.Unmarshal(data, &resource); err != nil {
		r := shim.Error(fmt.Sprintf("failed to unmarshal resource: %v", err))
		return nil, r
	}
	if resource.Status != StatusActive {
		r := shim.Error(fmt.Sprintf("C_status: resource %s is not active (status: %s)", id, resource.Status))
		return nil, r
	}
	return &resource, nil
}

// putResource serializes and stores a resource, maintaining the composite key
// index used by ListByGroup.
func (cc *DirectedTraceability) putResource(stub shim.ChaincodeStubInterface, r *Resource) *pb.Response {
	data, err := json.Marshal(r)
	if err != nil {
		return shim.Error(fmt.Sprintf("failed to marshal resource: %v", err))
	}
	if err := stub.PutState(r.ID, data); err != nil {
		return shim.Error(fmt.Sprintf("failed to write state: %v", err))
	}

	// Maintain composite key index for ListByGroup queries.
	if r.SubsetGroup != "" {
		ck, err := stub.CreateCompositeKey("group~id", []string{r.SubsetGroup, r.ID})
		if err != nil {
			return shim.Error(fmt.Sprintf("failed to create composite key: %v", err))
		}
		// Value is the full resource JSON so the orderer cache can read it
		// from a single range query without a second GetState per resource.
		if err := stub.PutState(ck, data); err != nil {
			return shim.Error(fmt.Sprintf("failed to write composite key: %v", err))
		}
	}

	return shim.Success(data)
}

// checkActionAllowed returns an error response if the proposed action is not
// in the resource's AllowedActions list (C_conditions).
func (cc *DirectedTraceability) checkActionAllowed(r *Resource, action string) *pb.Response {
	if len(r.Conditions.AllowedActions) == 0 {
		return nil // no restriction
	}
	for _, a := range r.Conditions.AllowedActions {
		if a == action {
			return nil
		}
	}
	resp := shim.Error(fmt.Sprintf("C_conditions: action %q is not permitted for resource %s (allowed: %v)",
		action, r.ID, r.Conditions.AllowedActions))
	return resp
}

// checkPurposeAllowed returns an error response if the declared purpose is not
// in the resource's AllowedPurposes list (C_conditions).
func (cc *DirectedTraceability) checkPurposeAllowed(r *Resource, purpose string) *pb.Response {
	if len(r.Conditions.AllowedPurposes) == 0 {
		return nil // no restriction
	}
	for _, p := range r.Conditions.AllowedPurposes {
		if p == purpose {
			return nil
		}
	}
	resp := shim.Error(fmt.Sprintf("C_conditions: purpose %q is not permitted for resource %s (allowed: %v)",
		purpose, r.ID, r.Conditions.AllowedPurposes))
	return resp
}

// checkExpiry returns an error response if the resource's conditions have
// expired (C_expiry).
func (cc *DirectedTraceability) checkExpiry(r *Resource) *pb.Response {
	if r.Conditions.ExpiresAt == "" {
		return nil
	}
	expiry, err := time.Parse(time.RFC3339, r.Conditions.ExpiresAt)
	if err != nil {
		return nil // unparseable expiry: ignore (fail-open for expiry only)
	}
	if time.Now().UTC().After(expiry) {
		resp := shim.Error(fmt.Sprintf("C_expiry: resource %s conditions expired at %s", r.ID, r.Conditions.ExpiresAt))
		return resp
	}
	return nil
}

// checkConditionPropagation enforces C_propagation: the new conditions on a
// transfer must be no more permissive than the current conditions.
//
// "More permissive" means: more actions, more purposes, higher MaxTransfers,
// higher MaxConcurrent, or a later expiry than the current conditions allow.
func (cc *DirectedTraceability) checkConditionPropagation(current, proposed Conditions) *pb.Response {
	// Allowed actions: proposed must be a subset of current.
	if len(current.AllowedActions) > 0 {
		if resp := checkSubset("AllowedActions", current.AllowedActions, proposed.AllowedActions); resp != nil {
			return resp
		}
	}

	// Allowed purposes: proposed must be a subset of current.
	if len(current.AllowedPurposes) > 0 {
		if resp := checkSubset("AllowedPurposes", current.AllowedPurposes, proposed.AllowedPurposes); resp != nil {
			return resp
		}
	}

	// MaxTransfers: proposed cap must not exceed current cap (0 = unlimited).
	if current.MaxTransfers > 0 {
		if proposed.MaxTransfers == 0 || proposed.MaxTransfers > current.MaxTransfers {
			resp := shim.Error(fmt.Sprintf(
				"C_propagation: proposed MaxTransfers (%d) relaxes current cap (%d)",
				proposed.MaxTransfers, current.MaxTransfers))
			return resp
		}
	}

	// MaxConcurrent: proposed cap must not exceed current cap.
	if current.MaxConcurrent > 0 {
		if proposed.MaxConcurrent == 0 || proposed.MaxConcurrent > current.MaxConcurrent {
			resp := shim.Error(fmt.Sprintf(
				"C_propagation: proposed MaxConcurrent (%d) relaxes current cap (%d)",
				proposed.MaxConcurrent, current.MaxConcurrent))
			return resp
		}
	}

	// ExpiresAt: proposed expiry must not be later than current expiry.
	if current.ExpiresAt != "" && proposed.ExpiresAt != "" {
		curr, err1 := time.Parse(time.RFC3339, current.ExpiresAt)
		prop, err2 := time.Parse(time.RFC3339, proposed.ExpiresAt)
		if err1 == nil && err2 == nil && prop.After(curr) {
			resp := shim.Error(fmt.Sprintf(
				"C_propagation: proposed expiry (%s) extends beyond current expiry (%s)",
				proposed.ExpiresAt, current.ExpiresAt))
			return resp
		}
	}

	return nil
}

// checkSubset verifies that every element of proposed exists in allowed.
func checkSubset(field string, allowed, proposed []string) *pb.Response {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		allowedSet[a] = struct{}{}
	}
	for _, p := range proposed {
		if _, ok := allowedSet[p]; !ok {
			resp := shim.Error(fmt.Sprintf(
				"C_propagation: proposed %s contains %q which is not in current allowed set %v",
				field, p, allowed))
			return resp
		}
	}
	return nil
}
