#!/usr/bin/env bash
# Run all three Paper 3 benchmark variants and collect results.
# Prerequisite: Fabric network is up (docker compose -f network/docker-compose-base.yaml up -d)
# and the directed chaincode has been deployed to paper3channel.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$SCRIPT_DIR/.."
CALIPER_DIR="$ROOT/caliper"
RESULTS_DIR="$ROOT/results"
mkdir -p "$RESULTS_DIR"

CALIPER_BIN="${CALIPER_BIN:-npx caliper}"
NETWORK_CONFIG="$CALIPER_DIR/network.yaml"

run_benchmark() {
    local label="$1"
    local benchmark="$2"
    local extra_compose="${3:-}"

    echo "======================================================="
    echo " Running benchmark: $label"
    echo "======================================================="

    if [[ -n "$extra_compose" ]]; then
        docker compose -f "$ROOT/network/docker-compose-base.yaml" \
                       -f "$ROOT/network/$extra_compose" up -d zkcoordinator
    fi

    $CALIPER_BIN launch manager \
        --caliper-workspace "$CALIPER_DIR" \
        --caliper-networkconfig "$NETWORK_CONFIG" \
        --caliper-benchconfig "benchmarks/${benchmark}" \
        --caliper-flow-only-test \
        --caliper-report-path "$RESULTS_DIR/${label}-report.html" \
        2>&1 | tee "$RESULTS_DIR/${label}.log"

    if [[ -n "$extra_compose" ]]; then
        docker compose -f "$ROOT/network/docker-compose-base.yaml" \
                       -f "$ROOT/network/$extra_compose" stop zkcoordinator
    fi

    echo "Results saved to $RESULTS_DIR/${label}-report.html"
}

# ── 1. Fabric standard (M_L) ─────────────────────────────────────────────────
run_benchmark "fabric-std" "fabric-std.yaml"

# ── 2. ZooKeeper exogenous (M_D exo) ─────────────────────────────────────────
run_benchmark "zk-exogenous" "zk-exogenous.yaml" "docker-compose-zk.yaml"

# ── 3. Constraint-aware orderer (M_D endo) ────────────────────────────────────
# Requires the orderer image built with the constraint-aware patch (Paper 3).
# Set FABRIC_IMAGE=paper3/fabric-orderer FABRIC_TAG=latest before running.
run_benchmark "orderer-endogenous" "orderer-endogenous.yaml"

echo ""
echo "All benchmarks complete. Generate figures with:"
echo "  python3 scripts/plot-results.py $RESULTS_DIR"
