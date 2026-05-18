#!/usr/bin/env bash
# generate.sh — generate crypto material and channel artifacts for Paper 3.
# Must be run from the evaluation/paper3/network/ directory.
# Requires: cryptogen and configtxgen on PATH (installed by bootstrap-azure.sh).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo "=== Cleaning previous artifacts ==="
rm -rf crypto-config channel-artifacts
mkdir -p channel-artifacts

echo "=== Generating crypto material (cryptogen) ==="
cryptogen generate --config=crypto-config.yaml --output=crypto-config
echo "  → crypto-config/ created"

echo "=== Generating channel genesis block (Fabric 2.5+ — no system channel) ==="
export FABRIC_CFG_PATH="$SCRIPT_DIR"
configtxgen \
    -profile DirectedTraceabilityChannel \
    -channelID paper3channel \
    -outputBlock channel-artifacts/paper3channel.block
echo "  → channel-artifacts/paper3channel.block"

echo ""
echo "=== Artifacts summary ==="
find channel-artifacts crypto-config -maxdepth 2 -name "*.block" \
    | sort | sed 's/^/  /'
echo ""
echo "Done. Run 'docker compose -f docker-compose-base.yaml up -d' next."
