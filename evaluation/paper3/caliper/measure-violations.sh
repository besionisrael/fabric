#!/usr/bin/env bash
# measure-violations.sh — Post-run C_global violation counter.
#
# After each violation-quota benchmark run, this script queries the ledger via
# ListByGroup for every group registered during the test and reports:
#   - |S_sub|: number of distinct agents currently holding a resource in the group
#   - violations: max(0, |S_sub| - K_MAX)
#
# Usage:
#   source ~/fabric/evaluation/paper3/network/env.sh
#   bash measure-violations.sh <runTag> [kMax]
#
# Arguments:
#   runTag  — the tag used in the benchmark YAML (e.g. r01, r02, …)
#             Group IDs are: viol-group-w0-<runTag>, viol-group-w1-<runTag>
#   kMax    — MaxConcurrent quota (default: 2)
#
# Example:
#   bash measure-violations.sh r01 2
#   bash measure-violations.sh r02 2

set -euo pipefail

CHAINCODE="directed-traceability"
CHANNEL="paper3channel"
K_MAX="${2:-2}"
RUN_TAG="${1:?Usage: bash measure-violations.sh <runTag> [kMax]}"

ORDERER_ENDPOINT="orderer0.example.com:7050"
ORDERER_CA="$HOME/fabric/evaluation/paper3/network/crypto-config/ordererOrganizations/example.com/orderers/orderer0.example.com/msp/tlscacerts/tlsca.example.com-cert.pem"

echo "=== C_global Violation Measurement ==="
echo "Channel:     $CHANNEL"
echo "Chaincode:   $CHAINCODE"
echo "K_MAX:       $K_MAX"
echo "Run tag:     $RUN_TAG"
echo "Groups:      viol-group-w0-${RUN_TAG}, viol-group-w1-${RUN_TAG}"
echo ""

total_violations=0
total_groups=0

# Workers 0 and 1 each have their own per-worker SubsetGroup.
for worker in 0 1; do
    GROUP_ID="viol-group-w${worker}-${RUN_TAG}"

    echo "--- Worker ${worker}: group = ${GROUP_ID} ---"

    # Query the ledger.
    RAW=$(peer chaincode query \
        -C "$CHANNEL" \
        -n "$CHAINCODE" \
        -c "{\"function\":\"ListByGroup\",\"Args\":[\"${GROUP_ID}\"]}" \
        2>/dev/null) || {
        echo "  [WARN] Query failed for group ${GROUP_ID} (may not exist yet)"
        continue
    }

    if [ -z "$RAW" ] || [ "$RAW" = "null" ] || [ "$RAW" = "[]" ]; then
        echo "  No resources found in this group."
        continue
    fi

    # Count distinct currentHolder values across active resources.
    HOLDER_COUNT=$(echo "$RAW" | python3 -c "
import json, sys
resources = json.load(sys.stdin)
if not resources:
    print(0)
    sys.exit(0)
holders = set(r['currentHolder'] for r in resources if r.get('status') == 'active')
print(len(holders))
" 2>/dev/null || echo "0")

    RESOURCE_COUNT=$(echo "$RAW" | python3 -c "
import json, sys
resources = json.load(sys.stdin)
print(len(resources) if resources else 0)
" 2>/dev/null || echo "0")

    VIOLATIONS=$(( HOLDER_COUNT > K_MAX ? HOLDER_COUNT - K_MAX : 0 ))

    echo "  Resources in group : $RESOURCE_COUNT"
    echo "  |S_sub| (holders)  : $HOLDER_COUNT"
    echo "  K_MAX              : $K_MAX"
    echo "  Violations         : $VIOLATIONS"

    if [ "$VIOLATIONS" -gt 0 ]; then
        echo "  *** C_global VIOLATED: |S_sub|=$HOLDER_COUNT > K_MAX=$K_MAX ***"
    else
        echo "  ✓  C_global satisfied: |S_sub|=$HOLDER_COUNT ≤ K_MAX=$K_MAX"
    fi
    echo ""

    total_violations=$(( total_violations + VIOLATIONS ))
    total_groups=$(( total_groups + 1 ))
done

echo "=== Summary ==="
echo "Groups measured : $total_groups"
echo "Total violations: $total_violations"
if [ "$total_violations" -gt 0 ]; then
    echo "RESULT: C_global VIOLATED in this run (fabric-std / zk-exogenous path)"
else
    echo "RESULT: C_global ENFORCED in this run (orderer-endogenous path)"
fi
