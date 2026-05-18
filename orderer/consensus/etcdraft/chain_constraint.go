package etcdraft

import (
	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric/orderer/consensus/etcdraft/constraint"
)

// constraintManager is the interface the Chain uses to interact with the
// constraint-aware ordering service (Paper 3, Definition 1). It is an
// interface rather than a concrete type so that tests can inject fakes and
// so that the Fabric-standard (no-constraint) path is expressed as nil.
type constraintManager interface {
	// BeginBatch initialises the speculative cache for a new batch
	// (Definition 2, invariant I2: s^spec_0 = s^stable).
	BeginBatch()

	// ProcessBatch evaluates every envelope in the batch against the evolving
	// speculative state (Algorithm 1, Paper 3). It returns the admitted and
	// rejected subsets, both in arrival order.
	ProcessBatch(envelopes []*common.Envelope) (admitted, rejected []*common.Envelope)

	// Commit promotes the speculative state to stable after a Raft commit
	// (Definition 1, Commit; Proposition 1, condition C3).
	Commit()

	// Rollback discards the speculative state after a Raft failure
	// (Definition 1, Rollback; Proposition 1, condition C3).
	Rollback()
}

// filterBatch runs the constraint evaluation pass over a batch of envelopes
// and returns only the admitted subset. Rejected envelopes are logged.
//
// When c.constraintMgr is nil (Fabric standard / M_L baseline), the full
// batch is returned unchanged — preserving the standard pipeline exactly.
//
// This function is the only call site of ProcessBatch in the block construction
// path, ensuring condition C2 (Proposition 1): sequential, single-threaded
// evaluation within each batch.
func (c *Chain) filterBatch(batch []*common.Envelope) []*common.Envelope {
	if c.constraintMgr == nil {
		return batch
	}

	c.constraintMgr.BeginBatch()
	admitted, rejected := c.constraintMgr.ProcessBatch(batch)

	if len(rejected) > 0 {
		c.logger.Infof("Constraint evaluator rejected %d/%d transactions in batch (C_global)",
			len(rejected), len(batch))
		c.Metrics.ConstraintRejections.Add(float64(len(rejected)))
	}

	return admitted
}

// EnableConstraintAwareOrdering wires a constraint.StateManager into the chain,
// activating the M_D endogenous architecture. Called by the consenter during
// chain setup when a ConstraintEvaluator is configured for the channel.
//
// Calling this after Start() is a programming error.
func (c *Chain) EnableConstraintAwareOrdering(mgr *constraint.StateManager) {
	c.constraintMgr = mgr
}

