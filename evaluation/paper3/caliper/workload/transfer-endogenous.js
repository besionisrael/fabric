'use strict';

// Workload: transfer — M_D endogenous (constraint-aware orderer).
//
// Clients submit Transfer transactions directly to Fabric with no coordinator
// round-trip. The Raft leader's filterBatch() evaluates C_global inline and
// rejects violating transactions before writing the block.
//
// Rejected transactions are NOT delivered back to clients (the Fabric EOV
// pipeline has no rejection-notification mechanism). Clients discover rejection
// only by querying the ledger (GetResource) or waiting for their tx to time out.
// For the benchmark we model the client as: submit → wait for commit → if the
// resource state did not advance, the tx was silently rejected → retry with
// exponential backoff.
//
// This is the key ergonomic cost of the endogenous approach relative to the
// ZK coordinator, which gives immediate admission feedback before endorsement.
// However, the endogenous approach eliminates coordinator RTT and the
// confirm/cancel obligation entirely, making it resilient to client crashes.

const { WorkloadModuleBase } = require('@hyperledger/caliper-core');

const AGENTS = ['agent1', 'agent2', 'agent3', 'agent4', 'agent5'];
const RETRY_DELAY_MS = 200;
const MAX_RETRIES = 3;

class TransferEndogenousWorkload extends WorkloadModuleBase {
    constructor() {
        super();
        this.txIndex = 0;
        this.agentID = null;
        this.resourcePool = [];
        this.subsetGroup = 'biobank-group-A';
        this.maxConcurrent = 3;
        this.retryCount = 0;
    }

    async initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext) {
        await super.initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext);
        this.agentID = AGENTS[workerIndex % AGENTS.length];
        this.subsetGroup = roundArguments.subsetGroup || 'biobank-group-A';
        this.maxConcurrent = roundArguments.maxConcurrent || 3;

        for (let i = 0; i < 10; i++) {
            this.resourcePool.push(`res-${this.agentID}-${workerIndex}-${i}`);
        }
    }

    async submitTransaction() {
        const resourceID = this.resourcePool[this.txIndex % this.resourcePool.length];
        this.txIndex++;
        const nextAgent = AGENTS[(AGENTS.indexOf(this.agentID) + 1) % AGENTS.length];

        const transferConditions = {
            allowedActions: ['use', 'publish'],
            allowedPurposes: ['clinical'],
            maxTransfers: 3,
            maxConcurrent: this.maxConcurrent,
            expiresAt: '',
            customRules: {}
        };

        const request = {
            contractId: 'directed',
            contractFunction: 'Transfer',
            contractArguments: [
                resourceID,
                this.agentID,
                JSON.stringify({
                    toAgent: nextAgent,
                    purpose: 'clinical',
                    conditions: transferConditions
                })
            ],
            readOnly: false
        };

        // Simple retry loop — models client behaviour when orderer silently
        // drops the tx (the client observes a timeout or MVCC conflict).
        let lastErr;
        for (let attempt = 0; attempt <= MAX_RETRIES; attempt++) {
            try {
                return await this.sutAdapter.sendRequests(request);
            } catch (err) {
                lastErr = err;
                this.retryCount++;
                await new Promise(r => setTimeout(r, RETRY_DELAY_MS * (attempt + 1)));
            }
        }
        throw lastErr;
    }

    async cleanupWorkloadModule() {
        this.sutAdapter.getLogger().info(
            `Endogenous workload stats — tx retries due to orderer rejection: ${this.retryCount}`
        );
    }
}

module.exports.createWorkloadModule = () => new TransferEndogenousWorkload();
