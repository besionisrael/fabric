// transfer-resource.js — Caliper 0.6 workload module
// Pre-registers a pool of resources during initializeWorkloadModule, then
// submits TransferResource transactions to exercise the read-modify-write path.
// Arguments:
//   chaincodeId — chaincode name
//   prefix      — resource ID prefix
//   poolSize    — number of resources to pre-register (default: 100)

'use strict';

const { WorkloadModuleBase } = require('@hyperledger/caliper-core');

class TransferResourceWorkload extends WorkloadModuleBase {
    constructor() {
        super();
        this.txIndex    = 0;
        this.pool       = [];
        this.chaincodeId = 'directed-traceability';
        this.prefix      = 'xfer';
    }

    async initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext) {
        await super.initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext);
        this.chaincodeId = roundArguments.chaincodeId || 'directed-traceability';
        this.prefix      = roundArguments.prefix      || 'xfer';
        this.workerIndex = workerIndex;
        this.runId = Date.now();

        const poolSize = roundArguments.poolSize || 100;

        // Pre-register the resource pool so transfer round has valid targets.
        const conditions = JSON.stringify({
            allowedActions:  ['research', 'transfer'],
            allowedPurposes: ['oncology', 'genomics'],
            maxTransfers: 50,
        });

        for (let i = 0; i < poolSize; i++) {
            const id = `${this.prefix}-pool-w${workerIndex}-${this.runId}-${i}`;
            this.pool.push(id);
            await sutAdapter.sendRequests({
                contractId:        this.chaincodeId,
                contractFunction:  'RegisterResource',
                contractArguments: [id, 'biobank-research', `init-agent-${workerIndex}`, 'active', conditions],
                timeout:  30,
                readOnly: false,
            });
        }
    }

    async submitTransaction() {
        this.txIndex++;
        // Round-robin through the pool.
        const resourceId   = this.pool[this.txIndex % this.pool.length];
        // agentID = current holder (set during pool registration as init-agent-<workerIndex>)
        // transferJSON wraps toAgent + newConditions as required by the chaincode.
        const currentHolder = `init-agent-${this.workerIndex}`;
        const toAgent       = `agent-w${this.workerIndex}-${this.txIndex}`;
        const transferJSON  = JSON.stringify({
            toAgent,
            newConditions: {
                allowedActions:  ['research'],
                allowedPurposes: ['oncology'],
                maxTransfers: 10,
            },
        });

        const request = {
            contractId:        this.chaincodeId,
            contractFunction:  'Transfer',
            contractArguments: [resourceId, currentHolder, transferJSON],
            timeout:  30,
            readOnly: false,
        };

        await this.sutAdapter.sendRequests(request);
    }
}

module.exports.createWorkloadModule = () => new TransferResourceWorkload();
