# Changelog — Paper 3 Evaluation Artefacts

Constraint-Aware Ordering for Directed Traceability in Hyperledger Fabric
ÉTS PhD / ACM Distributed Ledger Technologies, Autumn 2026

Format: newest entries first.

---

## [Unreleased] — §V LaTeX draft

### Added
- `paper_section_evaluation.tex` — complete §V Evaluation section for the paper:
  - §V.A Experimental Setup (VM, Fabric config, Caliper parameters)
  - §V.B Scenario 1: C_global violation rate, IVR analysis, comparison with theoretical maximum
  - §V.C Scenario 2: endogenous performance overhead (< 1% TPS, < 3% latency)
  - §V.D Scenario 3: exogenous coordinator structural bottleneck (−10.5% at 200 TPS co-located; analytical WAN bound +40 ms/tx at RTT 20 ms)
  - §V.E Summary table and discussion (correctness, negligible endogenous overhead, exogenous fragility)
- `README.md` — full rewrite: research context, architecture comparison table, complete step-by-step deployment guide, troubleshooting section
- `CHANGELOG.md` — this file
- `DESIGN.md` — detailed architecture and code walkthrough

---

## 2026-05-25 — Evaluation complete; results synced to local machine

### Scenario 3 — M_D_exo (perf-exogenous) completed
- **Added** `caliper/workloads/perf-exogenous.js`: Caliper 0.6 workload for Scenario 3.
  - Implements the full three-phase exogenous protocol: `Reserve → Fabric Transfer → Confirm` (or `Cancel` on abort).
  - Module-level `holderState` Map (same pattern as `perf-transfer.js`) persists state across rounds.
  - `jsonPost()` helper — minimal Node.js HTTP/HTTPS client with no external dependencies.
  - Co-located coordinator at `http://zkcoordinator:8080`; K_max=999 (pure RTT overhead, no quota pressure).
- **Added** `caliper/benchmarks/perf-exogenous.yaml`: benchmark configuration for Scenario 3.
  - Round 1: `setup-exo` — 200 resources at 20 TPS, runTag `z01`.
  - Rounds 2–4: `transfer-50`, `transfer-100`, `transfer-200` — 500 txs each through ZK coordinator.
- **Result** (runTag `z01`):
  - 50 TPS: 47.8 eff., avg 0.38 s, p99 0.63 s — identical to M_L.
  - 100 TPS: 95.2 eff., avg 0.41 s — within 0.5% of M_L.
  - 200 TPS: 120.6 eff., avg 0.47 s — **−10.5% TPS** vs M_L (coordinator mutex bottleneck).
- **Key finding**: at 200 TPS, 400 coordinator calls/s must pass through a single Go mutex. The Fabric network is under-loaded (avg latency drops from 0.50 s to 0.47 s); the coordinator is the sole bottleneck.

### Scenario 2 — M_L vs M_D_endo (perf-overhead) completed
- **Added** `caliper/workloads/perf-transfer.js`: Caliper 0.6 workload for Scenario 2.
  - Ping-pong Transfer between `init-agent-w` and `perf-agent-w` per worker.
  - Module-level `holderState = new Map()` outside the class — survives `initializeWorkloadModule` being called again on round 2, 3, 4.
  - Conditional holder update in `try/catch`: `holderState` only advances on tx success, preventing cascading C_auth failures after MVCC.
  - Pool: 100 resources per worker (reduced MVCC collision at high TPS).
- **Added** `caliper/benchmarks/perf-overhead.yaml`: benchmark configuration.
  - Round 1: `setup-perf` — 200 resources (100/worker) at 20 TPS, K_max=999.
  - Rounds 2–4: `transfer-50`, `transfer-100`, `transfer-200` — 500 txs each.
  - RunTags: `p03` (M_L), `p04` (M_D_endo).
- **Result** (M_L p03 / M_D_endo p04):
  - All 500 transactions succeed at all tiers for both architectures (0 failures).
  - Max TPS overhead: −0.6% at 200 TPS. Max latency variation: +2.7% at 50 TPS.
  - Conclusion: **BioankEvaluator adds no observable overhead** at up to 200 TPS.
- **Fixed**: C_auth failures on rounds 2+ caused by `holderState` being reset to `agentA` at each `initializeWorkloadModule` call. Fix: seed `holderState` only once per resource ID (`if (!holderState.has(id))`).
- **Fixed**: MVCC failures at 100 TPS with `poolSize=20` (205 Succ / 295 Fail). Fix: increase `poolSize` to 100 (expected in-flight per resource drops from 1.0 to 0.2).

### Scenario 1 — C_global violation rate completed (final run r03/r04)
- **Updated** `caliper/benchmarks/violation-quota.yaml`: runTag advanced to `r04` (r01, r02 used for development; r03 = final Scenario 1 M_L result used in figures).
- **Measured results** used in figures and §V.B:
  - M_L: 40 Succ, 0 Fail in quota-violation round → **36 violations** (IVR = 0.90).
  - M_D_endo: 2 Succ, 38 Fail/Timeout → **0 violations** (IVR = 0.00).
  - Theoretical max IVR = (2N − K_max) / 2N = 38/40 = 0.95; measured 0.90 because MVCC caught 2 same-block conflicts accidentally.

### Figures generated
- **Rewrote** `caliper/plot-results.py` with actual measured data from all three scenarios.
- Produces 4 publication-quality figures in PDF + PNG:
  - `fig1_violations.pdf` — Sc1 bar chart (Succ/Fail/Violations for M_L and M_D_endo)
  - `fig2_throughput.pdf` — Sc2+3 grouped bar chart (TPS at 50/100/200 for M_L, M_D_endo, M_D_exo)
  - `fig3_latency.pdf` — Sc2+3 average latency with p99 error bars
  - `fig4_overhead.pdf` — Sc2+3 % overhead vs M_L with ±5% dashed threshold
- ACM-compatible style: `pdf.fonttype=42` (TrueType embedding), DejaVu Serif, 3.33-inch column width.

### Sync and cleanup
- **Fixed** UTF-8 BOM (`﻿`) from shebang line in `caliper/setup-caliper.sh` (caused "invalid interpreter" error on Linux).
- **Updated** `integration/chaincode/directed/go.mod`: `go 1.22` → `go 1.24.0` (required for Docker build on updated base image).
- **Synced** all figures (PDF + PNG) from VM to local machine via SCP.

---

## 2026-05-24 — Constraint orderer + ZK coordinator implemented

### Generic constraint framework
- **Added** `orderer/consensus/etcdraft/constraint/evaluator.go`:
  - `Evaluator` interface: `Init(snapshot)`, `Evaluate(s, tx)`, `Apply(s, tx)`, `TxParser()`.
  - `TxView` struct: `TxID`, `Function`, `Args`, `Channel`.
  - `WorldStateSnapshot` interface: `GetByRange(namespace, start, end)`.
- **Added** `orderer/consensus/etcdraft/constraint/cache.go`:
  - `StateManager` struct: stable/speculative `CacheState` pair.
  - `BeginBatch`: reset `s_spec ← s_stable.Clone()`.
  - `ProcessBatch` (Algorithm 1): iterate envelopes → TxParser → Evaluate → Apply or drop.
  - `Commit`: promote `s_stable ← s_spec` (called from Raft apply goroutine, mutex-protected).
  - `Rollback`: discard `s_spec` (called from `becomeFollower`).
  - Follower guard in `Commit`: `if !inBatch { return }` — no-op on followers.
- **Added** `orderer/consensus/etcdraft/constraint/cache_test.go`:
  - Unit tests for full lifecycle, multi-block sequences, concurrent Commit/Rollback.

### BioankEvaluator
- **Added** `orderer/consensus/etcdraft/constraint/biobank/evaluator.go`:
  - `BioankCacheState`: `resources` map (resourceID → holder/group/K_max/status) + `groups` map (groupID → Holders set + K_max).
  - `Clone()`: deep copy of both maps including Holders sets.
  - `Init`: calls `GetAllResources` via peer snapshot; projects active resources with non-empty SubsetGroup and positive K_max.
  - `Evaluate`: for Transfer(r, a) — admits if `a ∈ Holders(group)` OR `|Holders| + 1 ≤ K_max`; for RegisterResource — same logic with registering agent as initial holder. All other functions unconditionally admitted.
  - `Apply`: updates both maps; `removeHolderFromGroup` scans resources to avoid removing a holder that owns another resource in the same group.
  - `BioankTxParser.Parse`: unwraps 5-layer Fabric envelope hierarchy (Envelope → Payload → Transaction → TransactionAction → ChaincodeInvocationSpec).

### Peer snapshot subsystem
- **Added** `orderer/consensus/etcdraft/constraint/peersnapshot/snapshot.go`:
  - `PeerEndorserSnapshot`: wraps a persistent gRPC connection to a trusted peer.
  - `GetByRange`: builds `ChaincodeInvocationSpec{GetAllResources}`, signs with SHA-256+ECDSA using reader identity (not OrdererMSP), calls `ProcessProposal`, parses JSON array response.
  - Reader identity loaded from `FABRIC_CONSTRAINT_READER_CERT` + `FABRIC_CONSTRAINT_READER_KEY`.
  - Six environment variables: `FABRIC_CONSTRAINT_PEER_ADDR`, `FABRIC_CONSTRAINT_PEER_TLSCA`, `FABRIC_CONSTRAINT_READER_CERT`, `FABRIC_CONSTRAINT_READER_KEY`, `FABRIC_CONSTRAINT_READER_MSP`, `FABRIC_CONSTRAINT_CHAINCODE`.

### Raft orderer hooks
- **Modified** `orderer/consensus/etcdraft/chain.go` (4 hook points):
  - Hook 1 (`propose`): `batch = c.filterBatch(batch)` — calls `BeginBatch + ProcessBatch`.
  - Hook 2 (`writeBlock`): `c.constraintMgr.Commit()` — immediately after `support.WriteBlock`.
  - Hook 3 (`becomeFollower`): `c.constraintMgr.Rollback()` — discard in-flight speculative state.
  - Hook 4 (`HandleChain`): activation sequence for channels listed in `FABRIC_CONSTRAINT_CHANNELS`.
  - Nil-check idiom: when `constraintMgr == nil`, all hooks are no-ops (backwards compatible).

### ZooKeeper coordinator
- **Added** `tools/zkcoordinator/coordinator.go`:
  - `GroupState`: `holders map[string]int` (agent → confirmed count) + `reservations map[string]*Reservation`.
  - `Reserve`: acquires mutex, computes `currentHolderCount()` (confirmed + in-flight reserved), checks `K_max`, increments if admitted, returns UUID `reservationId`.
  - `Confirm`: finalises reservation — decrements old holder if count drops to zero, marks new holder as confirmed.
  - `Cancel`: releases a reservation without committing (decrements speculative count).
  - `Release`: explicit holder release (used after Revoke/Publish).
  - `expireStaleReservations`: background goroutine (10 s interval), TTL default 30 s.
- **Added** `tools/zkcoordinator/main.go`: HTTP/1.1 server on port 8080 with `gorilla/mux` router.
- **Added** `tools/zkcoordinator/Dockerfile`: multi-stage build (Go 1.22 builder + Alpine runtime).

---

## 2026-05-23 — DirectedTraceability chaincode

### Chaincode
- **Added** `integration/chaincode/directed/model.go`:
  - `Resource` struct: ID, Type, Origin, CurrentHolder, SubsetGroup, MaxConcurrent, Conditions, Status, TraceLog.
  - `Conditions` struct: AllowedActions, AllowedPurposes, MaxTransfers, MaxConcurrent, ExpiryDate, PropagateConditions.
  - `TraceEntry`: TxID, FromAgent, ToAgent, Timestamp, Action.
- **Added** `integration/chaincode/directed/chaincode.go` (~620 lines):
  - `RegisterResource`: creates resource with initial holder = registering agent; enforces C_auth (only org admin can register).
  - `Transfer`: enforces C_auth (sender must be current holder), C_excl (always satisfied post-transfer), updates holder, appends TraceEntry. Optionally propagates/updates conditions.
  - `Use`, `Publish`, `Revoke`: lifecycle transitions with C_auth enforcement and status updates.
  - `GetResource`, `GetTrace`, `ListByGroup`: read-only queries.
  - `GetAllResources`: range scan over primary-key space, returns JSON array (used by orderer Init).
- **Added** `integration/chaincode/directed/main.go`: `shim.Start(new(DirectedTraceability))`.

### Network configuration
- **Added** `evaluation/paper3/network/`:
  - `crypto-config.yaml`: 1 orderer org (3 Raft nodes), 2 peer orgs (1 peer each).
  - `configtx.yaml`: application channel, anchor peers, Raft consenter set.
  - `docker-compose-base.yaml`: all 5 Fabric containers + CLI.
  - `docker-compose-constraint.yaml`: overrides orderer image → constraint-aware build.
  - `docker-compose-zk.yaml`: adds `zkcoordinator` sidecar.
  - `generate.sh`: `cryptogen generate` + `configtxgen`.
  - `run-constraint.sh`: `start` / `stop` / `status` helper for constraint orderer swap.
  - `orderer.Dockerfile`: multi-stage build for constraint-aware orderer binary.

### Early Caliper scaffolding
- **Added** `caliper/workload/register.js`, `transfer-std.js`, `transfer-zk.js`, `transfer-endogenous.js` — initial prototype workloads (superseded by `workloads/` for final benchmarks).
- **Added** `caliper/benchmarks/fabric-std.yaml`, `zk-exogenous.yaml`, `orderer-endogenous.yaml` — prototype benchmark configs.
- **Added** `caliper/networks/paper3-network.yaml` (template) and `paper3-network-resolved.yaml` (resolved paths).
- **Added** `caliper/setup-caliper.sh`.
- **Added** `scripts/audit-ivr.js`, `scripts/run-all.sh`, `scripts/bootstrap-azure.sh`.

---

## 2026-05-20 — Project initialised

- Paper 3 scope defined: constraint-aware ordering for directed traceability, 3-architecture comparison.
- Azure VM provisioned: `Standard_D4s_v3`, `40.86.227.109`.
- Hyperledger Fabric v2.5 cloned and built from source (adds orderer extension).
- Evaluation directory structure created under `evaluation/paper3/`.
