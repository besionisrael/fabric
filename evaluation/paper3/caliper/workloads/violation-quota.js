// violation-quota.js — Caliper workload: C_global quota violation test.
//
// Demonstrates the gap between M_L (fabric-std) and M_D (orderer-endogenous):
//
//   M_L cannot reliably enforce C_global because concurrent endorsements both
//   read the same group state, both pass local checks, and both commit —
//   resulting in |S_sub| > K_max (Paper 2, Theorem 2).
//
//   M_D enforces C_global at the orderer via ProcessBatch: transactions that
//   would exceed MaxConcurrent are dropped from the block before commit
//   (Paper 3, Theorem 1).
//
// Setup (initializeWorkloadModule):
//   - Register POOL_SIZE resources in a per-worker SubsetGroup with MaxConcurrent = K_MAX.
//   - Each resource's initial holder is init-agent-<workerIndex>.
//   - On start, GroupState.Holders = { init-agent-<w> }, len = 1.
//
// Test (submitTransaction):
//   - Transfer resource[i % pool] from init-agent-<w> to a UNIQUE new agent.
//   - Every transfer introduces a fresh agent → every success increments |S_sub|.
//   - With K_MAX = 2 only ONE additional holder is admitted:
//       fabric-std:         all POOL_SIZE transfers succeed → |S_sub| >> K_MAX  (VIOLATIONS)
//       orderer-endogenous: 1 transfer succeeds, rest timeout → |S_sub| = K_MAX (ENFORCED)
//
// Timeout is set short (8 s) so that orderer-dropped transactions fail quickly
// rather than waiting for the full 30 s default.  Caliper counts them as Fail.
//
// Post-run measurement (run on VM after each variant):
//   peer chaincode query -C paper3channel -n directed-traceability \
//     -c '{"function":"ListByGroup","Args":["<groupId>"]}' \
//     | python3 -c "import json,sys; r=json.load(sys.stdin); \
//       holders=set(x['currentHolder'] for x in r if r); \
//       print(f'|S_sub|={len(holders)}, violations={max(0,len(holders)-K_MAX)}')"
//
// Arguments (from benchmark YAML):
//   chaincodeId  — chaincode name (default: directed-traceability)
//   prefix       — resource ID prefix (default: vq)
//   poolSize     — resources per worker (default: 20)
//   kMax         — MaxConcurrent quota (default: 2)

'use strict';

const { WorkloadModuleBase } = require('@hyperledger/caliper-core');

class ViolationQuotaWorkload extends WorkloadModuleBase {
    constructor() {
        super();
        this.txIndex     = 0;
        this.pool        = [];     // resource IDs
        this.groupId     = '';     // SubsetGroup for this worker's pool
        this.initAgent   = '';     // initial holder of all pool resources
        this.chaincodeId = 'directed-traceability';
        this.prefix      = 'vq';
        this.kMax        = 2;
    }

    async initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext) {
        await super.initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext);

        this.chaincodeId = roundArguments.chaincodeId || 'directed-traceability';
        this.prefix      = roundArguments.prefix      || 'vq';
        this.kMax        = roundArguments.kMax        || 2;
        this.workerIndex = workerIndex;
        this.runId       = Date.now();

        const poolSize   = roundArguments.poolSize || 20;

        // Per-worker group: isolated so workers don't interfere via shared state.
        this.groupId   = `${this.prefix}-group-w${workerIndex}-${this.runId}`;
        this.initAgent = `init-agent-${workerIndex}`;

        // Conditions: MaxConcurrent = kMax enforced by orderer; NOT by chaincode.
        // AllowedActions includes 'transfer' so C_conditions is satisfied.
        const conditions = JSON.stringify({
            allowedActions:  ['research', 'transfer'],
            allowedPurposes: ['oncology', 'genomics'],
            maxTransfers:    999,
            maxConcurrent:   this.kMax,
        });

        // Register all pool resources.
        // RegisterResource args: [resourceID, resourceType, agentID, subsetGroup, conditionsJSON]
        // Note: subsetGroup is arg[3], NOT status. Status is always 'active' at registration.
        for (let i = 0; i < poolSize; i++) {
            const id = `${this.prefix}-r-w${workerIndex}-${this.runId}-${i}`;
            this.pool.push(id);
            await sutAdapter.sendRequests({
                contractId:        this.chaincodeId,
                contractFunction:  'RegisterResource',
                contractArguments: [
                    id,
                    'biobank-specimen',
                    this.initAgent,
                    this.groupId,       // subsetGroup ← required for C_global tracking
                    conditions,
                ],
                timeout:  30,
                readOnly: false,
            });
        }

        console.log(`[ViolationQuota] Worker ${workerIndex}: registered ${poolSize} resources ` +
                    `in group "${this.groupId}" with MaxConcurrent=${this.kMax}`);
        console.log(`[ViolationQuota] Expected: fabric-std admits all ${poolSize} transfers ` +
                    `(|S_sub|=${poolSize+1} >> K_max=${this.kMax}); ` +
                    `orderer-endo admits ${this.kMax - 1}, rejects ${poolSize - this.kMax + 1}.`);
    }

    async submitTransaction() {
        this.txIndex++;
        // Each resource gets exactly one Transfer to a UNIQUE new agent.
        // Round-robin ensures we try each resource before repeating.
        const idx        = (this.txIndex - 1) % this.pool.length;
        const resourceId = this.pool[idx];

        // Always transfer FROM the initial holder (not tracking post-transfer state).
        // Rationale: we want every attempt to introduce a NEW agent into S_sub,
        // maximising the violation pressure.  After the first successful transfer,
        // the init-agent is no longer the holder of that resource, so subsequent
        // round-robin attempts on the same resource will fail C_auth — which is
        // expected and logged by Caliper as a (non-quota) failure.
        // With txNumber = poolSize (1 attempt per resource), this doesn't arise.
        const fromAgent  = this.initAgent;
        const toAgent    = `violator-w${this.workerIndex}-r${idx}-t${this.txIndex}`;

        const transferJSON = JSON.stringify({
            toAgent,
            newConditions: {
                allowedActions:  ['research', 'transfer'],
                allowedPurposes: ['oncology', 'genomics'],
                maxTransfers:    999,
                maxConcurrent:   this.kMax,   // must not relax: C_propagation
            },
        });

        await this.sutAdapter.sendRequests({
            contractId:        this.chaincodeId,
            contractFunction:  'Transfer',
            contractArguments: [resourceId, fromAgent, transferJSON],
            timeout:  8,     // short: orderer-dropped txs fail in 8 s instead of 30 s
            readOnly: false,
        });
    }
}

module.exports.createWorkloadModule = () => new ViolationQuotaWorkload();
