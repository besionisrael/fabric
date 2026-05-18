'use strict';

// Post-benchmark IVR audit script.
// Connects to the Fabric network, queries all resources in the test group,
// and reconstructs the temporal sequence of holdings to count C_global violations.
//
// IVR = |{tx ∈ Trace : |S_sub at tx.time| > K_max}| / |Trace|
//
// Usage:
//   node audit-ivr.js <subsetGroup> <maxConcurrent> <connectionProfile>
//
// Outputs: IVR value and a CSV of violation timestamps for Figure 3.

const { Gateway, Wallets } = require('fabric-network');
const fs = require('fs');
const path = require('path');

async function main() {
    const [,, subsetGroup, maxConcurrentStr, profilePath] = process.argv;
    if (!subsetGroup || !maxConcurrentStr || !profilePath) {
        console.error('Usage: node audit-ivr.js <subsetGroup> <maxConcurrent> <connectionProfile>');
        process.exit(1);
    }
    const maxConcurrent = parseInt(maxConcurrentStr, 10);

    const ccpPath = path.resolve(profilePath);
    const ccp = JSON.parse(fs.readFileSync(ccpPath, 'utf8'));

    // Load admin identity from wallet.
    const wallet = await Wallets.newFileSystemWallet('./wallet');
    const gateway = new Gateway();
    await gateway.connect(ccp, {
        wallet,
        identity: 'admin',
        discovery: { enabled: true, asLocalhost: false }
    });

    const network = await gateway.getNetwork('paper3channel');
    const contract = network.getContract('directed');

    // ListByGroup returns all resources in the group.
    const result = await contract.evaluateTransaction('ListByGroup', subsetGroup);
    const resources = JSON.parse(result.toString());

    // Reconstruct timeline from trace entries.
    const events = [];  // { timestamp, action, agent, toAgent }
    for (const res of resources) {
        for (const entry of (res.trace || [])) {
            if (entry.action === 'transfer' || entry.action === 'register') {
                events.push({
                    ts: new Date(entry.timestamp).getTime(),
                    action: entry.action,
                    agent: entry.agent,
                    toAgent: entry.toAgent || null,
                });
            }
        }
    }
    events.sort((a, b) => a.ts - b.ts);

    // Simulate holder set evolution and detect violations.
    const holders = new Map();   // agent → count
    let violations = 0;
    let total = 0;

    function holderCount() {
        return [...holders.values()].filter(v => v > 0).length;
    }

    for (const ev of events) {
        total++;
        if (ev.action === 'register') {
            holders.set(ev.agent, (holders.get(ev.agent) || 0) + 1);
            if (holderCount() > maxConcurrent) violations++;
        } else if (ev.action === 'transfer') {
            // Release from current holder, acquire for toAgent.
            const prev = holders.get(ev.agent) || 0;
            if (prev > 0) holders.set(ev.agent, prev - 1);
            holders.set(ev.toAgent, (holders.get(ev.toAgent) || 0) + 1);
            if (holderCount() > maxConcurrent) violations++;
        }
    }

    const ivr = total > 0 ? violations / total : 0;

    console.log(`Group: ${subsetGroup}  K_max: ${maxConcurrent}`);
    console.log(`Total trace events: ${total}`);
    console.log(`C_global violations: ${violations}`);
    console.log(`IVR: ${ivr.toFixed(4)}`);

    // Write CSV for plot-results.py.
    const csvPath = `./results/ivr-${subsetGroup}.csv`;
    const csv = events.map((ev, i) => {
        const h = holderCount();
        const viol = h > maxConcurrent ? 1 : 0;
        return `${i},${ev.ts},${ev.action},${ev.agent},${ev.toAgent || ''},${h},${viol}`;
    }).join('\n');
    fs.writeFileSync(csvPath, 'seq,timestamp,action,agent,toAgent,holderCount,violation\n' + csv);
    console.log(`CSV written to ${csvPath}`);

    await gateway.disconnect();
}

main().catch(err => { console.error(err); process.exit(1); });
