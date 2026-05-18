package constraint

import (
	"sync"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
)

// StateManager maintains the stable/speculative CacheState pair described in
// Paper 3, Definition 2, and coordinates the lifecycle operations of
// Definition 1 (Init, Commit, Rollback).
//
// It is owned by the Raft leader's block construction goroutine.
// All public methods are called from that single goroutine under the
// sequential constraint C2 (Proposition 1), so no internal locking is
// required for the stable/speculative pair. The mu guard protects only the
// commit/rollback path which is called from the Raft apply goroutine.
type StateManager struct {
	mu          sync.Mutex
	evaluator   Evaluator
	stable      CacheState // s_stable: last Raft-committed state (Def. 2, I1)
	speculative CacheState // s_spec:   working copy for current batch  (Def. 2, I2)
	inBatch     bool       // true while a batch is under construction
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

// BeginBatch starts a new batch construction cycle.
// It clones the stable state into the speculative state (Def. 2, I2: s^spec_0 = s^stable).
// Must be called before the first Evaluate call in each batch.
func (sm *StateManager) BeginBatch() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.speculative = sm.stable.Clone()
	sm.inBatch = true
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
// The speculative state after this call reflects the net effect of all
// admitted transactions; it is promoted to stable by Commit on Raft success.
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
	return admitted, rejected
}

// Commit promotes the speculative state to stable after the Raft quorum
// confirms a block. This is the Commit operation of Definition 1 and
// corresponds to invariant I3 (Def. 2): s_stable ← s_spec_n.
//
// Called from the Raft apply goroutine after a successful consensus round.
//
// On follower nodes, writeBlock is called without a preceding BeginBatch/
// ProcessBatch (followers do not run the constraint evaluator — they commit
// whatever the leader admitted). In that case inBatch is false and Commit
// is a no-op: the follower's stable state was set correctly by Init at
// startup and will be rebuilt by Init again on re-election. A production
// deployment would replay each committed block through the evaluator here;
// for the Paper 3 benchmark (stable leader) this omission is inconsequential.
func (sm *StateManager) Commit() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if !sm.inBatch {
		// Follower path: no speculative state was built for this block.
		return
	}
	sm.stable = sm.speculative
	sm.inBatch = false
}

// Rollback discards the speculative state and restores the stable state after
// a Raft round failure. This is the Rollback operation of Definition 1 and
// corresponds to invariant I3 (Def. 2): discard s_spec, retain s_stable.
//
// Called from the Raft apply goroutine on consensus failure.
func (sm *StateManager) Rollback() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.speculative = sm.stable.Clone()
	sm.inBatch = false
}

// StableState returns a snapshot of the current stable state for inspection
// (e.g. by the ZooKeeper baseline's state query service).
func (sm *StateManager) StableState() CacheState {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.stable.Clone()
}
