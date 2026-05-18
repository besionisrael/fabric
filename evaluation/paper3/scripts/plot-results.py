#!/usr/bin/env python3
"""Generate comparison figures for Paper 3 from Caliper JSON result files.

Usage:
    python3 plot-results.py <results-dir>

Expects Caliper to have written JSON summaries alongside the HTML reports:
    results/fabric-std-report.json
    results/zk-exogenous-report.json
    results/orderer-endogenous-report.json

Outputs:
    figures/fig-throughput.pdf   — Throughput comparison (tx/s)
    figures/fig-latency.pdf      — P50/P95/P99 latency (ms)
    figures/fig-ivr.pdf          — IVR comparison (fraction)
    figures/fig-overhead.pdf     — Coordinator overhead vs. endogenous (ms/tx)
"""
import sys
import json
import pathlib
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
import numpy as np

ARCHITECTURES = {
    'fabric-std':           'M_L (Fabric std)',
    'zk-exogenous':        'M_D exo (ZK)',
    'orderer-endogenous':  'M_D endo (orderer)',
}

COLORS = {
    'fabric-std':          '#d73027',
    'zk-exogenous':       '#fc8d59',
    'orderer-endogenous': '#4575b4',
}

TRANSFER_ROUND = 'concurrent-transfer'   # prefix match


def load_report(path: pathlib.Path) -> dict:
    with open(path) as f:
        return json.load(f)


def find_round(report: dict, label_prefix: str) -> dict | None:
    for r in report.get('rounds', []):
        if r.get('label', '').startswith(label_prefix):
            return r
    return None


def extract_metrics(results_dir: pathlib.Path):
    metrics = {}
    for key in ARCHITECTURES:
        p = results_dir / f'{key}-report.json'
        if not p.exists():
            print(f'Warning: {p} not found, skipping.')
            continue
        report = load_report(p)
        r = find_round(report, TRANSFER_ROUND)
        if r is None:
            print(f'Warning: transfer round not found in {p}')
            continue
        metrics[key] = {
            'throughput': r.get('throughput', {}).get('avg', 0),
            'lat_p50':    r.get('latency', {}).get('p50', 0),
            'lat_p95':    r.get('latency', {}).get('p95', 0),
            'lat_p99':    r.get('latency', {}).get('p99', 0),
            # IVR is computed post-hoc by the audit script; placeholder here.
            'ivr':        r.get('custom', {}).get('ivr', None),
        }
    return metrics


def bar_chart(ax, keys, values, ylabel, title, colors, ylim=None):
    x = np.arange(len(keys))
    bars = ax.bar(x, values, color=[colors[k] for k in keys], width=0.5, edgecolor='black', linewidth=0.7)
    ax.set_xticks(x)
    ax.set_xticklabels([ARCHITECTURES[k] for k in keys], fontsize=9)
    ax.set_ylabel(ylabel, fontsize=10)
    ax.set_title(title, fontsize=11)
    if ylim:
        ax.set_ylim(ylim)
    for bar, val in zip(bars, values):
        ax.text(bar.get_x() + bar.get_width() / 2, bar.get_height() + 0.5,
                f'{val:.1f}', ha='center', va='bottom', fontsize=8)


def main():
    if len(sys.argv) < 2:
        print(__doc__)
        sys.exit(1)

    results_dir = pathlib.Path(sys.argv[1])
    figures_dir = results_dir.parent / 'figures'
    figures_dir.mkdir(exist_ok=True)

    metrics = extract_metrics(results_dir)
    if not metrics:
        print('No results found. Run the benchmarks first.')
        sys.exit(1)

    keys = [k for k in ARCHITECTURES if k in metrics]

    # ── Figure 1: Throughput ──────────────────────────────────────────────────
    fig, ax = plt.subplots(figsize=(5, 3.5))
    bar_chart(ax, keys, [metrics[k]['throughput'] for k in keys],
              'Throughput (tx/s)', 'Effective throughput under C_global load', COLORS)
    fig.tight_layout()
    fig.savefig(figures_dir / 'fig-throughput.pdf')
    plt.close(fig)

    # ── Figure 2: Latency ─────────────────────────────────────────────────────
    fig, ax = plt.subplots(figsize=(6, 3.5))
    x = np.arange(len(keys))
    width = 0.25
    for i, pct in enumerate(['lat_p50', 'lat_p95', 'lat_p99']):
        vals = [metrics[k][pct] for k in keys]
        ax.bar(x + i * width, vals, width, label=f'P{50 if i==0 else 95 if i==1 else 99}',
               edgecolor='black', linewidth=0.7)
    ax.set_xticks(x + width)
    ax.set_xticklabels([ARCHITECTURES[k] for k in keys], fontsize=9)
    ax.set_ylabel('Latency (ms)', fontsize=10)
    ax.set_title('End-to-end latency percentiles', fontsize=11)
    ax.legend(fontsize=8)
    fig.tight_layout()
    fig.savefig(figures_dir / 'fig-latency.pdf')
    plt.close(fig)

    # ── Figure 3: IVR ─────────────────────────────────────────────────────────
    ivr_keys = [k for k in keys if metrics[k]['ivr'] is not None]
    if ivr_keys:
        fig, ax = plt.subplots(figsize=(5, 3.5))
        bar_chart(ax, ivr_keys, [metrics[k]['ivr'] for k in ivr_keys],
                  'IVR (fraction)', 'Invariant Violation Rate', COLORS, ylim=(0, 1))
        ax.axhline(0, color='black', linewidth=0.8, linestyle='--')
        fig.tight_layout()
        fig.savefig(figures_dir / 'fig-ivr.pdf')
        plt.close(fig)
    else:
        print('IVR data not available (run audit script after benchmarks).')

    print(f'Figures written to {figures_dir}/')


if __name__ == '__main__':
    main()
