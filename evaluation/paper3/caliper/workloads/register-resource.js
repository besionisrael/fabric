// register-resource.js — Caliper 0.6 workload module
// Submits RegisterResource transactions to the directed-traceability chaincode.
// Arguments (from benchmark YAML):
//   chaincodeId  — chaincode name (default: directed-traceability)
//   prefix       — resource ID prefix for this round
//   zkProof      — if true, append a mock ZK proof field to conditions (zk-exogenous variant)

'use strict';

const { WorkloadModuleBase } = require('@hyperledger/caliper-core');

// Simulated ZK proof blob (~512 bytes, realistic size for Groth16 proof).
const MOCK_ZK_PROOF = Buffer.alloc(512, 0xab).toString('base64');

class RegisterResourceWorkload extends WorkloadModuleBase {
    constructor() {
        super();
        this.txIndex = 0;
        this.chaincodeId = 'directed-traceability';
        this.prefix = 'res';
        this.zkProof = false;
    }

    async initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext) {
        await super.initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext);
        this.chaincodeId = roundArguments.chaincodeId || 'directed-traceability';
        this.prefix      = roundArguments.prefix      || 'res';
        this.zkProof     = !!roundArguments.zkProof;
        this.workerIndex = workerIndex;
    }

    async submitTransaction() {
        this.txIndex++;
        const resourceId = `${this.prefix}-w${this.workerIndex}-${this.txIndex}`;

        const conditions = {
            allowedActions:  ['research', 'transfer'],
            allowedPurposes: ['oncology', 'genomics'],
            maxTransfers: 5,
        };
        if (this.zkProof) {
            // zk-exogenous variant: attach mock proof so chaincode must unmarshal it.
            conditions.zkProof = MOCK_ZK_PROOF;
        }

        const request = {
            contractId:        this.chaincodeId,
            contractFunction:  'RegisterResource',
            contractArguments: [
                resourceId,
                'biobank-research',
                `agent-w${this.workerIndex}`,
                'active',
                JSON.stringify(conditions),
            ],
            timeout:  30,
            readOnly: false,
        };

        await this.sutAdapter.sendRequests(request);
    }
}

module.exports.createWorkloadModule = () => new RegisterResourceWorkload();
