// violation-quota.js — Caliper 0.6 workload module
// Round 2 of the C_global violation experiment.
//
// Demonstrates the gap between M_L (fabric-std) and M_D (orderer-endogenous):
//
//   M_L cannot reliably enforce C_global because concurrent endorsements both
//   read the same group state, both pass local checks, and both commit —
//   resulting in |S_sub| > K_max (Paper 2, Theorem 2 / MVCC read-write conflict).
//
//   M_D enforces C_global at the orderer via ProcessBatch: transactions that
//   would exceed MaxConcurrent are dropped from the block before commit
//   (Paper 3, Theorem 1).
//
// Pre-condition:
//   Run setup-violation round first (setup-violation.js).  That round registers
//   the resource pool via submitTransaction so Caliper tracks success/failure.
//   This round then just Transfers each resource to a unique new agent.
//
// Pool ID scheme (must match setup-violation.js exactly):
//   Resource:  <prefix>-r-w<workerIndex>-<runTag>-<1..poolSize>
//   Group:     <prefix>-group-w<workerIndex>-<runTag>
//   initAgent: init-agent-<workerIndex>
//
// Design:
//   Every Transfer introduces a FRESH agent into S_sub.
//   With K_MAX = 2 the quota is saturated after the FIRST success.
//   fabric-std:         all poolSize transfers succeed → |S_sub| >> K_MAX  (VIOLATIONS)
//   orderer-endogenous: 1 transfer succeeds, rest timeout → |S_sub| = K_MAX (ENFORCED)
//
// Timeout 8 s: orderer-dropped transactions fail fast rather than waiting 30 s.
//
// Post-run measurement:
//   bash measure-violations.sh <runTag>
//
// Arguments (from benchmark YAML):
//   chaincodeId  — chaincode name (default: directed-traceability)
//   prefix       — resource ID prefix (default: viol)
//   poolSize     — resources per worker; must match setup round (default: 20)
//   kMax         — MaxConcurrent quota (default: 2)
//   runTag       — must match the setup round (default: r01)

'use strict';

const { WorkloadModuleBase } = require('@hyperledger/caliper-core');

class ViolationQuotaWorkload extends WorkloadModuleBase {
    constructor() {
        super();
        this.txIndex     = 0;
        this.pool        = [];
        this.groupId     = '';
        this.initAgent   = '';
        this.chaincodeId = 'directed-traceability';
        this.prefix      = 'viol';
        this.kMax        = 2;
        this.runTag      = 'r01';
    }

    async initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext) {
        await super.initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext);

        this.workerIndex = workerIndex;
        this.chaincodeId = roundArguments.chaincodeId || 'directed-traceability';
        this.prefix      = roundArguments.prefix      || 'viol';
        this.kMax        = roundArguments.kMax        || 2;
        this.runTag      = roundArguments.runTag      || 'r01';
        const poolSize   = roundArguments.poolSize    || 20;

        this.initAgent = `init-agent-${workerIndex}`;
        this.groupId   = `${this.prefix}-group-w${workerIndex}-${this.runTag}`;

        // Reconstruct the pool from deterministic IDs — no network calls needed.
        // These IDs are identical to what setup-violation.js registered in round 1.
        for (let i = 1; i <= poolSize; i++) {
            this.pool.push(`${this.prefix}-r-w${workerIndex}-${this.runTag}-${i}`);
        }

        console.log(`[ViolationQuota] Worker ${workerIndex}: pool of ${poolSize} resources ` +
                    `in group "${this.groupId}" with MaxConcurrent=${this.kMax}`);
        console.log(`[ViolationQuota] Expected (fabric-std): all ${poolSize} transfers succeed ` +
                    `→ |S_sub|=${poolSize + 1} >> K_MAX=${this.kMax} (${poolSize - (this.kMax - 1)} violations per worker)`);
        console.log(`[ViolationQuota] Expected (orderer-endo): 1 transfer succeeds, ` +
                    `${poolSize - 1} timeout → |S_sub|=${this.kMax}, 0 violations`);
    }

    async submitTransaction() {
        this.txIndex++;
        // Each resource slot gets exactly one Transfer attempt to a UNIQUE new agent.
        // Round-robin through the pool so all resources are attempted evenly.
        const idx        = (this.txIndex - 1) % this.pool.length;
        const resourceId = this.pool[idx];

        // Always transfer FROM the initial holder (init-agent-<w>).
        // Rationale: we want every successful Transfer to introduce a FRESH agent
        // into S_sub, maximising violation pressure.  After the first successful
        // transfer the initAgent no longer holds that resource, so subsequent
        // round-robin attempts on the same resource will fail C_auth — which is
        // expected and counted as a (non-quota) failure by Caliper.
        // With txNumber = poolSize (1 attempt per resource), this never arises.
        const fromAgent  = this.initAgent;
        const toAgent    = `violator-w${this.workerIndex}-r${idx}-t${this.txIndex}`;

        const transferJSON = JSON.stringify({
            toAgent,
            newConditions: {
                allowedActions:  ['research', 'transfer'],
                allowedPurposes: ['oncology', 'genomics'],
                maxTransfers:    999,
                maxConcurrent:   this.kMax,   // C_propagation: must not relax the cap
            },
        });

        await this.sutAdapter.sendRequests({
            contractId:        this.chaincodeId,
            contractFunction:  'Transfer',
            contractArguments: [resourceId, fromAgent, transferJSON],
            timeout:  8,      // short: orderer-dropped txs fail in 8 s, not 30 s
            readOnly: false,
        });
    }
}

module.exports.createWorkloadModule = () => new ViolationQuotaWorkload();
