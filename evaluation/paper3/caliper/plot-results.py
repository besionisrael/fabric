#!/usr/bin/env python3
"""
plot-results.py — Paper 3 §V evaluation figures.

Reads hard-coded Caliper benchmark results (fabric-std, zk-exogenous,
orderer-endogenous) and produces publication-quality PDF figures:

  fig1_throughput.pdf  — grouped bar chart: throughput at sustained / stress
  fig2_latency.pdf     — grouped bar chart: avg & max latency at sustained / stress
  fig3_overhead.pdf    — % overhead of each variant vs fabric-std baseline

Usage:
    pip install matplotlib numpy
    python3 plot-results.py
"""

import numpy as np
import matplotlib
matplotlib.use('Agg')          # headless — no display needed on VM
import matplotlib.pyplot as plt
import matplotlib.patches as mpatches

# ── Raw results ───────────────────────────────────────────────────────────────
# Source: Caliper 0.6 run on Azure Standard_D4s_v3 (4 vCPU, 16 GB RAM)
#         Hyperledger Fabric 3.1.4, 2 workers, 2026-05-25
#
# Structure: results[variant][round] = {tps, avg_lat, max_lat, min_lat, succ, fail}

results = {
    'fabric-std': {
        'warmup':    {'tps': 9.8,  'avg': 0.41, 'max': 0.66, 'min': 0.18, 'succ': 100,  'fail': 0},
        'sustained': {'tps': 49.9, 'avg': 0.37, 'max': 0.65, 'min': 0.10, 'succ': 6002, 'fail': 0},
        'stress':    {'tps': 146.1,'avg': 0.44, 'max': 0.74, 'min': 0.14, 'succ': 8825, 'fail': 0},
        'transfer':  {'tps': 29.9, 'avg': 0.38, 'max': 0.65, 'min': 0.12, 'succ': 3602, 'fail': 0},
    },
    'zk-exogenous': {
        'warmup':    {'tps': 9.8,  'avg': 0.40, 'max': 0.66, 'min': 0.17, 'succ': 100,  'fail': 0},
        'sustained': {'tps': 49.9, 'avg': 0.38, 'max': 0.65, 'min': 0.11, 'succ': 6002, 'fail': 0},
        'stress':    {'tps': 145.1,'avg': 0.44, 'max': 0.77, 'min': 0.15, 'succ': 8768, 'fail': 0},
    },
    'orderer-endogenous': {
        'warmup':    {'tps': 9.8,  'avg': 0.40, 'max': 0.68, 'min': 0.17, 'succ': 100,  'fail': 0},
        'sustained': {'tps': 49.9, 'avg': 0.37, 'max': 0.66, 'min': 0.11, 'succ': 6002, 'fail': 0},
        'stress':    {'tps': 144.1,'avg': 0.44, 'max': 0.74, 'min': 0.14, 'succ': 8702, 'fail': 0},
    },
}

# ── Style ─────────────────────────────────────────────────────────────────────
COLORS = {
    'fabric-std':         '#4878CF',   # blue
    'zk-exogenous':       '#6ACC65',   # green
    'orderer-endogenous': '#D65F5F',   # red
}
LABELS = {
    'fabric-std':         'fabric-std (baseline)',
    'zk-exogenous':       'zk-exogenous',
    'orderer-endogenous': 'orderer-endogenous',
}

plt.rcParams.update({
    'font.family':    'serif',
    'font.size':      10,
    'axes.titlesize': 11,
    'axes.labelsize': 10,
    'legend.fontsize': 9,
    'figure.dpi':     150,
})

VARIANTS  = ['fabric-std', 'zk-exogenous', 'orderer-endogenous']
BAR_WIDTH = 0.25
X_ROUNDS  = ['sustained\n(50 TPS target)', 'stress\n(200 TPS target)']


# ── Figure 1: Throughput ──────────────────────────────────────────────────────
def fig_throughput():
    fig, ax = plt.subplots(figsize=(6, 3.8))

    x = np.arange(len(X_ROUNDS))
    for i, var in enumerate(VARIANTS):
        vals = [results[var]['sustained']['tps'], results[var]['stress']['tps']]
        bars = ax.bar(x + (i - 1) * BAR_WIDTH, vals,
                      width=BAR_WIDTH, label=LABELS[var],
                      color=COLORS[var], edgecolor='white', linewidth=0.5)
        for bar, v in zip(bars, vals):
            ax.text(bar.get_x() + bar.get_width() / 2, bar.get_height() + 0.8,
                    f'{v:.1f}', ha='center', va='bottom', fontsize=8)

    ax.set_xticks(x)
    ax.set_xticklabels(X_ROUNDS)
    ax.set_ylabel('Throughput (TPS)')
    ax.set_title('Throughput: fabric-std vs ZK-exogenous vs Orderer-endogenous')
    ax.set_ylim(0, 175)
    ax.axhline(y=146.1, color=COLORS['fabric-std'], linestyle='--',
               linewidth=0.8, alpha=0.5, label='Baseline saturation (146.1 TPS)')
    ax.legend(loc='upper left', framealpha=0.9)
    ax.spines['top'].set_visible(False)
    ax.spines['right'].set_visible(False)
    fig.tight_layout()
    fig.savefig('results/fig1_throughput.pdf', bbox_inches='tight')
    fig.savefig('results/fig1_throughput.png', bbox_inches='tight')
    print('Saved fig1_throughput.pdf / .png')


# ── Figure 2: Latency (avg + max) ────────────────────────────────────────────
def fig_latency():
    fig, axes = plt.subplots(1, 2, figsize=(8, 3.8), sharey=False)

    for col, rnd in enumerate(['sustained', 'stress']):
        ax = axes[col]
        x  = np.arange(len(VARIANTS))
        avg_vals = [results[v][rnd]['avg'] for v in VARIANTS]
        max_vals = [results[v][rnd]['max'] for v in VARIANTS]

        bars_avg = ax.bar(x - BAR_WIDTH / 2, avg_vals, width=BAR_WIDTH,
                          label='Avg latency', color=[COLORS[v] for v in VARIANTS],
                          edgecolor='white', linewidth=0.5, alpha=0.9)
        bars_max = ax.bar(x + BAR_WIDTH / 2, max_vals, width=BAR_WIDTH,
                          label='Max latency', color=[COLORS[v] for v in VARIANTS],
                          edgecolor='white', linewidth=0.5, alpha=0.5, hatch='//')

        for bar, v in zip(bars_avg, avg_vals):
            ax.text(bar.get_x() + bar.get_width() / 2, bar.get_height() + 0.005,
                    f'{v:.2f}', ha='center', va='bottom', fontsize=7.5)
        for bar, v in zip(bars_max, max_vals):
            ax.text(bar.get_x() + bar.get_width() / 2, bar.get_height() + 0.005,
                    f'{v:.2f}', ha='center', va='bottom', fontsize=7.5)

        ax.set_xticks(x)
        ax.set_xticklabels([LABELS[v].replace(' ', '\n') for v in VARIANTS], fontsize=8)
        ax.set_ylabel('Latency (s)')
        ax.set_title(f'Latency — {rnd} round')
        ax.set_ylim(0, 1.0)
        ax.spines['top'].set_visible(False)
        ax.spines['right'].set_visible(False)

    # Shared legend
    solid_patch  = mpatches.Patch(color='grey', alpha=0.9, label='Avg latency')
    hatch_patch  = mpatches.Patch(facecolor='grey', alpha=0.5, hatch='//', label='Max latency')
    fig.legend(handles=[solid_patch, hatch_patch], loc='upper right',
               bbox_to_anchor=(1.0, 1.0), framealpha=0.9)
    fig.suptitle('Transaction latency across constraint-enforcement variants', y=1.02)
    fig.tight_layout()
    fig.savefig('results/fig2_latency.pdf', bbox_inches='tight')
    fig.savefig('results/fig2_latency.png', bbox_inches='tight')
    print('Saved fig2_latency.pdf / .png')


# ── Figure 3: % overhead vs baseline ─────────────────────────────────────────
def fig_overhead():
    fig, axes = plt.subplots(1, 2, figsize=(7, 3.6))

    metrics = [
        ('Throughput overhead (%)', 'tps',
         lambda base, v: (base - v) / base * 100, True),
        ('Avg-latency overhead (%)', 'avg',
         lambda base, v: (v - base) / base * 100, False),
    ]

    non_base = ['zk-exogenous', 'orderer-endogenous']
    rounds   = ['sustained', 'stress']

    for ax, (title, key, fn, invert) in zip(axes, metrics):
        x = np.arange(len(rounds))
        for i, var in enumerate(non_base):
            vals = [fn(results['fabric-std'][r][key], results[var][r][key])
                    for r in rounds]
            bars = ax.bar(x + (i - 0.5) * BAR_WIDTH, vals,
                          width=BAR_WIDTH, label=LABELS[var],
                          color=COLORS[var], edgecolor='white', linewidth=0.5)
            for bar, v in zip(bars, vals):
                ypos = bar.get_height() + 0.02 if v >= 0 else bar.get_height() - 0.15
                ax.text(bar.get_x() + bar.get_width() / 2, ypos,
                        f'+{v:.1f}%' if v >= 0 else f'{v:.1f}%',
                        ha='center', va='bottom', fontsize=8)

        ax.axhline(0, color='black', linewidth=0.7)
        ax.set_xticks(x)
        ax.set_xticklabels(rounds)
        ax.set_ylabel('Overhead vs fabric-std (%)')
        ax.set_title(title)
        ax.legend(fontsize=8)
        ax.spines['top'].set_visible(False)
        ax.spines['right'].set_visible(False)

    fig.suptitle('Per-variant overhead relative to fabric-std baseline', y=1.02)
    fig.tight_layout()
    fig.savefig('results/fig3_overhead.pdf', bbox_inches='tight')
    fig.savefig('results/fig3_overhead.png', bbox_inches='tight')
    print('Saved fig3_overhead.pdf / .png')


# ── Figure 4: Transfer baseline (fabric-std only) ────────────────────────────
def fig_transfer():
    fig, ax = plt.subplots(figsize=(5, 3.4))

    categories = ['register\n(sustained)', 'register\n(stress)', 'transfer\n(sustained)']
    tps_vals   = [results['fabric-std']['sustained']['tps'],
                  results['fabric-std']['stress']['tps'],
                  results['fabric-std']['transfer']['tps']]
    lat_vals   = [results['fabric-std']['sustained']['avg'],
                  results['fabric-std']['stress']['avg'],
                  results['fabric-std']['transfer']['avg']]

    x    = np.arange(len(categories))
    ax2  = ax.twinx()

    b1 = ax.bar(x - 0.15, tps_vals,  width=0.28, color=COLORS['fabric-std'],
                alpha=0.85, label='Throughput (TPS)', edgecolor='white')
    b2 = ax2.bar(x + 0.15, lat_vals, width=0.28, color='#E88522',
                 alpha=0.75, label='Avg latency (s)', edgecolor='white')

    for bar, v in zip(b1, tps_vals):
        ax.text(bar.get_x() + bar.get_width() / 2, bar.get_height() + 1,
                f'{v:.1f}', ha='center', fontsize=8)
    for bar, v in zip(b2, lat_vals):
        ax2.text(bar.get_x() + bar.get_width() / 2, bar.get_height() + 0.005,
                 f'{v:.2f}', ha='center', fontsize=8)

    ax.set_xticks(x)
    ax.set_xticklabels(categories)
    ax.set_ylabel('Throughput (TPS)', color=COLORS['fabric-std'])
    ax2.set_ylabel('Avg latency (s)', color='#E88522')
    ax.set_ylim(0, 180)
    ax2.set_ylim(0, 0.7)
    ax.set_title('fabric-std baseline: register vs transfer paths')
    ax.spines['top'].set_visible(False)

    lines = [mpatches.Patch(color=COLORS['fabric-std'], alpha=0.85, label='Throughput (TPS)'),
             mpatches.Patch(color='#E88522', alpha=0.75, label='Avg latency (s)')]
    ax.legend(handles=lines, loc='upper right', fontsize=8)
    fig.tight_layout()
    fig.savefig('results/fig4_transfer_baseline.pdf', bbox_inches='tight')
    fig.savefig('results/fig4_transfer_baseline.png', bbox_inches='tight')
    print('Saved fig4_transfer_baseline.pdf / .png')


if __name__ == '__main__':
    import os
    os.makedirs('results', exist_ok=True)
    fig_throughput()
    fig_latency()
    fig_overhead()
    fig_transfer()
    print('\nAll figures written to results/fig*.pdf and results/fig*.png')

    # ── Console summary table ─────────────────────────────────────────────────
    print('\n' + '=' * 72)
    print(f'{"Round":<22} {"fabric-std":>12} {"zk-exo":>12} {"endo":>12}')
    print('=' * 72)
    for rnd in ['sustained', 'stress']:
        row = f'Throughput {rnd:<10}'
        for v in VARIANTS:
            if rnd in results[v]:
                row += f'  {results[v][rnd]["tps"]:>8.1f} TPS'
        print(row)
    for rnd in ['sustained', 'stress']:
        row = f'Avg-lat {rnd:<13}'
        for v in VARIANTS:
            if rnd in results[v]:
                row += f'  {results[v][rnd]["avg"]:>10.2f} s'
        print(row)
    print('=' * 72)
