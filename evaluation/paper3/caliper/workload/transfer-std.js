'use strict';

// Workload: transfer — M_L baseline (Fabric standard).
// Submits Transfer transactions WITHOUT any pre-admission check.
// Under concurrent load with 5 agents and K_max=3, some transactions will
// be admitted by the chaincode despite violating C_global, because the
// chaincode sees only the committed ledger state and cannot observe in-flight
// concurrent transactions. IVR > 0 is the expected and documented outcome.
//
// Post-benchmark IVR computation:
//   Audit the ledger after the run via GetTrace / ListByGroup.
//   Count txs where |S_sub| exceeded K_max at commit time → IVR = violations / total.

const { WorkloadModuleBase } = require('@hyperledger/caliper-core');

const AGENTS = ['agent1', 'agent2', 'agent3', 'agent4', 'agent5'];

class TransferStdWorkload extends WorkloadModuleBase {
    constructor() {
        super();
        this.txIndex = 0;
        this.agentID = null;
        this.resourcePool = [];  // resource IDs registered in the warm-up round
        this.subsetGroup = 'biobank-group-A';
        this.maxConcurrent = 3;
    }

    async initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext) {
        await super.initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext);
        this.agentID = AGENTS[workerIndex % AGENTS.length];
        this.subsetGroup = roundArguments.subsetGroup || 'biobank-group-A';
        this.maxConcurrent = roundArguments.maxConcurrent || 3;

        // Build a pool of resource IDs for this worker (registered during warm-up).
        for (let i = 0; i < 10; i++) {
            this.resourcePool.push(`res-${this.agentID}-${workerIndex}-${i}`);
        }
    }

    async submitTransaction() {
        // Pick a resource from this worker's pool (round-robin).
        const resourceID = this.resourcePool[this.txIndex % this.resourcePool.length];
        this.txIndex++;

        // Transfer to the next agent in the cycle — creates contention on the group.
        const nextAgent = AGENTS[(AGENTS.indexOf(this.agentID) + 1) % AGENTS.length];

        const transferConditions = JSON.stringify({
            allowedActions: ['use', 'publish'],       // conditions narrow on transfer
            allowedPurposes: ['clinical'],
            maxTransfers: 3,
            maxConcurrent: this.maxConcurrent,
            expiresAt: '',
            customRules: {}
        });

        const request = {
            contractId: 'directed',
            contractFunction: 'Transfer',
            contractArguments: [
                resourceID,
                this.agentID,  // current holder (authorised by Fabric identity in practice)
                JSON.stringify({
                    toAgent: nextAgent,
                    purpose: 'clinical',
                    conditions: JSON.parse(transferConditions)
                })
            ],
            readOnly: false
        };

        return this.sutAdapter.sendRequests(request);
    }
}

module.exports.createWorkloadModule = () => new TransferStdWorkload();
