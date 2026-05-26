# Design Document — Constraint-Aware Ordering for Directed Traceability

**Paper 3 — ÉTS PhD / ACM Distributed Ledger Technologies, Autumn 2026**

This document explains in detail the motivation, architecture, design decisions,
and implementation of every component built for Paper 3. It is intended as a
self-contained reference for anyone reading the code cold or revisiting it after
a long break.

---

## Table of Contents

1. [Problem Statement](#1-problem-statement)
2. [Why Chaincode Enforcement Fails for C_global](#2-why-chaincode-enforcement-fails-for-c_global)
3. [The Three Architectures — Design Rationale](#3-the-three-architectures--design-rationale)
4. [The DirectedTraceability Chaincode](#4-the-directedtraceability-chaincode)
5. [The Generic Constraint Framework (orderer/constraint)](#5-the-generic-constraint-framework-orderercconstraint)
6. [The BioankEvaluator](#6-the-biobankevaluator)
7. [The Peer Snapshot Subsystem](#7-the-peer-snapshot-subsystem)
8. [Raft Orderer Hook Points](#8-raft-orderer-hook-points)
9. [The ZooKeeper Coordinator](#9-the-zookeeper-coordinator)
10. [Caliper Workload Design](#10-caliper-workload-design)
11. [Benchmark Scenarios — Design Decisions](#11-benchmark-scenarios--design-decisions)
12. [Key Engineering Problems Solved](#12-key-engineering-problems-solved)
13. [Correctness Argument](#13-correctness-argument)
14. [Performance Model](#14-performance-model)
15. [Data Flow Diagrams](#15-data-flow-diagrams)

---

## 1. Problem Statement

A *directed-traceability* system records the custody chain of a resource as it
moves between agents in a multi-party consortium. Each resource carries a set of
governance conditions that travel with it through transfers — analogous to a
legal title that imposes restrictions on its future use.

The critical governance constraint for this paper is:

> **C_global** — At most K_max distinct agents may simultaneously hold resources
> that belong to the same *SubsetGroup* in the consortium ledger.

This constraint models real-world scenarios such as:
- A biobank specimen may be split and loaned to at most 2 research institutions at once.
- A dataset licence permits concurrent access by at most 5 verified entities.
- A financial instrument may be held by at most 1 custodian per jurisdiction.

The challenge is that this constraint is **global**: its evaluation requires
knowing the current holder set across *all* resources in the group, which is
spread across multiple ledger state entries. No single chaincode simulation has
an atomic, consistent view of all of them under concurrent load.

---

## 2. Why Chaincode Enforcement Fails for C_global

### The MVCC Window Problem

Hyperledger Fabric uses an optimistic concurrency model:

1. **Endorsement**: each peer simulates the chaincode transaction against a
   *snapshot* of the world state taken at a specific block height.
2. **Ordering**: the orderer sequences transactions into blocks.
3. **Validation**: each peer checks that the read-set of each transaction still
   matches the current world state (MVCC check). Conflicting transactions are
   marked invalid.

For **local** constraints like C_auth ("is this agent the current holder?"), the
MVCC check catches violations: if two agents both try to transfer the same
resource, only the first commit wins; the second fails the read-set check because
the holder field was modified.

For **C_global**, the situation is different. Consider K_max = 2 with groups
currently having 1 holder:

```
World state:   group G → Holders = {Alice}   (|S_sub| = 1)
K_max = 2
```

Two concurrent Transfer transactions arrive:
- Tx1: Transfer resource R1 to Bob  → simulation sees |S_sub| = 1 < K_max → admits
- Tx2: Transfer resource R2 to Carol → simulation sees |S_sub| = 1 < K_max → admits

Both transactions are endorsed against **the same snapshot** (Holders = {Alice}).
Both are ordered into the same block. Both pass MVCC because they modify
*different* keys (R1 and R2 are different state entries with no read-set
overlap). Both are committed. Now |S_sub| = 3 > K_max = 2.

This is an *intra-block violation*. The IVR (Intra-block Violation Rate) measures
the fraction of committed transactions that produce such violations.

### Why MVCC Doesn't Help Here

MVCC only protects the *same* key from concurrent modification. Since R1 and R2
are different resources with different keys, their MVCC read-sets do not overlap,
and the validator has no mechanism to detect the inter-resource constraint
violation. The constraint is *relational* — it depends on the combination of
multiple resources' states.

The IVR is mathematically bounded below by:
```
IVR ≥ (N_concurrent − K_max) / N_concurrent
```
where N_concurrent is the number of simultaneous Transfer transactions targeting
the same group. With K_max = 2 and 20 concurrent transfers, IVR ≥ 18/20 = 0.90.

---

## 3. The Three Architectures — Design Rationale

### M_L — Baseline (fabric-std)

The simplest architecture: use Hyperledger Fabric as-is. `C_auth` and `C_excl`
are enforced by the chaincode at endorsement time. `C_global` is not enforced;
the system relies on application-level retry logic or accepts violations.

**IVR > 0** in any scenario with concurrent transfers to the same group.

This is the *null hypothesis* for the paper: if we do nothing, how bad is it?
The benchmark (Scenario 1) confirms IVR = 0.90 at K_max=2 with 40 concurrent
transfers.

### M_D_exo — Exogenous (ZK coordinator)

An external HTTP coordinator serialises admission decisions:

1. Client calls `POST /v1/reserve` with group ID, target agent, and K_max.
2. Coordinator checks the in-memory holder count under a mutex.
3. If admitted: returns a `reservationId`; client proceeds to endorse and submit.
4. After Fabric commit: client calls `POST /v1/confirm` (success) or `POST /v1/cancel` (abort).

**IVR = 0** as long as every client executes the protocol faithfully.

The key structural problem: the coordinator is a **single point of serialisation**.
Its mutex gates all Reserve calls. At high TPS, this becomes the bottleneck —
not Fabric, not the orderer, but the coordinator's own lock. Additionally:
- If a client crashes between Reserve and Cancel, the slot is leaked until TTL expiry.
- If the coordinator crashes, all in-flight reservations are lost.
- If the coordinator is remote (WAN), 2 × RTT is added to every transaction's
  critical path.

### M_D_endo — Endogenous (constraint-aware orderer)

The global constraint is evaluated **inside** the Raft ordering loop, as part of
block construction. The leader orderer maintains a *stable/speculative cache*
of the current holder state for all SubsetGroups. For each incoming envelope, it
evaluates C_global against the speculative cache before including the envelope in
a proposed block.

**IVR = 0** always, not just when clients behave correctly, because:
- The orderer sees all transactions before they are committed.
- Block construction is sequential within the Raft leader.
- The speculative cache is updated atomically with each admitted envelope.
- Unadmitted envelopes never reach the ledger.

**No coordination overhead**: no extra round-trips, no external service, no
client-side protocol changes.

**Cost**: the orderer must maintain the constraint cache (memory) and evaluate
each Transfer envelope (CPU). As Scenario 2 shows, this cost is < 1% at up to
200 TPS.

---

## 4. The DirectedTraceability Chaincode

**Location**: `integration/chaincode/directed/`

### Model

```go
// model.go
type Resource struct {
    ID             string
    Type           string
    Origin         string
    CurrentHolder  string
    SubsetGroup    string     // group name for C_global; empty = no global constraint
    MaxConcurrent  int        // K_max for this resource's group
    Conditions     Conditions // governance rules that travel with the resource
    Status         string     // "active" | "inactive"
    TraceLog       []TraceEntry
}

type Conditions struct {
    AllowedActions   []string
    AllowedPurposes  []string
    MaxTransfers     int
    MaxConcurrent    int      // also stored here for completeness
    ExpiryDate       string   // RFC3339 or empty
    PropagateConditions bool  // if true, conditions are inherited by next holder
}

type TraceEntry struct {
    TxID      string
    FromAgent  string
    ToAgent    string
    Timestamp  string
    Action     string
}
```

### Key functions

#### `RegisterResource`
- Called by an organisation admin to introduce a new resource into the system.
- Creates the `Resource` struct with `Status = "active"` and `CurrentHolder = caller`.
- If `SubsetGroup` is non-empty and `MaxConcurrent > 0`, the orderer's cache will
  include this resource in its SubsetGroup tracking.
- Writes the resource under key `resourceID` in the world state.
- Also writes a composite key `\x00group\x00{groupID}\x00{resourceID}\x00` for
  efficient group-level queries.

#### `Transfer`
- The core function. Enforces all local constraints:
  - **C_auth**: `stub.GetCreator()` must correspond to `resource.CurrentHolder`.
  - **C_excl**: the resource transitions atomically from one holder to another;
    no split-custody possible at the chaincode level.
  - **C_conditions**: checks AllowedActions, MaxTransfers, ExpiryDate.
- Updates `resource.CurrentHolder = toAgent`.
- If `PropagateConditions = true`, merges or replaces the resource's conditions
  with the ones provided in the transfer request.
- Appends a `TraceEntry` to the trace log.
- **Does NOT check C_global**: this is the orderer's responsibility.

#### `GetAllResources`
- Special read-only function used exclusively by the orderer's Init sequence.
- Performs a key range scan over the primary-key space (`\x20` to `\x7E` —
  printable ASCII), excluding composite-key entries (null-byte prefix).
- Returns all `Resource` objects as a JSON array.
- Called once at orderer startup via the peer snapshot subsystem.

### Why GetAllResources instead of replaying the log?

Replaying the block log from genesis would be:
1. **Proportional to log length**, not state size. In a long-running system with
   thousands of blocks, startup would be slow.
2. **Complex**: the orderer would need to implement the same state-transition
   logic as the chaincode, duplicating business logic.
3. **Fragile**: any chaincode upgrade that changes the state encoding would break
   the replayer.

A direct world-state query via the peer is O(state size), simple, and always
up-to-date regardless of log length. The tradeoff is that the peer must be
reachable at orderer startup — enforced by making startup fail-fast if the peer
is unavailable.

---

## 5. The Generic Constraint Framework (orderer/constraint)

**Location**: `orderer/consensus/etcdraft/constraint/`

The framework is designed to be **use-case agnostic**. The orderer knows nothing
about biobank specimens, SubsetGroups, or K_max. It only knows how to maintain
a stable/speculative state pair and call `Evaluate`/`Apply` on each envelope.

### The Evaluator Interface

```go
// evaluator.go
type Evaluator interface {
    // Init builds the initial stable cache from a world-state snapshot.
    // Called once at orderer startup (HandleChain).
    Init(snapshot WorldStateSnapshot) error

    // Evaluate returns true iff tx is admissible in state s.
    // Must not modify s. May be called concurrently with other reads.
    Evaluate(s CacheState, tx TxView) bool

    // Apply returns the new cache state after admitting tx.
    // Called only when Evaluate returned true.
    Apply(s CacheState, tx TxView) CacheState

    // TxParser returns the parser for this evaluator.
    TxParser() TxParser
}

type TxView struct {
    TxID     string
    Function string
    Args     [][]byte
    Channel  string
}

type CacheState interface {
    Clone() CacheState
}

type WorldStateSnapshot interface {
    GetByRange(namespace, startKey, endKey string) ([]KeyValue, error)
}
```

### The StateManager (stable/speculative pair)

```go
// cache.go
type StateManager struct {
    evaluator  Evaluator
    stable     CacheState   // committed state — matches ledger at last committed block
    speculative CacheState  // in-flight state — includes all admitted envelopes in current batch
    inBatch    bool         // follower guard: true only when leader is in BeginBatch..Commit cycle
    mu         sync.Mutex   // guards Commit/Rollback path (called from Raft apply goroutine)
}
```

The two-state design is necessary because:
- **Stable** = what was committed. If the current Raft round fails (leader
  abdication, timeout), the speculative state must be discarded and regenerated
  from the stable state.
- **Speculative** = what we're building. Each admitted envelope advances the
  speculative state so that subsequent envelopes in the same batch are evaluated
  against an up-to-date view.

### Algorithm 1 — ProcessBatch

```
BeginBatch:
    s_spec ← s_stable.Clone()
    inBatch ← true

ProcessBatch(envelopes):
    admitted ← []
    for each envelope in envelopes:
        tx ← TxParser.Parse(envelope)
        if tx == nil:
            admitted.append(envelope)   // config tx → always admit
            continue
        if Evaluate(s_spec, tx):
            s_spec ← Apply(s_spec, tx)
            admitted.append(envelope)
        // else: envelope silently dropped
    return admitted

Commit:
    if !inBatch: return    // follower guard
    mu.Lock()
    s_stable ← s_spec
    inBatch ← false
    mu.Unlock()

Rollback:
    mu.Lock()
    s_spec ← s_stable.Clone()
    inBatch ← false
    mu.Unlock()
```

### Why Clone() instead of a diff/patch approach?

**Simplicity**: deep-copying the entire state on `BeginBatch` is O(state size),
not O(batch size). For the biobank use case with a few hundred resources, this
is microseconds. A diff/patch approach would require tracking per-key deltas and
merging them on `Commit` — more complex, harder to prove correct, and only
necessary at very large state sizes (tens of thousands of resources).

**Correctness**: the deep copy guarantees that `Evaluate` on the speculative
state cannot accidentally modify the stable state. No aliasing bugs are possible.

---

## 6. The BioankEvaluator

**Location**: `orderer/consensus/etcdraft/constraint/biobank/evaluator.go`

### Cache Structure

```go
type BioankCacheState struct {
    resources map[string]*ResourceEntry   // resourceID → {holder, group, kMax, status}
    groups    map[string]*GroupEntry       // groupID → {Holders set, kMax}
}

type GroupEntry struct {
    Holders map[string]struct{}  // set of current distinct holders
    KMax    int
}
```

The `groups` map is the primary data structure for C_global evaluation.
`|Holders|` is the quantity compared against `KMax` during `Evaluate`.

The `resources` map is needed for:
1. **Init**: projecting the world state into groups.
2. **Apply → removeHolderFromGroup**: checking whether a departing agent still
   holds other resources in the same group before removing them from `Holders`.

### Evaluate Logic

For `Transfer(resourceID, fromAgent, transferJSON)`:
1. Look up the resource in `resources`. If not found → unconditionally admit
   (resource may not be in a group, or it's a new resource being created).
2. Extract the target agent from `transferJSON`.
3. Look up the group in `groups`.
4. If `toAgent` is already in `Holders` → admit (holder count doesn't increase).
5. Else if `|Holders| + 1 ≤ KMax` → admit.
6. Else → reject.

For `RegisterResource(resourceID, agent, resourceJSON)`:
- Same logic as Transfer, with `agent` as the "target holder".

For all other functions (`Use`, `Publish`, `Revoke`, `GetResource`, etc.):
- Unconditionally admit. These functions do not affect the holder set.

### Apply Logic

For an admitted `Transfer(resourceID, _, transferJSON)`:
1. Remove `fromAgent` from `groups[group].Holders` **only if** they hold no other
   active resource in the same group (calls `removeHolderFromGroup`).
2. Add `toAgent` to `groups[group].Holders`.
3. Update `resources[resourceID].Holder = toAgent`.

The `removeHolderFromGroup` scan iterates over all resources to check for
remaining holdings. This is O(|resources|) but runs synchronously in the block
construction goroutine, where blocking is acceptable. At 200 TPS with ~200
resources, this scan takes microseconds.

### Why Does the Evaluator Only Track Transfers?

`Use`, `Publish`, and `Revoke` are admitted unconditionally by the evaluator
because they don't introduce new holders. However, `Revoke` and `Publish` do
retire a resource (status → inactive), which should decrement the holder count.

The `Apply` implementation handles this: when a resource is deactivated, the
holder is removed from the group's Holders set (same `removeHolderFromGroup`
logic). This ensures the cache stays accurate over the full resource lifecycle.

---

## 7. The Peer Snapshot Subsystem

**Location**: `orderer/consensus/etcdraft/constraint/peersnapshot/snapshot.go`

### Problem

The orderer's MSP identity (OrdererMSP) cannot endorse chaincode proposals
because it is not a member of any application channel policy. We need to call
`GetAllResources` at orderer startup, but the orderer can't sign the proposal
with its own identity.

### Solution: Reader Identity

A dedicated *reader identity* — a regular application-org certificate and private
key — is configured for the orderer via environment variables:

```
FABRIC_CONSTRAINT_READER_CERT  = /path/to/reader.pem
FABRIC_CONSTRAINT_READER_KEY   = /path/to/reader_sk
FABRIC_CONSTRAINT_READER_MSP   = Org1MSP
```

The orderer loads these at startup and uses them to sign the `GetAllResources`
proposal. The reader identity is a member of the channel (can read state) but
has no special privileges beyond that. It is never used for transactions.

### Signing Without the Fabric MSP Stack

The Fabric MSP stack (used by peers and clients) is not imported by the orderer's
constraint extension to keep the dependency footprint small. Instead, signing is
implemented directly:

```
1. Load ECDSA private key from PEM file (crypto/ecdsa)
2. Compute SHA-256 hash of the serialised proposal bytes
3. Sign with ECDSA → (r, s) pair
4. DER-encode the signature
5. Attach to the ChaincodeProposalPayload envelope
```

This produces a valid Fabric-endorsed proposal that the peer will accept.

### gRPC Connection

The orderer dials the trusted peer at orderer startup using mutual TLS:
- Server TLS CA: `FABRIC_CONSTRAINT_PEER_TLSCA`
- Client certificate: the reader identity certificate

The gRPC connection is persistent (reused across potential future Init calls) and
is closed only when the channel is torn down.

---

## 8. Raft Orderer Hook Points

**Location**: `orderer/consensus/etcdraft/chain.go` (modified)

The constraint layer integrates via four minimal changes to the existing Raft
chain code, each a single nil-checked function call.

### Hook 1 — Block Proposal (propose)

```go
// In the block construction loop, immediately before proposing to Raft:
if c.constraintMgr != nil {
    batch = c.filterBatch(batch)
    if len(batch) == 0 {
        continue  // skip empty batches (all envelopes rejected)
    }
}
```

`filterBatch` wraps `BeginBatch + ProcessBatch`. This is the only place where
the evaluator is called during normal operation. The block construction goroutine
is the sole caller, ensuring sequential access.

### Hook 2 — Block Commit (writeBlock)

```go
// Immediately after support.WriteBlock returns:
if c.constraintMgr != nil {
    c.constraintMgr.Commit()
}
```

The placement is critical: the stable cache must be updated atomically with the
ledger write. No other goroutine can observe the ledger update before the cache
update, because `writeBlock` is called synchronously in the Raft apply goroutine.

### Hook 3 — Leader Abdication (becomeFollower)

```go
// When the Raft node transitions from leader to follower:
if c.constraintMgr != nil {
    c.constraintMgr.Rollback()
}
```

Any speculative state built for in-flight proposals will never be committed
(the new leader will rebuild blocks from the incoming log). Rollback discards
the speculative state, resetting to the last committed stable state.

### Hook 4 — Channel Activation (HandleChain)

```go
// In HandleChain, when channel appears in FABRIC_CONSTRAINT_CHANNELS:
snap, err := peersnapshot.New(peerConfig)
if err != nil { return nil, err }

evaluator := biobank.NewBioankEvaluator()
mgr, err := constraint.NewStateManager(evaluator, snap)
if err != nil { return nil, err }

chain.EnableConstraintAwareOrdering(mgr)
```

If any step fails, `HandleChain` returns an error and the orderer refuses to
start for that channel. This is a deliberate fail-fast design: it's better to
refuse to start than to run in an unconstrained state.

### The Nil-Check Idiom

All four hooks are guarded by `if c.constraintMgr != nil`. When the constraint
manager is nil (default for all channels not listed in `FABRIC_CONSTRAINT_CHANNELS`),
the hooks are no-ops with zero overhead. This preserves full backwards
compatibility: existing networks upgrading to the constraint orderer binary
continue to operate identically until they opt in by setting the environment
variables.

---

## 9. The ZooKeeper Coordinator

**Location**: `tools/zkcoordinator/`

Despite the name, this coordinator does not use Apache ZooKeeper. The name
reflects its role as an *external coordination service* in the M_D_exo
architecture, analogous to the coordination role that ZooKeeper plays in
distributed systems.

### State Model

```go
// coordinator.go
type GroupState struct {
    holders      map[string]int            // agent → confirmed hold count
    reservations map[string]*Reservation   // reservationID → {agent, timestamp, kMax}
    mu           sync.Mutex
}

type Reservation struct {
    Agent     string
    KMax      int
    CreatedAt time.Time
}
```

The `holders` map tracks confirmed holdings (post-Confirm). The `reservations`
map tracks in-flight reservations (post-Reserve, pre-Confirm/Cancel).

### currentHolderCount()

The key invariant is that the coordinator must not over-admit. The effective
holder count at any point is:

```
effective_count = |confirmed_holders| + |distinct_agents_with_pending_reservations|
```

The `currentHolderCount()` function computes this: it takes the confirmed holders
set, then adds any agents in pending reservations who are not already in the
confirmed set. This prevents the race where two concurrent Reserve calls both see
the confirmed count as 1 and both admit (bringing effective count to 3 when
K_max = 2).

### Reserve → Confirm/Cancel Protocol

```
Client                      Coordinator
  │                               │
  ├──POST /v1/reserve────────────>│  mutex.Lock()
  │  {group, agent, kMax}         │  check currentHolderCount() <= kMax
  │                               │  if ok: create reservation, return reservationId
  │<──{admitted:true, reservationId}  mutex.Unlock()
  │
  ├──[Fabric endorsement + submit]
  │
  ├──POST /v1/confirm────────────>│  mutex.Lock()
  │  {reservationId}              │  remove from reservations
  │                               │  add agent to holders (or increment count)
  │                               │  remove old holder if count = 0
  │<──{ok}                        │  mutex.Unlock()
```

### TTL-Based Expiry

The background goroutine runs every 10 seconds:

```go
func (c *Coordinator) expireStaleReservations() {
    for _, group := range c.groups {
        group.mu.Lock()
        for id, r := range group.reservations {
            if time.Since(r.CreatedAt) > c.reservationTTL {
                delete(group.reservations, id)
                // count is released implicitly
            }
        }
        group.mu.Unlock()
    }
}
```

Default TTL: 30 seconds. This handles client crashes between Reserve and
Cancel. The tradeoff: during the TTL window, the leaked reservation temporarily
tightens the effective K_max seen by other clients (they see a "phantom" holder).

### Why the Mutex Is a Bottleneck at High TPS

The mutex serialises all Reserve calls globally. At 200 TPS with 2 workers
sending 100 Reserve calls/second each:
- Total coordinator calls/second: 400 Reserve + 400 Confirm = 800 calls/second
- Each call: ~1 µs computation + goroutine scheduling overhead
- But the mutex means all 800 calls are sequential
- Go's sync.Mutex has a fairness mechanism that adds latency under contention

At 200 TPS the coordinator becomes saturated, capping effective throughput at
~120 TPS (−10.5%). This is the fundamental structural limitation of the
exogenous architecture under a single-coordinator deployment.

---

## 10. Caliper Workload Design

**Location**: `caliper/workloads/`

### Module-Level holderState Map

All performance workloads (`perf-transfer.js`, `perf-exogenous.js`) use a
module-level Map to track which agent currently holds each resource:

```javascript
// Outside the class — persists across round initializations in same Node.js process
const holderState = new Map();

class PerfTransferWorkload extends WorkloadModuleBase {
    async initializeWorkloadModule(...) {
        for (let i = 1; i <= poolSize; i++) {
            const id = `${prefix}-r-w${workerIndex}-${runTag}-${i}`;
            this.pool.push(id);
            // Seed only on first access — don't reset if already tracked
            if (!holderState.has(id)) {
                holderState.set(id, this.agentA);
            }
            this.holders.push(holderState.get(id));
        }
    }
}
```

**Why this matters**: Caliper calls `initializeWorkloadModule` at the start of
each round. A naive implementation would reset `holders` to `[agentA, agentA, ...]`
at the start of round 2 (`transfer-100`). But after round 1 (`transfer-50`),
half the resources are held by `agentB`. Resetting would cause the round 2
transfers to fail `C_auth` ("agent init-agent-0 is not the current holder").

The module-level Map persists because Node.js `require()` caches modules. The
same module instance — and therefore the same Map — is reused for all rounds
within a single Caliper run.

### Conditional Holder Update (try/catch)

```javascript
async submitTransaction() {
    const fromAgent = this.holders[idx];
    const toAgent = fromAgent === this.agentA ? this.agentB : this.agentA;

    try {
        await this.sutAdapter.sendRequests({
            contractFunction: 'Transfer',
            contractArguments: [resourceId, fromAgent, transferJSON],
        });
        // Only advance holder state on SUCCESS
        this.holders[idx] = toAgent;
        holderState.set(resourceId, toAgent);
    } catch (err) {
        // MVCC failure or network error — don't update holder
        throw err;
    }
}
```

If a transaction fails (MVCC conflict at high TPS), the holder state stays at
`fromAgent`. The next attempt will retry the transfer from `fromAgent`, which is
still the legitimate holder. Without this guard, an MVCC failure would cause the
holder state to advance in memory even though the ledger didn't change, leading
to a cascade of C_auth failures.

### Ping-Pong Pool Design

Each worker has a pool of `poolSize` resources (100 for Scenario 2/3). Resources
are transferred alternately between `agentA = init-agent-{w}` and
`agentB = perf-agent-{w}`. This ensures:

1. **C_auth always satisfied**: the workload knows the correct current holder.
2. **No MVCC from pool design**: each resource is only targeted by one worker
   (per-worker pool partitioning via `w${workerIndex}` in the ID).
3. **Low MVCC probability at high TPS**: with 100 resources and 50 TPS/worker,
   the expected in-flight count per resource is 50 × 0.4 s / 100 = 0.2.
   MVCC requires two simultaneous in-flight on the same resource, which happens
   with probability ~0.02 → negligible.

### setup-violation.js — Resource Registration

```javascript
// Registers N resources per worker in a per-worker SubsetGroup
// with a given K_max (MaxConcurrent)
async submitTransaction() {
    const i = ++this.txIndex;  // 1..poolSize
    const resourceId = `${prefix}-r-w${workerIndex}-${runTag}-${i}`;
    const groupId = `${prefix}-group-w${workerIndex}-${runTag}`;

    await this.sutAdapter.sendRequests({
        contractFunction: 'RegisterResource',
        contractArguments: [resourceId, JSON.stringify({
            subsetGroup: groupId,
            maxConcurrent: kMax,
            holder: `init-agent-${workerIndex}`,
        })],
    });
}
```

Per-worker groups (one group per worker) are important for Scenario 1: they
isolate the quota pressure within each worker's group, making the violation
pattern deterministic and measurable.

---

## 11. Benchmark Scenarios — Design Decisions

### Scenario 1 — Violation Rate

**K_max = 2**: chosen to make violations dramatic (not just marginal). With
K_max = 10 and 40 transfers, only 30 would be violations — less impactful.
K_max = 2 makes 38 out of 40 transfers violations (theoretical maximum).

**Pool size = 20/worker**: each resource gets its own dedicated Transfer
transaction (1 resource → 1 transaction → 1 new unique agent). This maximises
the number of distinct new holders introduced per burst, maximising IVR.

**50 TPS burst**: high enough to ensure all 40 transactions are in-flight
simultaneously (exceeds ordering rate), guaranteeing maximum concurrency overlap.

**2 workers**: doubles the concurrent load (each worker generates its own
group, its own burst). Not strictly necessary for the IVR measurement, but tests
the endogenous architecture under multi-worker concurrency.

### Scenario 2 — Performance Overhead

**Pool size = 100/worker**: the key insight is that MVCC probability is
`(txs_in_flight / pool_size)`. With 100/worker at 100 TPS (50/worker) and
0.4 s avg latency, txs_in_flight ≈ 20. MVCC probability per resource ≈ 20/100 = 0.2.
The probability that two specific txs target the same resource is 0.2/100 = 0.002
— negligible. Choosing `poolSize = 20` (the early mistake) gave 20/20 = 1.0,
causing pervasive MVCC.

**K_max = 999**: the constraint evaluator runs (Evaluate is called for every
Transfer) but always admits. This is a pure CPU overhead measurement, not a
quota test. If K_max were 2 with 500 transfers, half would be rejected, making
throughput comparison meaningless.

**500 txs per tier**: enough for a stable steady-state measurement. At 50 TPS,
500 txs takes 10 s — long enough for the Fabric batch pipeline to stabilise.

**3 TPS tiers (50/100/200)**: covers the range from well-below to slightly-above
the network's natural saturation point (which appears around 150 TPS given the
block batch timeout of 500 ms).

### Scenario 3 — Exogenous Overhead

**Same pool and TPS tiers as Scenario 2**: enables direct comparison. The only
difference is the workload module (`perf-exogenous.js` vs `perf-transfer.js`),
which adds the Reserve/Confirm calls.

**Co-located coordinator**: `http://zkcoordinator:8080` on the same Docker
bridge network as the Caliper workers. RTT < 1 ms. This is the **best-case**
scenario for the exogenous architecture — we're measuring the coordinator's
intrinsic mutex overhead, not network latency.

**K_max = 999 at coordinator**: same as Scenario 2. The coordinator always
admits, so we isolate protocol overhead from quota enforcement effects.

---

## 12. Key Engineering Problems Solved

### Problem 1: holderState Drift Across Rounds

**Symptom**: Scenario 2 round 2 (transfer-100) fails with
`C_auth: agent init-agent-0 is not the current holder of perf-r-w0-p01-1`.

**Root cause**: `initializeWorkloadModule` is called at the start of each round.
The original implementation initialised `this.holders = [agentA, agentA, ...]`
in the constructor, resetting it even if round 1 had advanced some resources to
`agentB`.

**Fix**: module-level `holderState = new Map()`. The Map is initialised once per
resource ID (first time seen, seed from agentA). Subsequent calls to
`initializeWorkloadModule` read from the Map instead of resetting.

### Problem 2: MVCC Cascade from Failed Holder Update

**Symptom**: After an MVCC failure at high TPS, subsequent transactions fail
C_auth even though MVCC is rare.

**Root cause**: the original implementation advanced `this.holders[idx]` after
submitting, regardless of whether the transaction succeeded. An MVCC failure
left `this.holders[idx]` pointing to the "wrong" agent.

**Fix**: conditional update — `holderState.set(...)` only inside the `try` block,
after the `await` succeeds. In the `catch` block, holder state is unchanged.

### Problem 3: MVCC at High TPS with Small Pool

**Symptom**: at 100 TPS, 205 Succ / 295 Fail (59% failure rate) with `poolSize = 20`.

**Root cause**: with 20 resources and 50 TPS/worker, each resource is targeted
at 2.5 TPS. With avg latency 0.4 s, expected in-flight per resource = 1.0. When
a second in-flight tx tries to modify a resource that a first in-flight tx is
still modifying, MVCC rejects the second.

**Fix**: increase `poolSize` to 100. Expected in-flight per resource = 0.2.

### Problem 4: `__ADMIN_KEY_PATH__` Not Resolved

**Symptom**: Caliper fails with `path property __ADMIN_KEY_PATH__ does not
point to a file that exists`.

**Root cause**: `paper3-network.yaml` is a template with placeholder strings.
The crypto material paths must be substituted with actual filesystem paths.

**Fix**: use `paper3-network-resolved.yaml`, which contains the actual paths
to the crypto-config material generated by `generate.sh`.

### Problem 5: UTF-8 BOM in Shebang Line

**Symptom**: `setup-caliper.sh` fails with `bad interpreter: No such file
or directory` on the Azure VM.

**Root cause**: the file was created on Windows with a UTF-8 BOM
(`0xEF 0xBB 0xBF`) prepended. Linux interprets the BOM as part of the
shebang interpreter path: `#!/usr/bin/env bash` becomes `﻿#!/usr/bin/env bash`,
and Linux looks for a binary named `﻿/bin/bash` (with the BOM character) which
doesn't exist.

**Fix**: `sed -i '1s/^\xEF\xBB\xBF//' setup-caliper.sh` to strip the BOM.

### Problem 6: Constraint Orderer Init Failure (Peer Not Ready)

**Symptom**: constraint orderer container exits immediately after start with
error about peer connection failure.

**Root cause**: `docker-compose-constraint.yaml` brings up all containers
simultaneously. The orderer calls `GetAllResources` at startup via the peer
snapshot; the peer may not yet have committed genesis block.

**Fix**: add a dependency in `docker-compose-constraint.yaml` so the orderer
waits for the peer's health endpoint before starting, or add a short sleep in
`run-constraint.sh` before bringing up the constraint orderer.

---

## 13. Correctness Argument

The endogenous architecture's IVR = 0 guarantee is formalised as Theorem 1 in
the paper, building on Proposition 1. Here is an informal argument:

**Proposition 1 — Sequential Equivalence**: Processing a batch of n transactions
through `ProcessBatch` under the evolving speculative state is equivalent to
evaluating each transaction individually in sequence, against the running state.

This holds because:
- **C1** (Leader uniqueness): only one orderer node runs `ProcessBatch` at any
  given time (Raft leader guarantee).
- **C2** (Sequential cache access): `filterBatch` (= `BeginBatch + ProcessBatch`)
  is called exclusively from the block construction goroutine, which processes
  at most one batch at a time.
- **C3** (Atomic commit): `Commit` is called synchronously from `writeBlock`,
  immediately after the ledger write, within the same Raft apply goroutine.
  No other goroutine can observe the new ledger state before the cache update.

**Theorem 1 — IVR = 0**: Under M_D_endo, no committed Transfer transaction can
produce |Holders| > K_max.

Proof sketch:
- Before any block is committed, `s_stable` correctly reflects the ledger state
  (established by `Init`).
- `BeginBatch` copies `s_stable` to `s_spec` at the start of each block.
- For each envelope: if `Evaluate(s_spec, tx) = false`, the envelope is dropped
  and never committed. If `true`, `s_spec ← Apply(s_spec, tx)` advances the
  speculative state.
- `Apply` is correct by construction of `BioankEvaluator.Apply` — it adds the
  new holder and removes the old one with correct group-membership tracking.
- After block commit, `s_stable ← s_spec` (the speculative state, which reflects
  all admitted transactions).
- By induction: if `s_stable` is correct before block N, then `s_spec` is correct
  after processing block N's batch, and `s_stable` is correct after committing.
- No transaction with `|Holders| + 1 > K_max` can ever be admitted (Evaluate
  rejects it). QED.

---

## 14. Performance Model

### Endogenous overhead model

Let:
- `T_block` = average block construction time (batch timeout + ordering + delivery)
- `N_env` = average envelopes per block
- `T_eval` = per-envelope evaluation time (O(1) for BioankEvaluator)

The constraint evaluation adds `N_env × T_eval` per block to the block
construction goroutine's cycle time.

With `T_block ≈ 500 ms` (batch timeout), `N_env ≈ 10` (max batch size),
`T_eval ≈ 1 µs` (map lookup + comparison):
```
overhead = 10 × 1 µs = 10 µs per 500 ms block = 0.002%
```

This explains why the measured overhead is < 1%: the evaluation is negligible
compared to the network round-trips (Raft consensus, peer delivery).

### Exogenous overhead model

Let:
- `T_fabric` = Fabric commit latency (endorsement + ordering + delivery)
- `RTT_coord` = coordinator round-trip time
- `T_mutex` = time spent waiting for coordinator mutex

Total per-transaction latency under M_D_exo:
```
T_total = RTT_coord(reserve) + T_fabric + RTT_coord(confirm) + T_mutex
```

For co-located coordinator: `RTT_coord ≈ 5 ms`, `T_mutex ≈ 0` (low load).
For 200 TPS with 2 workers: `T_mutex` grows as the mutex becomes saturated.

**Coordinator throughput limit**: the coordinator can process at most
`1 / (T_lock + T_reserve_computation)` Reserve calls per second per mutex.
With `T_lock + T_compute ≈ 10 µs` per call, the theoretical maximum is
100,000 calls/second. However, Go's mutex has fairness overhead under
contention that limits practical throughput to ~10,000–20,000 calls/second.
At 400 calls/second (200 TPS × 2 per tx), the coordinator is not saturated;
the bottleneck is the goroutine scheduling overhead and HTTP parsing.

The measured −10.5% at 200 TPS is therefore not mutex saturation per se, but
the accumulated goroutine blocking time across all Caliper workers waiting for
the coordinator to respond.

### MVCC collision probability model

Let:
- `λ` = transaction submission rate per resource (TPS/poolSize per worker)
- `τ` = average transaction latency (seconds)
- `W` = number of workers

Expected in-flight transactions per resource: `W × λ × τ`

Probability of two transactions colliding on the same resource:
```
P_mvcc ≈ (W × λ × τ)² / 2
```

With W=2, λ=50/100=0.5 TPS/resource, τ=0.4 s:
```
P_mvcc ≈ (2 × 0.5 × 0.4)² / 2 = (0.4)² / 2 = 0.08
```

That is, ~8% of transactions would encounter MVCC with `poolSize=100` at 100 TPS.
The measured result (0 failures) suggests the real collision probability is lower,
likely because transactions are spread across resources by the round-robin pool
access pattern.

---

## 15. Data Flow Diagrams

### M_D_endo — Transaction Flow

```
Client (Caliper)
    │
    ├──[1] Propose to peer (endorse)
    │        Peer simulates chaincode:
    │        - C_auth: valid? ✓
    │        - C_excl: valid? ✓
    │        - C_global: NOT checked (chaincode can't see concurrent state)
    │        Returns signed read-write set
    │
    ├──[2] Submit to orderer (Raft leader)
    │        filterBatch:
    │          BeginBatch → s_spec ← s_stable.Clone()
    │          for each envelope:
    │            tx ← TxParser.Parse(envelope)
    │            Evaluate(s_spec, tx):
    │              - Is toAgent already in Holders? → admit
    │              - |Holders| + 1 ≤ K_max? → admit
    │              - else → DROP (never committed)
    │            if admitted: s_spec ← Apply(s_spec, tx)
    │          Returns filtered batch (admitted envelopes only)
    │
    │        Raft proposes filtered block to followers
    │        Followers vote → quorum → Commit
    │          writeBlock → Commit → s_stable ← s_spec
    │
    ├──[3] Block delivered to peers
    │        Peer validates (MVCC, policy)
    │        Commits to ledger
    │
    └──[4] Caliper receives tx event (success/fail)
```

### M_D_exo — Transaction Flow

```
Client (Caliper)
    │
    ├──[1] POST /v1/reserve → Coordinator
    │        mutex.Lock()
    │        compute currentHolderCount()
    │        if count + 1 ≤ K_max:
    │          reservations[uuid] = {agent, timestamp}
    │          return {admitted: true, reservationId: uuid}
    │        else:
    │          return {admitted: false, reason: "quota_exceeded"}
    │        mutex.Unlock()
    │
    ├──[2] Propose to peer (endorse) — same as M_D_endo
    │
    ├──[3] Submit to orderer (stock, no constraint evaluation)
    │        Block committed normally
    │
    ├──[4] On SUCCESS: POST /v1/confirm → Coordinator
    │        mutex.Lock()
    │        remove reservation → add to holders
    │        mutex.Unlock()
    │
    └──[4'] On FAILURE: POST /v1/cancel → Coordinator
             mutex.Lock()
             remove reservation (slot released)
             mutex.Unlock()
```

### Init Sequence — M_D_endo Orderer Startup

```
HandleChain (orderer startup for constrained channel)
    │
    ├──[1] peersnapshot.New(config)
    │        Dial peer at FABRIC_CONSTRAINT_PEER_ADDR (mutual TLS)
    │        Returns PeerEndorserSnapshot (holds gRPC connection)
    │
    ├──[2] biobank.NewBioankEvaluator()
    │        Returns empty BioankEvaluator
    │
    ├──[3] constraint.NewStateManager(evaluator, snapshot)
    │        Calls evaluator.Init(snapshot):
    │          snapshot.GetByRange("directed","","")
    │            → Build ChaincodeInvocationSpec{GetAllResources}
    │            → Sign with reader identity (SHA-256+ECDSA)
    │            → ProcessProposal to peer
    │            → Parse JSON array response
    │          Build resources map + groups map from response
    │          s_stable = BioankCacheState{resources, groups}
    │          s_spec = s_stable.Clone()
    │        Returns StateManager
    │
    └──[4] chain.EnableConstraintAwareOrdering(mgr)
             c.constraintMgr = mgr
             (all four hooks now active)
```
