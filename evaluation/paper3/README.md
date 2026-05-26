# Paper 3 — Constraint-Aware Ordering for Directed Traceability in Hyperledger Fabric

**ÉTS PhD research / ACM Distributed Ledger Technologies, Autumn 2026**

This repository contains the full evaluation artefacts for Paper 3, which introduces
and evaluates *constraint-aware ordering* as a mechanism for enforcing global
concurrency constraints in a Hyperledger Fabric permissioned blockchain without
modifying the peer or the endorsement pipeline.

---

## Table of Contents

1. [Research Context](#1-research-context)
2. [The Three Architectures](#2-the-three-architectures)
3. [Repository Layout](#3-repository-layout)
4. [Prerequisites](#4-prerequisites)
5. [Network Setup](#5-network-setup)
6. [Running the Benchmarks](#6-running-the-benchmarks)
   - [Scenario 1 — Violation Rate](#scenario-1--violation-rate)
   - [Scenario 2 — Performance Overhead (Endogenous)](#scenario-2--performance-overhead-endogenous)
   - [Scenario 3 — Exogenous Overhead (ZK Coordinator)](#scenario-3--exogenous-overhead-zk-coordinator)
7. [Analysing Results](#7-analysing-results)
8. [Key Results Summary](#8-key-results-summary)
9. [Troubleshooting](#9-troubleshooting)

---

## 1. Research Context

A *directed-traceability* system tracks the custody chain of a resource (e.g.,
a biobank specimen, a dataset, a legal title) as it moves between agents.
Governance rules attached to each resource impose usage constraints:

| Constraint | Scope | Enforcer |
|---|---|---|
| `C_auth` | Local — proposing agent must be current holder | Chaincode (endorsement) |
| `C_excl` | Local — at most one holder per resource | Chaincode (endorsement) |
| `C_global` | **Global** — at most K_max distinct agents hold resources in the same SubsetGroup simultaneously | **Orderer / external coordinator** |

`C_global` cannot be enforced by the chaincode alone: two concurrent `Transfer`
proposals are each endorsed against the same stale world state, both pass the
`|S_sub| + 1 <= K_max` check independently, and both are committed — producing
an *intra-block violation* (IVR > 0).

Paper 3 proposes embedding `C_global` evaluation inside the Raft ordering loop
(*endogenous* enforcement), proves that IVR = 0 holds under three conditions
(leader uniqueness, sequential cache access, atomic commit), and benchmarks the
approach against a stock Fabric baseline and a ZooKeeper coordinator baseline.

---

## 2. The Three Architectures

| Label | Code name | Description | IVR |
|---|---|---|---|
| **M_L** | `fabric-std` | Stock Hyperledger Fabric. No global constraint. Chaincode enforces C_auth and C_excl only. | > 0 |
| **M_D_exo** | `zk-exogenous` | Standard Fabric + external HTTP coordinator. Clients call `Reserve` before endorsing, `Confirm` or `Cancel` after commit. Coordinator enforces C_global atomically. | 0† |
| **M_D_endo** | `orderer-endogenous` | Custom Raft orderer with an embedded `BioankEvaluator`. The orderer filters each block's envelope batch against a stable/speculative cache state. | 0 |

> † IVR = 0 only when every client faithfully calls `Confirm`/`Cancel`.
> A client crash between `Reserve` and `Cancel` leaks a reservation slot until
> the TTL-based expiry goroutine reclaims it (default: 30 s).

---

## 3. Repository Layout

```
evaluation/paper3/
│
├── README.md                         ← this file
├── CHANGELOG.md                      ← change history
├── DESIGN.md                         ← detailed design & code walkthrough
│
├── paper_section_implementation.tex  ← §IV LaTeX (implementation)
├── paper_section_evaluation.tex      ← §V LaTeX (evaluation results)
│
├── network/
│   ├── crypto-config.yaml            ← cryptogen: 1 orderer org (3 nodes), 2 peer orgs
│   ├── configtx.yaml                 ← channel genesis + anchor peers
│   ├── docker-compose-base.yaml      ← Fabric network (all 3 scenarios share this)
│   ├── docker-compose-constraint.yaml← swap orderer image → constraint-aware orderer
│   ├── docker-compose-zk.yaml        ← add zkcoordinator sidecar (Sc3 only)
│   ├── generate.sh                   ← crypto + channel artefact generation
│   ├── run-constraint.sh             ← start/stop/status for constraint orderer swap
│   ├── orderer.Dockerfile            ← multi-stage build for constraint orderer image
│   └── env.sh                        ← exports PATH, FABRIC_CFG_PATH, etc.
│
├── caliper/
│   ├── networks/
│   │   ├── paper3-network.yaml       ← Caliper network adapter (template)
│   │   └── paper3-network-resolved.yaml ← resolved version (actual key paths)
│   │
│   ├── benchmarks/
│   │   ├── violation-quota.yaml      ← Sc1: quota violation burst (K_max=2, 40 txs)
│   │   ├── perf-overhead.yaml        ← Sc2: M_L vs M_D_endo overhead (50/100/200 TPS)
│   │   └── perf-exogenous.yaml       ← Sc3: M_D_exo overhead (50/100/200 TPS)
│   │
│   ├── workloads/
│   │   ├── setup-violation.js        ← Round 1: register resource pools with SubsetGroup
│   │   ├── violation-quota.js        ← Round 2: burst Transfer to unique agents
│   │   ├── perf-transfer.js          ← Sc2 workload: ping-pong Transfer (M_L / M_D_endo)
│   │   └── perf-exogenous.js         ← Sc3 workload: ping-pong + Reserve/Confirm/Cancel
│   │
│   ├── results/
│   │   ├── fig1_violations.pdf/png   ← Sc1 bar chart (IVR)
│   │   ├── fig2_throughput.pdf/png   ← Sc2+3 throughput grouped bars
│   │   ├── fig3_latency.pdf/png      ← Sc2+3 avg/p99 latency
│   │   ├── fig4_overhead.pdf/png     ← Sc2+3 % overhead vs M_L
│   │   ├── perf-overhead-std-p03.log ← raw Caliper log (M_L run)
│   │   ├── perf-overhead-constraint-p04.log ← raw Caliper log (M_D_endo run)
│   │   └── perf-exogenous-z01.log   ← raw Caliper log (M_D_exo run)
│   │
│   ├── plot-results.py               ← generate all 4 figures from hardcoded data
│   ├── setup-caliper.sh              ← install Caliper + set NODE_PATH
│   └── measure-violations.sh         ← post-hoc IVR audit from ledger query
│
└── scripts/
    ├── bootstrap-azure.sh            ← provision fresh Azure VM from scratch
    ├── audit-ivr.js                  ← ledger IVR audit (Node.js, reads block files)
    └── run-all.sh                    ← run all three benchmark variants sequentially
```

The chaincode and orderer extension live at the **Fabric repo root**:

```
integration/chaincode/directed/       ← DirectedTraceability chaincode (Go)
orderer/consensus/etcdraft/
├── constraint/                       ← generic evaluator framework (Go)
│   ├── evaluator.go                  ← Evaluator interface + TxView
│   ├── cache.go                      ← StateManager (stable/speculative pair)
│   ├── cache_test.go                 ← unit tests
│   ├── biobank/evaluator.go          ← BioankEvaluator (C_global for directed traceability)
│   └── peersnapshot/snapshot.go      ← peer-endorsed world-state snapshot at Init time
tools/zkcoordinator/
├── coordinator.go                    ← Reserve/Confirm/Cancel/Release logic
├── main.go                           ← HTTP server (port 8080)
├── Dockerfile                        ← multi-stage build
└── go.mod
```

---

## 4. Prerequisites

### On the Azure VM (where Fabric runs)

| Tool | Version | Notes |
|---|---|---|
| Docker | ≥ 24.0 | `docker compose` v2 plugin required |
| Go | 1.24.0 | needed to build the constraint orderer |
| Node.js | 18 LTS | for Caliper and workload scripts |
| npm | ≥ 9 | |
| Python 3 | ≥ 3.10 | for `plot-results.py` (+ matplotlib, numpy) |
| Hyperledger Fabric binaries | 2.5 | `peer`, `orderer`, `configtxgen`, `cryptogen` |
| Caliper CLI | 0.6 | installed locally via `setup-caliper.sh` |

### SSH access

```powershell
# From your local machine (Windows PowerShell or WSL)
ssh -i C:\Users\SISIO1\.ssh\directed-traceability-vm_key.pem azureuser@40.86.227.109
```

---

## 5. Network Setup

### 5a. First-time provisioning (Azure VM from scratch)

```bash
# On the VM
bash ~/fabric/evaluation/paper3/scripts/bootstrap-azure.sh
```

This script installs Docker, Go, Node 18, Fabric binaries, and clones the repo.

### 5b. Generate cryptographic material and channel artefacts

```bash
cd ~/fabric/evaluation/paper3/network
source env.sh
bash generate.sh
```

Output: `crypto-config/`, `channel-artefacts/genesis.block`, `channel-artefacts/mychannel.tx`.

### 5c. Start the Fabric network (M_L / stock orderers)

```bash
cd ~/fabric/evaluation/paper3/network
docker compose -f docker-compose-base.yaml up -d
```

Wait ~10 s, then verify all containers are running:

```bash
docker ps --format "table {{.Names}}\t{{.Status}}"
```

Expected containers: `orderer0.example.com`, `orderer1.example.com`, `orderer2.example.com`,
`peer0.org1.example.com`, `peer0.org2.example.com`, `cli`.

### 5d. Deploy the chaincode

```bash
# From inside the CLI container, or using the peer binary:
docker exec cli bash /scripts/deploy-chaincode.sh
```

The chaincode (`directed-traceability`) must be committed on `mychannel` with
endorsement policy `1-of-2` before any benchmark can run.

### 5e. Install Caliper

```bash
cd ~/fabric/evaluation/paper3/caliper
bash setup-caliper.sh
source ~/fabric/evaluation/paper3/network/env.sh
export NODE_PATH=/home/azureuser/fabric/evaluation/paper3/caliper/node_modules
```

---

## 6. Running the Benchmarks

> **runTag rule**: increment the `runTag` field in both rounds of each YAML
> before each re-run. The chaincode uses resource IDs like `viol-r-w0-r04-1`;
> re-using a tag causes "resource already exists" errors.

### Scenario 1 — Violation Rate

**Purpose**: measure IVR under concurrent Transfer burst at K_max=2.

**Run twice** — first with stock orderers, then with the constraint orderer.

#### Run A — M_L baseline (stock orderers)

```bash
# Network must be running with stock orderers (default after docker-compose-base up)
cd ~/fabric/evaluation/paper3/caliper

npx caliper launch manager \
  --caliper-workspace . \
  --caliper-networkconfig networks/paper3-network-resolved.yaml \
  --caliper-benchconfig benchmarks/violation-quota.yaml \
  --caliper-flow-only-test \
  2>&1 | tee results/violation-std-r04.log
```

Record `Succ` and `Fail` from the `quota-violation` round (should be 40/0 for M_L).

#### Run B — M_D_endo (constraint orderer)

```bash
# Swap orderers
cd ~/fabric/evaluation/paper3/network
bash run-constraint.sh start

# Change runTag in violation-quota.yaml: r04 → r05
# Then re-run Caliper:
cd ~/fabric/evaluation/paper3/caliper
npx caliper launch manager \
  --caliper-workspace . \
  --caliper-networkconfig networks/paper3-network-resolved.yaml \
  --caliper-benchconfig benchmarks/violation-quota.yaml \
  --caliper-flow-only-test \
  2>&1 | tee results/violation-constraint-r05.log

# Restore stock orderers when done
bash ~/fabric/evaluation/paper3/network/run-constraint.sh stop
```

Expected: 2 Succ, 38 Fail/Timeout for the `quota-violation` round.

#### Post-hoc IVR audit

```bash
bash ~/fabric/evaluation/paper3/caliper/measure-violations.sh r04 2
```

---

### Scenario 2 — Performance Overhead (Endogenous)

**Purpose**: compare M_L vs M_D_endo at 50/100/200 TPS, no quota pressure (K_max=999).

#### Run A — M_L (stock orderers, runTag p03)

```bash
cd ~/fabric/evaluation/paper3/network
bash run-constraint.sh stop    # ensure stock orderers

cd ~/fabric/evaluation/paper3/caliper
# Verify runTag: p03 in perf-overhead.yaml (both rounds)
npx caliper launch manager \
  --caliper-workspace . \
  --caliper-networkconfig networks/paper3-network-resolved.yaml \
  --caliper-benchconfig benchmarks/perf-overhead.yaml \
  --caliper-flow-only-test \
  2>&1 | tee results/perf-overhead-std-p03.log
```

#### Run B — M_D_endo (constraint orderer, runTag p04)

```bash
cd ~/fabric/evaluation/paper3/network
bash run-constraint.sh start

cd ~/fabric/evaluation/paper3/caliper
# Change runTag: p03 → p04 in perf-overhead.yaml (both rounds)
npx caliper launch manager \
  --caliper-workspace . \
  --caliper-networkconfig networks/paper3-network-resolved.yaml \
  --caliper-benchconfig benchmarks/perf-overhead.yaml \
  --caliper-flow-only-test \
  2>&1 | tee results/perf-overhead-constraint-p04.log

bash ~/fabric/evaluation/paper3/network/run-constraint.sh stop
```

---

### Scenario 3 — Exogenous Overhead (ZK Coordinator)

**Purpose**: compare M_D_exo (ZK coordinator) vs M_L at same TPS tiers.

```bash
# Stock orderers + ZK coordinator sidecar
cd ~/fabric/evaluation/paper3/network
bash run-constraint.sh stop
docker compose -f docker-compose-base.yaml -f docker-compose-zk.yaml up -d zkcoordinator

cd ~/fabric/evaluation/paper3/caliper
# Verify runTag: z01 in perf-exogenous.yaml (both rounds)
npx caliper launch manager \
  --caliper-workspace . \
  --caliper-networkconfig networks/paper3-network-resolved.yaml \
  --caliper-benchconfig benchmarks/perf-exogenous.yaml \
  --caliper-flow-only-test \
  2>&1 | tee results/perf-exogenous-z01.log

# Tear down coordinator when done
docker compose -f docker-compose-base.yaml -f docker-compose-zk.yaml stop zkcoordinator
```

---

## 7. Analysing Results

### Generate figures

```bash
# On the VM (needs matplotlib + numpy)
cd ~/fabric/evaluation/paper3/caliper
python3 -m venv /tmp/plot-venv --system-site-packages
/tmp/plot-venv/bin/pip install matplotlib numpy
/tmp/plot-venv/bin/python3 plot-results.py --outdir results
```

Figures are written to `caliper/results/`:

| File | Content |
|---|---|
| `fig1_violations.pdf` | Sc1 — IVR bar chart (M_L vs M_D_endo) |
| `fig2_throughput.pdf` | Sc2+3 — effective TPS at 50/100/200 TPS |
| `fig3_latency.pdf` | Sc2+3 — avg/p99 latency |
| `fig4_overhead.pdf` | Sc2+3 — % overhead relative to M_L |

### Copy figures to local machine (Windows PowerShell)

```powershell
$VM = "azureuser@40.86.227.109"
$KEY = "C:\Users\SISIO1\.ssh\directed-traceability-vm_key.pem"
$DST = "D:\Workspace\BlockchainApps\fabric\evaluation\paper3\caliper\results\"

foreach ($f in @("fig1_violations.pdf","fig2_throughput.pdf","fig3_latency.pdf","fig4_overhead.pdf")) {
    scp -i $KEY "${VM}:~/fabric/evaluation/paper3/caliper/results/$f" $DST
}
```

---

## 8. Key Results Summary

All results from Azure Standard_D4s_v3 (4 vCPU, 16 GB), Fabric v2.5,
Caliper 0.6, 2 workers, May 2026.

### Scenario 1 — Violation Rate (K_max=2, 40 Transfer attempts)

| Architecture | Succ | Fail/Timeout | IVR |
|---|---|---|---|
| M_L (fabric-std) | 40 | 0 | **0.90** (36 violations) |
| M_D_endo (constraint orderer) | 2 | 38 | **0.00** |

### Scenario 2 — Performance Overhead (500 txs per tier, K_max=999)

| Architecture | 50 TPS eff. | 100 TPS eff. | 200 TPS eff. | Max Δ vs M_L |
|---|---|---|---|---|
| M_L | 47.7 | 95.6 | 134.8 | baseline |
| M_D_endo | 47.8 | 95.1 | 134.0 | **< 1%** |
| M_D_exo | 47.8 | 95.2 | 120.6 | **-10.5%** at 200 TPS |

### Scenario 3 — Exogenous coordinator bottleneck

At 200 TPS the coordinator mutex serialises `Reserve` calls, capping throughput
at 120.6 TPS (-10.5% vs M_L). At 50 and 100 TPS the overhead is negligible
(< 0.5%). A WAN coordinator (RTT 20 ms) would add 40 ms/tx additional latency.

---

## 9. Troubleshooting

### "resource already exists" error in setup round

Increment `runTag` in **both** rounds of the YAML (setup + transfer/violation).

### `__ADMIN_KEY_PATH__ does not point to a file`

Use `networks/paper3-network-resolved.yaml` instead of `paper3-network.yaml`.
The resolved file has actual filesystem paths instead of template placeholders.

### MVCC failures at high TPS with small pool

Increase `poolSize` in the benchmark YAML. At 100 TPS (50/worker) and 0.4 s
avg latency, ~20 txs are in-flight per worker. With `poolSize=20`, expected
in-flight per resource = 1.0 → frequent MVCC. With `poolSize=100`, expected
in-flight = 0.2 → negligible MVCC.

### C_auth failures on rounds 2+ (transfer-100, transfer-200)

The workload modules use a **module-level `holderState` Map** that persists
across Caliper rounds within the same worker process. If you are using an old
workload file that re-seeds `holderState` from `agentA` every round, holder
state drifts and produces C_auth failures. Use `perf-transfer.js` (which uses
the module-level Map pattern).

### Constraint orderer fails to start

Check that the six `FABRIC_CONSTRAINT_*` environment variables are set in
`docker-compose-constraint.yaml` and that the peer is reachable from the orderer
container before orderer startup. The orderer calls `GetAllResources` on the peer
at startup; if the peer is not ready, `HandleChain` returns an error.

### `externally-managed-environment` when installing matplotlib

Use a virtual environment:
```bash
python3 -m venv /tmp/plot-venv --system-site-packages
/tmp/plot-venv/bin/pip install matplotlib numpy
/tmp/plot-venv/bin/python3 plot-results.py
```
