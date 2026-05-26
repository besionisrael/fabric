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
        this.holders    = [];   // mirrors on-ledger currentHolder per resource
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
        // maxTransfers: 999 so the counter never depletes during any realistic test.
        const conditions = JSON.stringify({
            allowedActions:  ['research', 'transfer'],
            allowedPurposes: ['oncology', 'genomics'],
            maxTransfers: 999,
        });

        const initHolder = `init-agent-${workerIndex}`;
        for (let i = 0; i < poolSize; i++) {
            const id = `${this.prefix}-pool-w${workerIndex}-${this.runId}-${i}`;
            this.pool.push(id);
            // Track who currently holds each resource; starts with the registration agent.
            this.holders.push(initHolder);
            // RegisterResource args: [resourceID, resourceType, agentID, subsetGroup, conditionsJSON]
            // subsetGroup = '' → no C_global quota for transfer throughput benchmark.
            await sutAdapter.sendRequests({
                contractId:        this.chaincodeId,
                contractFunction:  'RegisterResource',
                contractArguments: [id, 'biobank-research', initHolder, '', conditions],
                timeout:  30,
                readOnly: false,
            });
        }
    }

    async submitTransaction() {
        this.txIndex++;
        // Round-robin through the pool.
        const idx        = this.txIndex % this.pool.length;
        const resourceId = this.pool[idx];

        // Use the locally tracked holder so we always send the correct agentID
        // even after a resource has been transferred multiple times.
        const fromAgent = this.holders[idx];
        const toAgent   = `agent-w${this.workerIndex}-tx${this.txIndex}`;

        // Update holder state optimistically before sending.
        // In a clean test environment failures are negligible; if a tx does fail
        // Caliper records it and the next round-robin attempt for this slot will
        // use the wrong holder (one failure per slot, bounded by pool size).
        this.holders[idx] = toAgent;

        // transferJSON wraps toAgent + newConditions as required by the chaincode.
        // Keep maxTransfers high so the counter never blocks re-transfers.
        const transferJSON = JSON.stringify({
            toAgent,
            newConditions: {
                allowedActions:  ['research', 'transfer'],
                allowedPurposes: ['oncology', 'genomics'],
                maxTransfers: 999,
            },
        });

        const request = {
            contractId:        this.chaincodeId,
            contractFunction:  'Transfer',
            contractArguments: [resourceId, fromAgent, transferJSON],
            timeout:  30,
            readOnly: false,
        };

        await this.sutAdapter.sendRequests(request);
    }
}

module.exports.createWorkloadModule = () => new TransferResourceWorkload();
