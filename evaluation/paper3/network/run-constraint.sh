#!/usr/bin/env bash
# run-constraint.sh — start the constraint-aware orderers (M_D path, Paper 3).
#
# Discovers the Admin@org1 keystore key, creates a fixed-name symlink so the
# docker-compose-constraint.yaml volume mount works, then starts all three
# orderers with the constraint overlay.
#
# Usage:
#   cd ~/fabric/evaluation/paper3/network
#   bash run-constraint.sh
#
# To stop and restore standard orderers:
#   bash run-constraint.sh stop
#   docker compose -f docker-compose-base.yaml up -d \
#     orderer0.example.com orderer1.example.com orderer2.example.com

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

CRYPTO="./crypto-config/peerOrganizations/org1.example.com"
KEYSTORE="${CRYPTO}/users/Admin@org1.example.com/msp/keystore"
SYMLINK="./crypto-config/constraint-reader-key.pem"

if [ "${1:-start}" = "stop" ]; then
    echo "Stopping constraint orderers and restoring standard image..."
    docker compose -f docker-compose-base.yaml up -d \
        orderer0.example.com orderer1.example.com orderer2.example.com
    echo "Standard orderers restarted."
    exit 0
fi

# ── Discover keystore key ───────────────────────────────────────────────────
KEY_FILE=$(ls "${KEYSTORE}"/*.pem 2>/dev/null | head -1 || \
           ls "${KEYSTORE}"/*_sk  2>/dev/null | head -1 || true)
if [ -z "$KEY_FILE" ]; then
    echo "ERROR: no key file found in ${KEYSTORE}" >&2
    exit 1
fi
echo "Using Admin key: ${KEY_FILE}"

# ── Create / refresh symlink with a fixed name ──────────────────────────────
ln -sf "$(realpath "${KEY_FILE}")" "${SYMLINK}"
echo "Symlink created: ${SYMLINK} → ${KEY_FILE}"

# ── Start constraint orderers ───────────────────────────────────────────────
echo "Starting constraint-aware orderers (M_D, paper3channel)..."
FABRIC_IMAGE=fabric-orderer-constraint FABRIC_TAG=directed-traceability \
  docker compose \
    -f docker-compose-base.yaml \
    -f docker-compose-constraint.yaml \
    up -d \
    orderer0.example.com orderer1.example.com orderer2.example.com

sleep 8

echo ""
echo "Container status:"
docker ps --format "{{.Names}}\t{{.Image}}\t{{.Status}}" | grep orderer

echo ""
echo "Constraint log (orderer0, last 5 lines):"
docker logs network-orderer0.example.com-1 2>&1 | grep -E "Constraint|WARN|ERROR|leader|elected" | tail -5 || true
