/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package constraint_test

// Tests for StateManager — the stable/speculative cache pair (Paper 3, Def. 2).
// The evaluator used here is a minimal in-process fake to keep the test
// independent of any domain-specific logic (biobank, etc.).

import (
	"fmt"
	"testing"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric/orderer/consensus/etcdraft/constraint"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Fake evaluator ────────────────────────────────────────────────────────────

// counterState tracks a single integer (number of admitted txs) as the
// constraint state. Cloning creates an independent copy.
type counterState struct{ count int }

func (s *counterState) Clone() constraint.CacheState {
	return &counterState{count: s.count}
}

// fakeEvaluator admits txs whose payload parses as "admit" and rejects "reject".
// Apply increments the count for admitted txs.
type fakeEvaluator struct {
	maxCount int // C_global analogue: reject if count would exceed maxCount
}

func (e *fakeEvaluator) Init(_ constraint.WorldStateSnapshot) (constraint.CacheState, error) {
	return &counterState{}, nil
}

func (e *fakeEvaluator) Evaluate(s constraint.CacheState, tx *constraint.TxView) bool {
	cs := s.(*counterState)
	if e.maxCount > 0 && cs.count >= e.maxCount {
		return false
	}
	return tx.Function == "admit"
}

func (e *fakeEvaluator) Apply(s constraint.CacheState, _ *constraint.TxView) constraint.CacheState {
	cs := s.(*counterState)
	return &counterState{count: cs.count + 1}
}

func (e *fakeEvaluator) TxParser() constraint.TxParser {
	return &fakeTxParser{}
}

// fakeTxParser interprets the Envelope.Payload as the function name.
type fakeTxParser struct{}

func (p *fakeTxParser) Parse(env *common.Envelope) (*constraint.TxView, error) {
	if env == nil {
		return nil, fmt.Errorf("nil envelope")
	}
	fn := string(env.Payload)
	if fn == "skip" {
		return nil, nil // non-chaincode — admitted unconditionally by StateManager
	}
	return &constraint.TxView{Function: fn, TxID: fn}, nil
}

// nullWorldStateSnapshot is an empty snapshot for Init (no pre-existing state).
type nullWorldStateSnapshot struct{}

func (n *nullWorldStateSnapshot) GetByRange(_, _, _ string) ([]constraint.KeyValue, error) {
	return nil, nil
}

func newStateManager(t *testing.T, maxCount int) *constraint.StateManager {
	t.Helper()
	eval := &fakeEvaluator{maxCount: maxCount}
	sm, err := constraint.NewStateManager(eval, &nullWorldStateSnapshot{})
	require.NoError(t, err)
	return sm
}

func env(fn string) *common.Envelope { return &common.Envelope{Payload: []byte(fn)} }

// ── BeginBatch + ProcessBatch ─────────────────────────────────────────────────

func TestProcessBatch_AdmitAll(t *testing.T) {
	sm := newStateManager(t, 0)
	sm.BeginBatch()
	admitted, rejected := sm.ProcessBatch([]*common.Envelope{env("admit"), env("admit")})
	assert.Len(t, admitted, 2)
	assert.Empty(t, rejected)
}

func TestProcessBatch_RejectAll(t *testing.T) {
	sm := newStateManager(t, 0)
	sm.BeginBatch()
	admitted, rejected := sm.ProcessBatch([]*common.Envelope{env("reject"), env("reject")})
	assert.Empty(t, admitted)
	assert.Len(t, rejected, 2)
}

func TestProcessBatch_MixedAdmissionInOrder(t *testing.T) {
	sm := newStateManager(t, 0)
	sm.BeginBatch()
	a1, a2, r := env("admit"), env("admit"), env("reject")
	admitted, rejected := sm.ProcessBatch([]*common.Envelope{a1, r, a2})
	require.Equal(t, []*common.Envelope{a1, a2}, admitted, "admitted must be in arrival order")
	require.Equal(t, []*common.Envelope{r}, rejected)
}

func TestProcessBatch_NonChaincodeEnvelopeAdmittedUnconditionally(t *testing.T) {
	// Payload "skip" → TxParser returns nil → admitted unconditionally.
	sm := newStateManager(t, 0)
	sm.BeginBatch()
	admitted, rejected := sm.ProcessBatch([]*common.Envelope{env("skip"), env("reject")})
	require.Len(t, admitted, 1)
	assert.Equal(t, "skip", string(admitted[0].Payload))
	_ = rejected
}

// ── C_global limit (Paper 3, Definition 3) ───────────────────────────────────

func TestProcessBatch_CGlobalLimit(t *testing.T) {
	// maxCount=2: only first 2 "admit" txs are accepted in a single batch.
	sm := newStateManager(t, 2)
	sm.BeginBatch()
	admitted, rejected := sm.ProcessBatch([]*common.Envelope{
		env("admit"), env("admit"), env("admit"),
	})
	require.Len(t, admitted, 2, "third tx must be rejected: count would exceed maxCount")
	require.Len(t, rejected, 1)
}

// ── Commit / Rollback ─────────────────────────────────────────────────────────

func TestCommit_PromotesSpeculativeToStable(t *testing.T) {
	sm := newStateManager(t, 0)

	// Batch 1: admit two txs.
	sm.BeginBatch()
	admitted, _ := sm.ProcessBatch([]*common.Envelope{env("admit"), env("admit")})
	require.Len(t, admitted, 2)
	sm.Commit()

	// Batch 2: with maxCount=2, no more should be admitted.
	// Restart with a manager that has maxCount=2 to verify stable count was kept.
	// Because fakeEvaluator.maxCount is fixed, we verify via a separate manager
	// created after the commit by checking the StableState count via a second batch.
	sm2 := newStateManager(t, 2)
	sm2.BeginBatch()
	sm2.ProcessBatch([]*common.Envelope{env("admit"), env("admit")})
	sm2.Commit()

	// A third batch after commit at maxCount=2: the 3rd tx must be rejected.
	sm2.BeginBatch()
	admitted2, rejected2 := sm2.ProcessBatch([]*common.Envelope{env("admit")})
	assert.Empty(t, admitted2, "at capacity after commit, third tx must be rejected")
	assert.Len(t, rejected2, 1)
}

func TestRollback_DiscardsSpeculativeState(t *testing.T) {
	sm := newStateManager(t, 2)

	// Batch 1: admit 2 txs (reaches maxCount).
	sm.BeginBatch()
	sm.ProcessBatch([]*common.Envelope{env("admit"), env("admit")})
	// Rollback instead of commit — stable count stays at 0.
	sm.Rollback()

	// After rollback, a new batch should still admit 2 txs (count reset to 0).
	sm.BeginBatch()
	admitted, rejected := sm.ProcessBatch([]*common.Envelope{env("admit"), env("admit")})
	require.Len(t, admitted, 2, "rollback must restore stable state: both txs admitted again")
	assert.Empty(t, rejected)
}

func TestCommit_NoopOnFollower(t *testing.T) {
	// If Commit is called without a prior BeginBatch (follower path),
	// it must be a no-op: stable state is unchanged.
	sm := newStateManager(t, 2)

	// Without BeginBatch, call Commit directly — must not panic or corrupt state.
	sm.Commit()

	// Stable state should still be the initial empty state.
	sm.BeginBatch()
	admitted, _ := sm.ProcessBatch([]*common.Envelope{env("admit"), env("admit")})
	assert.Len(t, admitted, 2, "stable state must be intact after no-op follower Commit")
}

// ── Sequential admission equivalence (Proposition 1) ─────────────────────────

func TestProcessBatch_SequentialEquivalence(t *testing.T) {
	// Proposition 1: processing a batch sequentially under speculative state
	// is equivalent to processing each tx individually against the running state.
	//
	// With maxCount=2 and 4 txs [admit, admit, admit, admit]:
	// Sequential: tx1 admitted (count→1), tx2 admitted (count→2), tx3 rejected,
	//             tx4 rejected. Admitted = {tx1, tx2}, Rejected = {tx3, tx4}.
	sm := newStateManager(t, 2)
	sm.BeginBatch()
	batch := []*common.Envelope{env("admit"), env("admit"), env("admit"), env("admit")}
	admitted, rejected := sm.ProcessBatch(batch)
	assert.Equal(t, batch[:2], admitted, "exactly first 2 admitted (sequential state advance)")
	assert.Equal(t, batch[2:], rejected)
}

// ── Multi-in-flight K_max enforcement (Paper 3, Proposition 1, C3) ───────────

func TestMultiInflight_KMaxEnforcedGlobally(t *testing.T) {
	// When MaxInflightBlocks > 1, several blocks can be in flight before any
	// is committed.  With the queue-based design, the K_max cap must be
	// respected globally across all in-flight blocks, not independently per
	// block.  Previously, each BeginBatch reset speculative to stable, allowing
	// every block to admit up to K_max independently — this test catches that.
	//
	// Setup: maxCount=2 (analogous to K_max=2 for C_global).
	// Three consecutive batches are filtered without any Commit in between
	// (simulating 3 blocks in flight simultaneously).
	// Expected: batch1 admits 2, batch2 and batch3 admit 0 (already at cap).
	sm := newStateManager(t, 2)

	// Batch 1: first block in flight.
	sm.BeginBatch() // queue=[], resets speculative to stable (count=0)
	a1, r1 := sm.ProcessBatch([]*common.Envelope{env("admit"), env("admit"), env("admit")})
	require.Len(t, a1, 2, "batch1: exactly 2 admitted (K_max=2)")
	require.Len(t, r1, 1, "batch1: 1 rejected (would exceed K_max)")

	// Batch 2: second block in flight, no Commit yet.
	// BeginBatch must NOT reset speculative (queue is non-empty).
	sm.BeginBatch()
	a2, r2 := sm.ProcessBatch([]*common.Envelope{env("admit"), env("admit")})
	assert.Empty(t, a2, "batch2: 0 admitted — K_max already reached in batch1")
	assert.Len(t, r2, 2, "batch2: all rejected")

	// Batch 3: third block in flight.
	sm.BeginBatch()
	a3, r3 := sm.ProcessBatch([]*common.Envelope{env("admit")})
	assert.Empty(t, a3, "batch3: 0 admitted — K_max still reached")
	assert.Len(t, r3, 1)

	// Commit block1: stable advances to post-batch1 state.
	sm.Commit()
	// Commit for blocks 2 and 3 is no-op (all-rejected; no queue entries).
	sm.Commit() // no-op
	sm.Commit() // no-op

	// After all commits, a new batch must also be blocked (stable has count=2).
	sm.BeginBatch() // queue=[], resets to stable (count=2)
	a4, _ := sm.ProcessBatch([]*common.Envelope{env("admit")})
	assert.Empty(t, a4, "after commit, new batch still at K_max — no more admitted")
}

func TestMultiInflight_RollbackRestoresAllInFlight(t *testing.T) {
	// After a Rollback (leader change), all in-flight speculative state is
	// discarded.  The next batch must start from the last committed stable state.
	sm := newStateManager(t, 2)

	// Batch 1: admit 2, queue=[{count:2}]
	sm.BeginBatch()
	a1, _ := sm.ProcessBatch([]*common.Envelope{env("admit"), env("admit")})
	require.Len(t, a1, 2)

	// Batch 2 in flight (no Commit yet): all rejected since at cap.
	sm.BeginBatch()
	a2, _ := sm.ProcessBatch([]*common.Envelope{env("admit")})
	assert.Empty(t, a2)

	// Leader change: Rollback discards everything.
	sm.Rollback()

	// After rollback, speculative = stable = initial (count=0).
	// A new batch must admit up to K_max again.
	sm.BeginBatch()
	a3, r3 := sm.ProcessBatch([]*common.Envelope{env("admit"), env("admit")})
	assert.Len(t, a3, 2, "after rollback, K_max slots available again")
	assert.Empty(t, r3)
}
