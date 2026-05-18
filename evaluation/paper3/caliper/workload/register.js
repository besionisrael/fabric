'use strict';

// Workload: register resources on the directed-traceability chaincode.
// Common to all three benchmark runs. Each Caliper worker registers
// `resourcesPerWorker` resources with the given subsetGroup and maxConcurrent.
// Used as the warm-up round to populate state before the transfer stress test.

const { WorkloadModuleBase } = require('@hyperledger/caliper-core');

const AGENTS = ['agent1', 'agent2', 'agent3', 'agent4', 'agent5'];

class RegisterWorkload extends WorkloadModuleBase {
    constructor() {
        super();
        this.txIndex = 0;
        this.agentID = null;
        this.resourcesPerWorker = 10;
        this.maxConcurrent = 3;
        this.subsetGroup = 'biobank-group-A';
    }

    async initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext) {
        await super.initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext);
        this.agentID = AGENTS[workerIndex % AGENTS.length];
        this.resourcesPerWorker = roundArguments.resourcesPerWorker || 10;
        this.maxConcurrent = roundArguments.maxConcurrent || 3;
        this.subsetGroup = roundArguments.subsetGroup || 'biobank-group-A';
    }

    async submitTransaction() {
        const resourceID = `res-${this.agentID}-${this.workerIndex}-${this.txIndex++}`;
        const conditions = JSON.stringify({
            allowedActions: ['transfer', 'use', 'publish'],
            allowedPurposes: ['research', 'clinical'],
            maxTransfers: 5,
            maxConcurrent: this.maxConcurrent,
            expiresAt: '',
            customRules: {}
        });

        const request = {
            contractId: 'directed',
            contractFunction: 'RegisterResource',
            contractArguments: [
                resourceID,
                'specimen',
                this.agentID,
                this.subsetGroup,
                conditions
            ],
            readOnly: false
        };

        return this.sutAdapter.sendRequests(request);
    }
}

module.exports.createWorkloadModule = () => new RegisterWorkload();
