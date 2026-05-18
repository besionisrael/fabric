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

echo "=== Generating genesis block ==="
export FABRIC_CFG_PATH="$SCRIPT_DIR"
configtxgen \
    -profile ThreeOrgsOrdererGenesis \
    -channelID system-channel \
    -outputBlock channel-artifacts/genesis.block
echo "  → channel-artifacts/genesis.block"

echo "=== Generating channel transaction ==="
configtxgen \
    -profile TwoOrgsChannel \
    -channelID paper3channel \
    -outputCreateChannelTx channel-artifacts/paper3channel.tx
echo "  → channel-artifacts/paper3channel.tx"

echo "=== Generating anchor peer transactions ==="
configtxgen \
    -profile TwoOrgsChannel \
    -channelID paper3channel \
    -outputAnchorPeersUpdate channel-artifacts/Org1MSPanchors.tx \
    -asOrg Org1MSP
configtxgen \
    -profile TwoOrgsChannel \
    -channelID paper3channel \
    -outputAnchorPeersUpdate channel-artifacts/Org2MSPanchors.tx \
    -asOrg Org2MSP
echo "  → anchor peer transactions"

echo ""
echo "=== Artifacts summary ==="
find channel-artifacts crypto-config -maxdepth 2 -name "*.block" -o -name "*.tx" \
    | sort | sed 's/^/  /'
echo ""
echo "Done. Run 'docker compose -f docker-compose-base.yaml up -d' next."
