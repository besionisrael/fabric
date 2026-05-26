// perf-transfer.js — Caliper 0.6 workload module
// Scenario 2: performance overhead of the constraint orderer (M_D) vs stock (M_L).
//
// Design goals:
//   • Engage the constraint evaluator on every Transfer (SubsetGroup + maxConcurrent=999)
//     so M_D overhead is measured even though quota is never exceeded.
//   • Avoid MVCC conflicts: each Caliper worker owns its own disjoint resource pool;
//     resources are transferred in round-robin order within a single worker, so no
//     two in-flight txs from the same worker touch the same resource key.
//   • Sustain transfers across multiple passes: resources ping-pong between two known
//     agents (agentA ↔ agentB).  Local holder tracking keeps fromAgent correct.
//   • Use deterministic IDs (runTag-based) so the setup round (setup-violation.js or
//     perf-overhead.yaml's setup-perf round) and this module share the same pool.
//
// Pool ID scheme (must match setup round exactly):
//   Resource:  perf-r-w<workerIndex>-<runTag>-<1..poolSize>
//   Group:     perf-group-w<workerIndex>-<runTag>
//   agentA:    init-agent-<workerIndex>   (holds all resources after setup)
//   agentB:    perf-agent-<workerIndex>   (alternate holder)
//
// With maxConcurrent=999 the group always has at most 2 distinct holders (agentA and
// agentB) while resources are mid-transfer, well below the quota.  The constraint
// orderer evaluates every transaction but always admits → pure overhead measurement.
//
// Arguments (from benchmark YAML):
//   chaincodeId — chaincode name (default: directed-traceability)
//   prefix      — resource ID prefix (default: perf)
//   poolSize    — resources per worker; must match setup round (default: 20)
//   runTag      — must match the setup round's runTag (default: p01)

'use strict';

const { WorkloadModuleBase } = require('@hyperledger/caliper-core');

// Module-level holder state — persists across rounds within the same Caliper worker
// process.  Keyed by resource ID; value is the agent currently holding that resource.
// This prevents the "wrong fromAgent" C_auth errors when initializeWorkloadModule
// resets the instance for each round but the ledger state carries forward from the
// previous round.
const holderState = new Map();

class PerfTransferWorkload extends WorkloadModuleBase {
    constructor() {
        super();
        this.txIndex     = 0;
        this.pool        = [];     // resource IDs for this worker
        this.holders     = [];     // local mirror; index-aligned with this.pool
        this.agentA      = '';
        this.agentB      = '';
        this.chaincodeId = 'directed-traceability';
        this.prefix      = 'perf';
        this.runTag      = 'p01';
    }

    async initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext) {
        await super.initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext);

        this.workerIndex = workerIndex;
        this.chaincodeId = roundArguments.chaincodeId || 'directed-traceability';
        this.prefix      = roundArguments.prefix      || 'perf';
        this.runTag      = roundArguments.runTag      || 'p01';
        const poolSize   = roundArguments.poolSize    || 20;

        // Two agents for ping-pong transfers — same across all rounds.
        this.agentA = `init-agent-${workerIndex}`;
        this.agentB = `perf-agent-${workerIndex}`;

        // Reconstruct pool from deterministic IDs — no network calls.
        // Matches what setup-perf round registered: prefix-r-w{w}-{tag}-{i}
        this.pool    = [];
        this.holders = [];
        for (let i = 1; i <= poolSize; i++) {
            const id = `${this.prefix}-r-w${workerIndex}-${this.runTag}-${i}`;
            this.pool.push(id);
            // Carry forward holder state from the previous round if available;
            // otherwise seed with agentA (all resources start at init-agent after setup).
            if (!holderState.has(id)) {
                holderState.set(id, this.agentA);
            }
            this.holders.push(holderState.get(id));
        }

        this.txIndex = 0; // reset per-round counter
        console.log(`[PerfTransfer] Worker ${workerIndex} round ${roundIndex}: ` +
                    `pool of ${poolSize} resources (runTag=${this.runTag}), ` +
                    `ping-pong ${this.agentA} ↔ ${this.agentB}`);
    }

    async submitTransaction() {
        this.txIndex++;

        // Round-robin through the pool; each resource is transferred sequentially.
        const idx        = (this.txIndex - 1) % this.pool.length;
        const resourceId = this.pool[idx];
        const fromAgent  = this.holders[idx];
        const toAgent    = fromAgent === this.agentA ? this.agentB : this.agentA;

        const transferJSON = JSON.stringify({
            toAgent,
            newConditions: {
                allowedActions:  ['research', 'transfer'],
                allowedPurposes: ['oncology', 'genomics'],
                maxTransfers:    999,    // never exhausted
                maxConcurrent:   999,    // quota never violated — pure overhead measurement
            },
        });

        // Only advance holder state on success.  If the tx fails (e.g. MVCC conflict),
        // keep the resource at fromAgent so the next attempt on this slot is correct.
        // Without this guard, a single MVCC failure triggers cascading C_auth failures
        // for every subsequent attempt on the same resource slot.
        try {
            await this.sutAdapter.sendRequests({
                contractId:        this.chaincodeId,
                contractFunction:  'Transfer',
                contractArguments: [resourceId, fromAgent, transferJSON],
                timeout:           30,
                readOnly:          false,
            });
            // Success — advance holder state in both the local array and the module Map.
            this.holders[idx] = toAgent;
            holderState.set(resourceId, toAgent);
        } catch (err) {
            // Failure — revert local pre-assignment so fromAgent stays correct.
            // Re-throw so Caliper records the failure in its stats.
            throw err;
        }
    }
}

module.exports.createWorkloadModule = () => new PerfTransferWorkload();
