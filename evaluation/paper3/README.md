# Paper 3 — Evaluation Setup

Caliper-based benchmark comparing three architectures for directed-traceability enforcement:

| Label | Architecture | IVR |
|-------|-------------|-----|
| `fabric-std` | Fabric standard (M_L) — chaincode-local validation only | > 0 |
| `zk-exogenous` | ZooKeeper + Fabric (M_D exogenous) — pre-submission coordinator | 0† |
| `orderer-endogenous` | Constraint-aware orderer (M_D endogenous) — single Raft round | 0 |

† IVR = 0 only when every client faithfully calls Confirm/Cancel; client crash breaks the guarantee.

## Directory layout

```
evaluation/paper3/
├── network/
│   ├── crypto-config.yaml          # Cryptogen config (1 org, 3 orderers, 2 peers)
│   ├── configtx.yaml               # Channel config
│   ├── docker-compose-base.yaml    # Fabric network (shared by all 3 runs)
│   └── docker-compose-zk.yaml     # ZK coordinator sidecar (exogenous run only)
├── caliper/
│   ├── benchmarks/
│   │   ├── fabric-std.yaml         # Caliper benchmark config — M_L baseline
│   │   ├── zk-exogenous.yaml       # Caliper benchmark config — ZK baseline
│   │   └── orderer-endogenous.yaml # Caliper benchmark config — endogenous M_D
│   ├── workload/
│   │   ├── register.js             # Workload: register resources (common)
│   │   ├── transfer-std.js         # Workload: transfer without pre-check (M_L)
│   │   ├── transfer-zk.js          # Workload: transfer with ZK Reserve/Confirm (ZK)
│   │   └── transfer-endogenous.js  # Workload: transfer direct (orderer enforces)
│   └── network.yaml                # Caliper network adapter config (Fabric)
└── scripts/
    ├── run-all.sh                  # Run all 3 benchmarks sequentially
    └── plot-results.py             # Generate comparison figures for paper
```

## Running

```bash
# 1. Generate crypto material and channel artifacts
cd network && ./generate.sh

# 2. Start Fabric network
docker compose -f network/docker-compose-base.yaml up -d

# 3. Run all three benchmark variants
./scripts/run-all.sh

# 4. Plot results
python3 scripts/plot-results.py results/
```

## Metrics collected

- **Throughput** (tx/s) — send rate vs. effective committed rate
- **Latency** (ms) — p50, p95, p99 end-to-end
- **Intra-block rejection rate** — fraction of ordered txs rejected by evaluator (endogenous only)
- **IVR** — fraction of committed txs that violated C_global at admission time (post-hoc ledger audit)
- **Coordinator overhead** — extra RTT cost of Reserve/Confirm round-trip (ZK only)
