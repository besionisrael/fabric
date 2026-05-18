'use strict';

// Workload: transfer — ZooKeeper M_D exogenous baseline.
//
// Protocol (Paper 3, §III.B):
//   1. POST /v1/reserve  → reservationID (or denied)
//   2. If admitted: endorse + submit Transfer to Fabric
//   3. On Fabric commit:  POST /v1/confirm
//   4. On Fabric abort:   POST /v1/cancel
//
// This workload measures:
//   - IVR = 0 (coordinator serialises admission)
//   - Additional latency = Reserve RTT + Confirm/Cancel RTT per transaction
//   - Effective throughput reduction from denied reservations (blocking load)
//
// Failure mode under test: if the Caliper process is killed between steps 2 and 3,
// the coordinator state diverges from the ledger (the stale reservation expires
// after 30 s via TTL). This is the structural weakness vs. the endogenous orderer.

const { WorkloadModuleBase } = require('@hyperledger/caliper-core');
const http = require('http');

const AGENTS = ['agent1', 'agent2', 'agent3', 'agent4', 'agent5'];

// Minimal HTTP helper (no external deps — Caliper environment may be restricted).
function jsonPost(url, body) {
    return new Promise((resolve, reject) => {
        const data = JSON.stringify(body);
        const parsed = new URL(url);
        const options = {
            hostname: parsed.hostname,
            port: parsed.port || 80,
            path: parsed.pathname,
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
                'Content-Length': Buffer.byteLength(data)
            }
        };
        const req = http.request(options, (res) => {
            let raw = '';
            res.on('data', (chunk) => { raw += chunk; });
            res.on('end', () => {
                try { resolve({ status: res.statusCode, body: JSON.parse(raw) }); }
                catch (e) { resolve({ status: res.statusCode, body: raw }); }
            });
        });
        req.on('error', reject);
        req.write(data);
        req.end();
    });
}

class TransferZkWorkload extends WorkloadModuleBase {
    constructor() {
        super();
        this.txIndex = 0;
        this.agentID = null;
        this.resourcePool = [];
        this.subsetGroup = 'biobank-group-A';
        this.maxConcurrent = 3;
        this.coordinatorUrl = 'http://zkcoordinator:8080';

        // Metrics tracked locally and written to Caliper custom results.
        this.reserveAdmitted = 0;
        this.reserveDenied = 0;
        this.confirmCalls = 0;
        this.cancelCalls = 0;
    }

    async initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext) {
        await super.initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext);
        this.agentID = AGENTS[workerIndex % AGENTS.length];
        this.subsetGroup = roundArguments.subsetGroup || 'biobank-group-A';
        this.maxConcurrent = roundArguments.maxConcurrent || 3;
        this.coordinatorUrl = roundArguments.coordinatorUrl || 'http://zkcoordinator:8080';

        for (let i = 0; i < 10; i++) {
            this.resourcePool.push(`res-${this.agentID}-${workerIndex}-${i}`);
        }
    }

    async submitTransaction() {
        const resourceID = this.resourcePool[this.txIndex % this.resourcePool.length];
        this.txIndex++;
        const nextAgent = AGENTS[(AGENTS.indexOf(this.agentID) + 1) % AGENTS.length];

        // Step 1 — Reserve (pre-submission admission check).
        const reserveResp = await jsonPost(`${this.coordinatorUrl}/v1/reserve`, {
            group: this.subsetGroup,
            agent: nextAgent,          // the agent that will gain a holding
            maxConcurrent: this.maxConcurrent
        });

        if (!reserveResp.body.admitted) {
            // Coordinator denied — do NOT submit to Fabric. IVR stays 0.
            this.reserveDenied++;
            // Return a synthetic "skipped" result so Caliper counts the attempt.
            return;
        }

        this.reserveAdmitted++;
        const reservationID = reserveResp.body.reservationId;

        const transferConditions = {
            allowedActions: ['use', 'publish'],
            allowedPurposes: ['clinical'],
            maxTransfers: 3,
            maxConcurrent: this.maxConcurrent,
            expiresAt: '',
            customRules: {}
        };

        // Step 2 — Endorse + submit Transfer to Fabric.
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

        let fabricResult;
        try {
            fabricResult = await this.sutAdapter.sendRequests(request);
        } catch (err) {
            // Step 4 — Fabric abort: cancel the reservation.
            await jsonPost(`${this.coordinatorUrl}/v1/cancel`, { reservationId: reservationID });
            this.cancelCalls++;
            throw err;
        }

        // Step 3 — Fabric commit: confirm the reservation.
        await jsonPost(`${this.coordinatorUrl}/v1/confirm`, { reservationId: reservationID });
        this.confirmCalls++;

        return fabricResult;
    }

    async cleanupWorkloadModule() {
        this.sutAdapter.getLogger().info(
            `ZK workload stats — admitted: ${this.reserveAdmitted}, denied: ${this.reserveDenied}, ` +
            `confirms: ${this.confirmCalls}, cancels: ${this.cancelCalls}`
        );
    }
}

module.exports.createWorkloadModule = () => new TransferZkWorkload();
