package constraint

import (
	"sync"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
)

// StateManager maintains the stable/speculative CacheState pair described in
// Paper 3, Definition 2, and coordinates the lifecycle operations of
// Definition 1 (Init, Commit, Rollback).
//
// # Multi-in-flight correctness (Paper 3, Proposition 1, C3)
//
// When MaxInflightBlocks > 1, the Raft leader can propose several blocks
// before any of them is acknowledged by a quorum.  The original
// BeginBatch-resets-to-stable design would allow each of those blocks to
// independently admit up to K_max transactions, violating C_global across
// block boundaries.
//
// The queue-based design fixes this:
//
//   - BeginBatch resets speculative to stable ONLY when no blocks are
//     in flight (queue is empty).  When blocks are already in flight it is
//     a no-op, and ProcessBatch continues from the current accumulated
//     speculative state.
//
//   - ProcessBatch appends a snapshot of the post-batch speculative state
//     to the queue whenever at least one envelope is admitted (i.e., a block
//     WILL be proposed for this batch).  The queue therefore has exactly one
//     entry per proposed-but-not-yet-committed block.
//
//   - Commit pops the oldest queue entry and promotes it to stable,
//     advancing the constraint baseline by exactly one Raft-committed block.
//
//   - Rollback resets speculative to the current stable and clears the
//     queue, discarding all in-flight speculative state.
//
// Consequence: the evaluator sees K_max as a true global cap across ALL
// in-flight blocks, not per-block.
//
// # Thread safety
//
// StateManager is owned by the Raft leader's block-construction goroutine.
// BeginBatch and ProcessBatch are always called from that goroutine.
// Commit and Rollback are called from the Raft apply goroutine.
// The mu guard protects the queue and stable fields that are accessed from
// both goroutines.
type StateManager struct {
	mu          sync.Mutex
	evaluator   Evaluator
	stable      CacheState   // s_stable: last Raft-committed state (Def. 2, I1)
	speculative CacheState   // s_spec:   cumulative working state across in-flight blocks
	queue       []CacheState // one post-batch snapshot per proposed-but-uncommitted block
}

// NewStateManager creates a StateManager and initialises the stable state by
// reading the current world state snapshot through evaluator.Init. This
// corresponds to the Init operation of Definition 1 (Paper 3): the snapshot
// π(S_world) is applied once at startup to produce the initial s_stable.
func NewStateManager(evaluator Evaluator, snapshot WorldStateSnapshot) (*StateManager, error) {
	stable, err := evaluator.Init(snapshot)
	if err != nil {
		return nil, err
	}
	return &StateManager{
		evaluator:   evaluator,
		stable:      stable,
		speculative: stable.Clone(),
	}, nil
}

// BeginBatch marks the start of a new batch construction cycle.
//
// When no blocks are in flight (queue is empty) it resets the speculative
// state to the current stable state (Def. 2, I2: s^spec_0 = s^stable).
//
// When blocks are already in flight (queue is non-empty) it is a no-op:
// speculative already reflects the cumulative effect of all admitted
// transactions from earlier batches, and the next batch must continue from
// that point to preserve the K_max cap across block boundaries.
func (sm *StateManager) BeginBatch() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if len(sm.queue) == 0 {
		// No in-flight blocks: start fresh from the last committed state.
		sm.speculative = sm.stable.Clone()
	}
	// else: in-flight blocks exist; continue building on the cumulative
	// speculative state so that K_max is enforced globally, not per-block.
}

// ProcessBatch evaluates every envelope in the batch against the evolving
// speculative state, implementing Algorithm 1 (Paper 3).
//
// For each envelope:
//  1. Parse it into a TxView (skip non-chaincode envelopes).
//  2. Evaluate Conf(s_spec, tx).
//  3. If Conf = 1: admit, advance s_spec via Apply.
//  4. If Conf = 0: reject, keep s_spec unchanged.
//
// Returns admitted and rejected envelope slices in arrival order.
//
// If at least one envelope is admitted, ProcessBatch appends a snapshot of
// the post-batch speculative state to the queue.  This snapshot is consumed
// by Commit when the corresponding Raft entry is acknowledged.  If no
// envelopes are admitted, the queue is left unchanged (no block will be
// proposed for this batch, so no Commit will fire).
func (sm *StateManager) ProcessBatch(envelopes []*common.Envelope) (admitted, rejected []*common.Envelope) {
	parser := sm.evaluator.TxParser()

	for _, env := range envelopes {
		tx, err := parser.Parse(env)
		if err != nil || tx == nil {
			// Not a chaincode invocation subject to constraint evaluation
			// (e.g. config transaction). Admit unconditionally.
			admitted = append(admitted, env)
			continue
		}

		if sm.evaluator.Evaluate(sm.speculative, tx) {
			admitted = append(admitted, env)
			sm.speculative = sm.evaluator.Apply(sm.speculative, tx)
		} else {
			rejected = append(rejected, env)
		}
	}

	// Checkpoint the post-batch speculative state iff a block will be proposed.
	// filterBatch only proposes a block when len(admitted) > 0, so we use the
	// same condition here to keep the queue length in sync with blockInflight.
	if len(admitted) > 0 {
		sm.mu.Lock()
		sm.queue = append(sm.queue, sm.speculative.Clone())
		sm.mu.Unlock()
	}

	return admitted, rejected
}

// Commit promotes the oldest queued speculative snapshot to stable after the
// Raft quorum confirms a block.  This is the Commit operation of Definition 1
// and corresponds to invariant I3 (Def. 2): s_stable ← s_spec_n.
//
// Called from the Raft apply goroutine after a successful consensus round.
// Each call to Commit corresponds to exactly one proposed block (one queue entry).
//
// On follower nodes, writeBlock is called without a preceding BeginBatch/
// ProcessBatch (followers do not run the constraint evaluator). In that case
// the queue is empty and Commit is a no-op.
func (sm *StateManager) Commit() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if len(sm.queue) == 0 {
		// Follower path or all-rejected-batch path: no queued state to promote.
		return
	}
	sm.stable = sm.queue[0]
	sm.queue = sm.queue[1:]
}

// Rollback discards all in-flight speculative state and restores the stable
// state after a Raft round failure (e.g. leader change).  This is the Rollback
// operation of Definition 1 and corresponds to invariant I3 (Def. 2): discard
// s_spec, retain s_stable.
//
// Called from the Raft apply goroutine on consensus failure.
func (sm *StateManager) Rollback() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.speculative = sm.stable.Clone()
	sm.queue = nil
}

// StableState returns a snapshot of the current stable state for inspection
// (e.g. by the ZooKeeper baseline's state query service).
func (sm *StateManager) StableState() CacheState {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.stable.Clone()
}
