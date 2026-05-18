package main

// Resource is the core traceable entity in the directed traceability protocol.
//
// The model is intentionally use-case agnostic: the Type field carries domain
// semantics (e.g. "specimen", "dataset", "funds"), while the Conditions field
// encodes the governance rules that travel with the resource through every
// transfer. The Trace field is the directed trace — the append-only record of
// every admitted interaction, valid by construction (Paper 2, Def. 9).
type Resource struct {
	ID            string       `json:"id"`
	Type          string       `json:"type"`          // domain label, e.g. "specimen", "dataset"
	Origin        string       `json:"origin"`        // agent who registered this resource
	CurrentHolder string       `json:"currentHolder"` // agent currently responsible
	SubsetGroup   string       `json:"subsetGroup"`   // group key for C_global; empty = no group constraint
	Conditions    Conditions   `json:"conditions"`    // governance rules traveling with the resource
	Status        string       `json:"status"`        // "active" | "published" | "revoked"
	TransferCount int          `json:"transferCount"`
	Trace         []TraceEntry `json:"trace"` // directed trace (append-only)
	CreatedAt     string       `json:"createdAt"`
	UpdatedAt     string       `json:"updatedAt"`
}

// Conditions encodes the governance rules that constrain how a resource may be
// interacted with. These rules are established at registration and propagate
// through transfers: on any transfer, the new conditions must be a subset of
// (no more permissive than) the current conditions (C_propagation).
//
// This models the Material Transfer Agreement (MTA) semantics: a biobank
// transfers a specimen with conditions; those conditions bound all future use
// and re-transfer. The same structure applies to directed payments, data
// sharing agreements, or any domain where "the rules travel with the resource".
type Conditions struct {
	// AllowedActions lists which operations the holder may perform:
	// "transfer", "use", "publish". Empty slice means all are permitted.
	AllowedActions []string `json:"allowedActions,omitempty"`

	// AllowedPurposes constrains the declared purpose for "use" and "publish"
	// operations. Empty slice means any purpose is permitted.
	AllowedPurposes []string `json:"allowedPurposes,omitempty"`

	// MaxTransfers caps the total number of transfers (0 = unlimited).
	MaxTransfers int `json:"maxTransfers,omitempty"`

	// MaxConcurrent is the C_global bound: at most this many distinct agents
	// may simultaneously hold resources in the same SubsetGroup (0 = no limit).
	// This is the global constraint that Fabric's locally-validated mechanism
	// (M_L) cannot enforce reliably; it is evaluated by the constraint-aware
	// orderer (M_D endogenous) or the ZooKeeper coordinator (M_D exogenous).
	MaxConcurrent int `json:"maxConcurrent,omitempty"`

	// ExpiresAt is an optional RFC3339 expiry timestamp (empty = no expiry).
	ExpiresAt string `json:"expiresAt,omitempty"`

	// CustomRules carries domain-specific constraint parameters as key-value
	// pairs, allowing use-case extensions without modifying the base model.
	CustomRules map[string]string `json:"customRules,omitempty"`
}

// TraceEntry records one admitted interaction in the directed trace.
// Every entry corresponds to a transaction for which Conf(s_i, u_i) = 1
// at the moment of admission (Paper 2, Def. 9; Paper 3, Thm. 1).
type TraceEntry struct {
	TxID       string     `json:"txId"`
	Timestamp  string     `json:"timestamp"`
	Action     string     `json:"action"`            // "register" | "transfer" | "use" | "publish" | "revoke"
	Agent      string     `json:"agent"`             // who performed this interaction
	ToAgent    string     `json:"toAgent,omitempty"` // transfer recipient (transfer only)
	Purpose    string     `json:"purpose,omitempty"` // declared purpose (use / publish)
	Ref        string     `json:"ref,omitempty"`     // publication reference (publish only)
	Conditions Conditions `json:"conditions"`        // governance rules in effect at this step
}

// TransferRequest is the parsed input for a Transfer invocation.
type TransferRequest struct {
	ToAgent       string     `json:"toAgent"`
	NewConditions Conditions `json:"newConditions"`
}

// ResourceStatus constants.
const (
	StatusActive    = "active"
	StatusPublished = "published"
	StatusRevoked   = "revoked"
)

// Action constants — the vocabulary of interactions in the directed trace.
const (
	ActionRegister = "register"
	ActionTransfer = "transfer"
	ActionUse      = "use"
	ActionPublish  = "publish"
	ActionRevoke   = "revoke"
)

// Chaincode function names — the public API surface.
const (
	FnRegisterResource = "RegisterResource"
	FnTransfer         = "Transfer"
	FnUse              = "Use"
	FnPublish          = "Publish"
	FnRevoke           = "Revoke"
	FnGetResource      = "GetResource"
	FnGetTrace         = "GetTrace"
	FnListByGroup      = "ListByGroup"
	FnGetAllResources  = "GetAllResources"
)
