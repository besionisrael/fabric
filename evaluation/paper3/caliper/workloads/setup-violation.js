// setup-violation.js — Caliper 0.6 workload module
// Round 1 of the C_global violation experiment.
//
// Registers a pool of resources with SubsetGroup + MaxConcurrent set, using
// deterministic IDs so the subsequent violation-quota round can reconstruct
// the pool without relying on Date.now() timestamps.
//
// ID scheme:  <prefix>-r-w<workerIndex>-<runTag>-<txIndex>
// Group ID:   <prefix>-group-w<workerIndex>-<runTag>
//
// Arguments (from benchmark YAML):
//   chaincodeId — chaincode name (default: directed-traceability)
//   prefix      — resource ID prefix (default: viol)
//   poolSize    — resources per worker; must equal txNumber/workers (default: 20)
//   kMax        — MaxConcurrent quota (default: 2)
//   runTag      — unique tag per test run to avoid "already exists" errors (default: r01)
//                 Increment (r02, r03…) when re-running on the same network.

'use strict';

const { WorkloadModuleBase } = require('@hyperledger/caliper-core');

class SetupViolationWorkload extends WorkloadModuleBase {
    constructor() {
        super();
        this.txIndex     = 0;
        this.chaincodeId = 'directed-traceability';
        this.prefix      = 'viol';
        this.kMax        = 2;
        this.runTag      = 'r01';
        this.initAgent   = '';
        this.groupId     = '';
    }

    async initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext) {
        await super.initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext);

        this.workerIndex = workerIndex;
        this.chaincodeId = roundArguments.chaincodeId || 'directed-traceability';
        this.prefix      = roundArguments.prefix      || 'viol';
        this.kMax        = roundArguments.kMax        || 2;
        this.runTag      = roundArguments.runTag      || 'r01';

        // Deterministic IDs — no Date.now() — so the violation round can
        // reconstruct the exact same pool without any shared state.
        this.initAgent = `init-agent-${workerIndex}`;
        this.groupId   = `${this.prefix}-group-w${workerIndex}-${this.runTag}`;

        console.log(`[SetupViolation] Worker ${workerIndex}: will register ${roundArguments.poolSize || 20} ` +
                    `resources in group "${this.groupId}" (MaxConcurrent=${this.kMax}, runTag=${this.runTag})`);
    }

    async submitTransaction() {
        this.txIndex++;
        // ID matches what violation-quota.js will compute: prefix-r-w{w}-{tag}-{i}
        const id = `${this.prefix}-r-w${this.workerIndex}-${this.runTag}-${this.txIndex}`;

        const conditions = JSON.stringify({
            allowedActions:  ['research', 'transfer'],
            allowedPurposes: ['oncology', 'genomics'],
            maxTransfers:    999,
            maxConcurrent:   this.kMax,   // C_global bound enforced by constraint orderer
        });

        // RegisterResource args: [resourceID, resourceType, agentID, subsetGroup, conditionsJSON]
        await this.sutAdapter.sendRequests({
            contractId:        this.chaincodeId,
            contractFunction:  'RegisterResource',
            contractArguments: [
                id,
                'biobank-specimen',
                this.initAgent,
                this.groupId,       // SubsetGroup — required for C_global tracking
                conditions,
            ],
            timeout:  30,
            readOnly: false,
        });
    }
}

module.exports.createWorkloadModule = () => new SetupViolationWorkload();
