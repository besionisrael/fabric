// perf-exogenous.js — Caliper 0.6 workload module
// Scenario 3: M_D exogenous — Transfer with ZooKeeper coordinator pre-admission.
//
// Protocol (Paper 3, §III.B):
//   1. POST /v1/reserve  → {admitted, reservationId}
//   2. If admitted: submit Transfer to Fabric
//   3. On commit:  POST /v1/confirm
//   4. On abort:   POST /v1/cancel
//
// Pool design mirrors perf-transfer.js (ping-pong agentA ↔ agentB, 100 resources
// per worker, module-level holder state).  MaxConcurrent=999 so the coordinator
// always admits — this is a pure latency overhead measurement, not a quota test.
//
// The overhead vs M_D endogenous (perf-transfer.js) is exactly:
//   Δlatency = RTT_reserve + RTT_confirm   (two extra HTTP round-trips per tx)
//
// Arguments (from benchmark YAML):
//   chaincodeId    — chaincode name (default: directed-traceability)
//   prefix         — resource ID prefix (default: perf)
//   poolSize       — resources per worker; must match setup round (default: 100)
//   runTag         — must match the setup round's runTag (default: z01)
//   coordinatorUrl — base URL of the ZK coordinator (default: http://zkcoordinator:8080)

'use strict';

const { WorkloadModuleBase } = require('@hyperledger/caliper-core');
const http = require('http');
const https = require('https');

// Module-level holder state — persists across rounds (same rationale as perf-transfer.js).
const holderState = new Map();

// Minimal JSON POST helper (no external dependencies).
function jsonPost(url, body) {
    return new Promise((resolve, reject) => {
        const data = JSON.stringify(body);
        let parsed;
        try { parsed = new URL(url); } catch (e) { return reject(e); }

        const lib = parsed.protocol === 'https:' ? https : http;
        const options = {
            hostname: parsed.hostname,
            port:     parsed.port || (parsed.protocol === 'https:' ? 443 : 80),
            path:     parsed.pathname + (parsed.search || ''),
            method:   'POST',
            headers: {
                'Content-Type':   'application/json',
                'Content-Length': Buffer.byteLength(data),
            },
        };

        const req = lib.request(options, (res) => {
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

class PerfExogenousWorkload extends WorkloadModuleBase {
    constructor() {
        super();
        this.txIndex        = 0;
        this.pool           = [];
        this.holders        = [];
        this.agentA         = '';
        this.agentB         = '';
        this.groupId        = '';
        this.chaincodeId    = 'directed-traceability';
        this.prefix         = 'perf';
        this.runTag         = 'z01';
        this.coordinatorUrl = 'http://zkcoordinator:8080';
    }

    async initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext) {
        await super.initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext);

        this.workerIndex     = workerIndex;
        this.chaincodeId     = roundArguments.chaincodeId    || 'directed-traceability';
        this.prefix          = roundArguments.prefix         || 'perf';
        this.runTag          = roundArguments.runTag         || 'z01';
        this.coordinatorUrl  = roundArguments.coordinatorUrl || 'http://zkcoordinator:8080';
        const poolSize       = roundArguments.poolSize       || 100;

        this.agentA  = `init-agent-${workerIndex}`;
        this.agentB  = `perf-agent-${workerIndex}`;
        this.groupId = `${this.prefix}-group-w${workerIndex}-${this.runTag}`;

        // Reconstruct pool from deterministic IDs (matches setup-violation.js).
        this.pool    = [];
        this.holders = [];
        for (let i = 1; i <= poolSize; i++) {
            const id = `${this.prefix}-r-w${workerIndex}-${this.runTag}-${i}`;
            this.pool.push(id);
            if (!holderState.has(id)) {
                holderState.set(id, this.agentA);
            }
            this.holders.push(holderState.get(id));
        }

        this.txIndex = 0;
        console.log(`[PerfExogenous] Worker ${workerIndex} round ${roundIndex}: ` +
                    `pool=${poolSize} runTag=${this.runTag} group=${this.groupId} ` +
                    `coordinator=${this.coordinatorUrl}`);
    }

    async submitTransaction() {
        this.txIndex++;

        const idx        = (this.txIndex - 1) % this.pool.length;
        const resourceId = this.pool[idx];
        const fromAgent  = this.holders[idx];
        const toAgent    = fromAgent === this.agentA ? this.agentB : this.agentA;

        // ── Step 1: Reserve ──────────────────────────────────────────────────
        // Pre-admission check at the coordinator (exogenous enforcement).
        // With maxConcurrent=999 this is always admitted; we measure the RTT cost.
        const reserveResp = await jsonPost(`${this.coordinatorUrl}/v1/reserve`, {
            group:         this.groupId,
            agent:         toAgent,
            maxConcurrent: 999,
        });

        if (!reserveResp.body.admitted) {
            // Coordinator denied — count as failure (should never happen with K_max=999).
            throw new Error(`Coordinator denied: ${reserveResp.body.reason}`);
        }
        const reservationId = reserveResp.body.reservationId;

        // ── Step 2: Submit Transfer to Fabric ────────────────────────────────
        const transferJSON = JSON.stringify({
            toAgent,
            newConditions: {
                allowedActions:  ['research', 'transfer'],
                allowedPurposes: ['oncology', 'genomics'],
                maxTransfers:    999,
                maxConcurrent:   999,
            },
        });

        try {
            await this.sutAdapter.sendRequests({
                contractId:        this.chaincodeId,
                contractFunction:  'Transfer',
                contractArguments: [resourceId, fromAgent, transferJSON],
                timeout:           30,
                readOnly:          false,
            });

            // ── Step 3: Confirm ──────────────────────────────────────────────
            // Fabric committed — finalize the reservation.
            await jsonPost(`${this.coordinatorUrl}/v1/confirm`, { reservationId });

            // Advance holder state only on success.
            this.holders[idx] = toAgent;
            holderState.set(resourceId, toAgent);

        } catch (err) {
            // ── Step 4: Cancel ───────────────────────────────────────────────
            // Fabric aborted — roll back the reservation.
            try {
                await jsonPost(`${this.coordinatorUrl}/v1/cancel`, { reservationId });
            } catch (cancelErr) {
                // Best-effort cancel; don't mask the original error.
                console.error(`[PerfExogenous] Cancel failed for ${reservationId}: ${cancelErr.message}`);
            }
            throw err; // Re-throw so Caliper records the failure.
        }
    }
}

module.exports.createWorkloadModule = () => new PerfExogenousWorkload();
