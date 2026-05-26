#!/usr/bin/env bash
# start.sh — relance l'environnement paper3 après un reboot VM.
#
# Usage:
#   cd ~/fabric/evaluation/paper3/network
#   bash start.sh
#
# Ce script :
#   1. Source les variables d'environnement (PATH, FABRIC_CFG_PATH, NODE_PATH)
#   2. Vérifie que Docker tourne
#   3. Relance les containers Fabric si nécessaire (les volumes/ledger sont intacts)
#   4. Attend que les peers et orderers soient prêts
#   5. Affiche l'état final

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PAPER3_DIR="$(dirname "$SCRIPT_DIR")"

# ── 1. Variables d'environnement ───────────────────────────────────────────────
source "$SCRIPT_DIR/env.sh"
export NODE_PATH="$PAPER3_DIR/caliper/node_modules"
echo "[start.sh] Environnement chargé (NODE_PATH=$NODE_PATH)"

# ── 2. Docker sanity check ─────────────────────────────────────────────────────
if ! docker info &>/dev/null; then
    echo "[start.sh] ERREUR: Docker ne répond pas. Vérifier: systemctl status docker"
    exit 1
fi
echo "[start.sh] Docker OK"

# ── 3. Relance des containers ──────────────────────────────────────────────────
cd "$SCRIPT_DIR"

RUNNING=$(docker compose -f docker-compose-base.yaml ps --services --status running 2>/dev/null | wc -l)
TOTAL=6  # orderer0,1,2 + peer0,1 + prometheus

if [ "$RUNNING" -ge "$TOTAL" ]; then
    echo "[start.sh] Containers déjà en cours ($RUNNING/$TOTAL) — rien à faire"
else
    echo "[start.sh] Relance des containers ($RUNNING/$TOTAL actifs)..."
    docker compose -f docker-compose-base.yaml up -d
fi

# ── 4. Attendre que les peers soient prêts ─────────────────────────────────────
echo "[start.sh] Attente de la disponibilité des peers (max 30s)..."
for i in $(seq 1 30); do
    if docker exec peer0.org1.example.com peer node status &>/dev/null 2>&1; then
        echo "[start.sh] peer0.org1 prêt (${i}s)"
        break
    fi
    sleep 1
done

# ── 5. Résumé ──────────────────────────────────────────────────────────────────
echo ""
echo "=== État des containers paper3 ==="
docker compose -f docker-compose-base.yaml ps --format "table {{.Name}}\t{{.Status}}\t{{.Ports}}"
echo ""
echo "=== Prêt. Variables exportées dans ce shell ==="
echo "  NODE_PATH=$NODE_PATH"
echo "  FABRIC_CFG_PATH=${FABRIC_CFG_PATH:-non défini}"
echo ""
echo "Pour lancer un benchmark:"
echo "  cd $PAPER3_DIR/caliper"
echo "  npx caliper launch manager --caliper-workspace . \\"
echo "    --caliper-networkconfig networks/paper3-network-resolved.yaml \\"
echo "    --caliper-benchconfig benchmarks/perf-overhead.yaml \\"
echo "    --caliper-flow-only-test"
