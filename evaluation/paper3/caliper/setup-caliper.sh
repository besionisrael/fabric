#!/usr/bin/env bash
# setup-caliper.sh — prepare Caliper workspace for Paper 3 experiments.
# Run once from evaluation/paper3/caliper/ before launching any benchmark.
# Usage: bash setup-caliper.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NETWORK_DIR="$SCRIPT_DIR/../network"
CRYPTO_DIR="$NETWORK_DIR/crypto-config"
ADMIN_KEYSTORE="$CRYPTO_DIR/peerOrganizations/org1.example.com/users/Admin@org1.example.com/msp/keystore"

echo "=== [1/3] Resolving Admin private key ==="
ADMIN_KEY_FILE=$(ls "$ADMIN_KEYSTORE"/*_sk 2>/dev/null | head -1)
if [[ -z "$ADMIN_KEY_FILE" ]]; then
    # Newer cryptogen names the key without _sk suffix
    ADMIN_KEY_FILE=$(ls "$ADMIN_KEYSTORE"/ | head -1)
    ADMIN_KEY_FILE="$ADMIN_KEYSTORE/$ADMIN_KEY_FILE"
fi
echo "  key: $ADMIN_KEY_FILE"

echo "=== [2/3] Generating paper3-network-resolved.yaml ==="
sed "s|__ADMIN_KEY_PATH__|${ADMIN_KEY_FILE}|g" \
    "$SCRIPT_DIR/networks/paper3-network.yaml" \
    > "$SCRIPT_DIR/networks/paper3-network-resolved.yaml"
echo "  -> networks/paper3-network-resolved.yaml"

echo "=== [3/3] Binding Caliper to Fabric SDK ==="
# caliper bind is idempotent — safe to re-run.
caliper bind --caliper-bind-sut fabric:fabric-gateway 2>&1 | tail -3 || \
caliper bind --caliper-bind-sut fabric:2.4 2>&1 | tail -3

echo ""
echo "Setup complete. Run a benchmark with:"
echo "  cd $SCRIPT_DIR"
echo "  caliper launch manager \\"
echo "    --caliper-workspace . \\"
echo "    --caliper-networkconfig networks/paper3-network-resolved.yaml \\"
echo "    --caliper-benchconfig benchmarks/fabric-std.yaml \\"
echo "    --caliper-flow-only-test"
