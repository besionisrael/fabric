#!/usr/bin/env python3
"""
plot-results.py — Paper 3 §V evaluation figures (ACM DLT 2026).

Actual Caliper 0.6 results from Azure Standard_D4s_v3 (4 vCPU, 16 GB RAM),
Hyperledger Fabric 3.x, 2 workers, 2026-05-26.

Produces four publication-quality PDF figures:
  fig1_violations.pdf   — Sc1: C_global violation rate (M_L vs M_D_endo)
  fig2_throughput.pdf   — Sc2+3: effective TPS at 50/100/200 TPS tiers
  fig3_latency.pdf      — Sc2+3: avg latency at 50/100/200 TPS tiers
  fig4_overhead.pdf     — Sc2+3: % overhead vs M_L baseline

Usage:
    pip install matplotlib numpy
    python3 plot-results.py [--outdir results]
"""

import sys
import os
import argparse
import numpy as np
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
import matplotlib.patches as mpatches

# ─────────────────────────────────────────────────────────────────────────────
# § Data
# ─────────────────────────────────────────────────────────────────────────────

# Scenario 1 — C_global violation rate (K_max=2, 2 workers × 20 resources)
# Run tags: r03 (M_L) and r03 (M_D_endo) on constraint orderer.
SC1 = {
    'M_L':      {'succ': 40, 'fail':  0, 'violations': 36},
    'M_D_endo': {'succ':  2, 'fail': 38, 'violations':  0},
}

# Scenario 2 — perf-overhead (M_L p03 vs M_D_endo p04)
# Scenario 3 — perf-exogenous (M_D_exo z01, co-located coordinator)
# Format: (eff_tps, avg_lat_s, max_lat_s, succ, fail)
SC23 = {
    'M_L': {
        50:  (47.7, 0.37, 0.63, 500, 0),
        100: (95.6, 0.40, 0.68, 500, 0),
        200: (134.8, 0.50, 0.76, 500, 0),
    },
    'M_D_endo': {
        50:  (47.8, 0.38, 0.64, 500, 0),
        100: (95.1, 0.40, 0.67, 500, 0),
        200: (134.0, 0.49, 0.77, 500, 0),
    },
    'M_D_exo': {
        50:  (47.8, 0.38, 0.63, 502, 0),
        100: (95.2, 0.41, 0.68, 502, 0),
        200: (120.6, 0.47, 0.74, 502, 0),
    },
}

TIERS = [50, 100, 200]

# ─────────────────────────────────────────────────────────────────────────────
# § Style
# ─────────────────────────────────────────────────────────────────────────────

COLORS = {
    'M_L':      '#4878CF',   # blue   — stock Fabric
    'M_D_endo': '#D65F5F',   # red    — constraint orderer
    'M_D_exo':  '#6ACC65',   # green  — ZK coordinator
}
LABELS = {
    'M_L':      r'$M_L$ (fabric-std)',
    'M_D_endo': r'$M_{D,\mathrm{endo}}$ (orderer)',
    'M_D_exo':  r'$M_{D,\mathrm{exo}}$ (coordinator)',
}
HATCH = {
    'M_L':      '',
    'M_D_endo': '//',
    'M_D_exo':  'xx',
}

# ACM single-column width = 3.33 in; double-column = 7 in
plt.rcParams.update({
    'font.family':     'serif',
    'font.size':        9,
    'axes.titlesize':  10,
    'axes.labelsize':   9,
    'legend.fontsize':  8,
    'xtick.labelsize':  8,
    'ytick.labelsize':  8,
    'figure.dpi':      150,
    'pdf.fonttype':     42,   # TrueType in PDF — required by ACM
    'ps.fonttype':      42,
})

BAR_W = 0.22


def _save(fig, name, outdir):
    os.makedirs(outdir, exist_ok=True)
    for ext in ('pdf', 'png'):
        path = os.path.join(outdir, f'{name}.{ext}')
        fig.savefig(path, bbox_inches='tight')
    print(f'  Saved {name}.pdf / .png')
    plt.close(fig)


# ─────────────────────────────────────────────────────────────────────────────
# Figure 1 — Scenario 1: C_global violations
# ─────────────────────────────────────────────────────────────────────────────

def fig_violations(outdir):
    """
    Grouped bar chart: admitted Transfer txs vs C_global violations.
    Left bars = Succ (filled).  Right bars = Violations (hatched red).
    """
    modes = ['M_L', 'M_D_endo']
    x = np.arange(len(modes))

    fig, ax = plt.subplots(figsize=(3.5, 3.0))

    succ_vals = [SC1[m]['succ']       for m in modes]
    viol_vals = [SC1[m]['violations'] for m in modes]

    b1 = ax.bar(x - BAR_W / 2, succ_vals,
                width=BAR_W, label='Committed transfers',
                color=[COLORS[m] for m in modes],
                edgecolor='white', linewidth=0.5)
    b2 = ax.bar(x + BAR_W / 2, viol_vals,
                width=BAR_W, label='C_global violations',
                color=[COLORS[m] for m in modes],
                edgecolor='black', linewidth=0.7,
                hatch='//', alpha=0.7)

    for bar, v in zip(b1, succ_vals):
        ax.text(bar.get_x() + bar.get_width() / 2, bar.get_height() + 0.3,
                str(v), ha='center', va='bottom', fontsize=8, fontweight='bold')
    for bar, v in zip(b2, viol_vals):
        ax.text(bar.get_x() + bar.get_width() / 2, bar.get_height() + 0.3,
                str(v), ha='center', va='bottom', fontsize=8, color='firebrick',
                fontweight='bold')

    ax.set_xticks(x)
    ax.set_xticklabels([LABELS[m] for m in modes])
    ax.set_ylabel('Transaction count')
    ax.set_title(r'Scenario 1: $C_\mathrm{global}$ violations ($K_\mathrm{max}=2$)')
    ax.set_ylim(0, 46)

    solid = mpatches.Patch(color='grey', alpha=0.9, label='Committed transfers')
    hatch = mpatches.Patch(facecolor='grey', hatch='//', alpha=0.6,
                           edgecolor='black', label=r'$C_\mathrm{global}$ violations')
    ax.legend(handles=[solid, hatch], loc='upper right', framealpha=0.9)
    ax.spines['top'].set_visible(False)
    ax.spines['right'].set_visible(False)
    fig.tight_layout()
    _save(fig, 'fig1_violations', outdir)


# ─────────────────────────────────────────────────────────────────────────────
# Figure 2 — Scenarios 2+3: Effective throughput
# ─────────────────────────────────────────────────────────────────────────────

def fig_throughput(outdir):
    """
    Grouped bars: effective TPS at 50 / 100 / 200 target TPS for M_L, M_D_endo, M_D_exo.
    """
    modes = ['M_L', 'M_D_endo', 'M_D_exo']
    x = np.arange(len(TIERS))
    offsets = [-BAR_W, 0, BAR_W]

    fig, ax = plt.subplots(figsize=(5.5, 3.4))

    for mode, off in zip(modes, offsets):
        vals = [SC23[mode][t][0] for t in TIERS]
        bars = ax.bar(x + off, vals, width=BAR_W,
                      label=LABELS[mode], color=COLORS[mode],
                      hatch=HATCH[mode], edgecolor='white', linewidth=0.5, alpha=0.9)
        for bar, v in zip(bars, vals):
            ax.text(bar.get_x() + bar.get_width() / 2, bar.get_height() + 0.6,
                    f'{v:.1f}', ha='center', va='bottom', fontsize=7)

    ax.set_xticks(x)
    ax.set_xticklabels([f'{t} TPS target' for t in TIERS])
    ax.set_ylabel('Effective throughput (TPS)')
    ax.set_title('Scenarios 2–3: Throughput by enforcement variant')
    ax.set_ylim(0, 155)
    ax.legend(loc='upper left', framealpha=0.9)
    ax.spines['top'].set_visible(False)
    ax.spines['right'].set_visible(False)
    fig.tight_layout()
    _save(fig, 'fig2_throughput', outdir)


# ─────────────────────────────────────────────────────────────────────────────
# Figure 3 — Scenarios 2+3: Avg latency
# ─────────────────────────────────────────────────────────────────────────────

def fig_latency(outdir):
    """
    Grouped bars: avg latency (s) at 50 / 100 / 200 TPS, with max-latency error bars.
    """
    modes = ['M_L', 'M_D_endo', 'M_D_exo']
    x = np.arange(len(TIERS))
    offsets = [-BAR_W, 0, BAR_W]

    fig, ax = plt.subplots(figsize=(5.5, 3.4))

    for mode, off in zip(modes, offsets):
        avg_vals = [SC23[mode][t][1] for t in TIERS]
        max_vals = [SC23[mode][t][2] for t in TIERS]
        err_up   = [mx - avg for avg, mx in zip(avg_vals, max_vals)]

        bars = ax.bar(x + off, avg_vals, width=BAR_W,
                      label=LABELS[mode], color=COLORS[mode],
                      hatch=HATCH[mode], edgecolor='white', linewidth=0.5, alpha=0.9,
                      yerr=err_up, capsize=3,
                      error_kw={'elinewidth': 0.8, 'ecolor': 'black', 'capthick': 0.8})
        for bar, v in zip(bars, avg_vals):
            ax.text(bar.get_x() + bar.get_width() / 2, v / 2,
                    f'{v:.2f}', ha='center', va='center', fontsize=6.5,
                    color='white', fontweight='bold')

    ax.set_xticks(x)
    ax.set_xticklabels([f'{t} TPS target' for t in TIERS])
    ax.set_ylabel('Avg latency (s)  [error bar = max]')
    ax.set_title('Scenarios 2–3: Latency by enforcement variant')
    ax.set_ylim(0, 0.90)
    ax.legend(loc='upper left', framealpha=0.9)
    ax.spines['top'].set_visible(False)
    ax.spines['right'].set_visible(False)
    fig.tight_layout()
    _save(fig, 'fig3_latency', outdir)


# ─────────────────────────────────────────────────────────────────────────────
# Figure 4 — Overhead relative to M_L baseline
# ─────────────────────────────────────────────────────────────────────────────

def fig_overhead(outdir):
    """
    Two sub-plots:
      Left:  throughput loss (%)  = (TPS_ML - TPS_var) / TPS_ML × 100
      Right: latency overhead (%) = (lat_var - lat_ML)  / lat_ML  × 100
    Positive = worse than M_L. A negative throughput value means higher TPS (noise).
    """
    variants = ['M_D_endo', 'M_D_exo']
    x = np.arange(len(TIERS))
    offsets = [-BAR_W / 2, BAR_W / 2]

    fig, axes = plt.subplots(1, 2, figsize=(7.0, 3.2))

    for ax, (metric_name, idx, sign) in zip(axes, [
        ('Throughput loss vs $M_L$ (%)', 0, -1),   # sign=-1: loss = (ML-var)/ML
        ('Latency overhead vs $M_L$ (%)', 1,  1),  # sign=+1: overhead = (var-ML)/ML
    ]):
        for var, off in zip(variants, offsets):
            if sign == -1:
                vals = [(SC23['M_L'][t][idx] - SC23[var][t][idx])
                        / SC23['M_L'][t][idx] * 100 for t in TIERS]
            else:
                vals = [(SC23[var][t][idx] - SC23['M_L'][t][idx])
                        / SC23['M_L'][t][idx] * 100 for t in TIERS]

            bars = ax.bar(x + off, vals, width=BAR_W,
                          label=LABELS[var], color=COLORS[var],
                          hatch=HATCH[var], edgecolor='white', linewidth=0.5, alpha=0.9)
            for bar, v in zip(bars, vals):
                yoff = 0.15 if v >= 0 else -0.35
                ax.text(bar.get_x() + bar.get_width() / 2,
                        bar.get_height() + yoff,
                        f'{v:+.1f}%', ha='center', va='bottom', fontsize=7)

        ax.axhline(0, color='black', linewidth=0.7)
        ax.set_xticks(x)
        ax.set_xticklabels([f'{t} TPS' for t in TIERS])
        ax.set_ylabel('Overhead (%)')
        ax.set_title(metric_name)
        ax.legend(fontsize=7.5, framealpha=0.9)
        ax.spines['top'].set_visible(False)
        ax.spines['right'].set_visible(False)

    fig.suptitle(r'Overhead of $M_{D,\mathrm{endo}}$ and $M_{D,\mathrm{exo}}$ vs $M_L$ baseline',
                 y=1.02)
    fig.tight_layout()
    _save(fig, 'fig4_overhead', outdir)


# ─────────────────────────────────────────────────────────────────────────────
# § Console summary
# ─────────────────────────────────────────────────────────────────────────────

def print_summary():
    SEP = '─' * 76
    print(f'\n{SEP}')
    print('SCENARIO 1 — C_global Violation Rate (K_max = 2, 40 total transfers)')
    print(SEP)
    print(f'  {"Mode":<18} {"Succ":>6} {"Fail":>6} {"Violations":>12}')
    print(f'  {"─"*18} {"─"*6} {"─"*6} {"─"*12}')
    for m in ['M_L', 'M_D_endo']:
        d = SC1[m]
        print(f'  {LABELS[m]:<18} {d["succ"]:>6} {d["fail"]:>6} {d["violations"]:>12}')

    print(f'\n{SEP}')
    print('SCENARIOS 2–3 — Performance Overhead (500 txs, 100 resources/worker)')
    print(SEP)
    print(f'  {"Tier":>8} {"Mode":<22} {"Eff TPS":>9} {"Avg lat":>9} {"Max lat":>9}')
    print(f'  {"─"*8} {"─"*22} {"─"*9} {"─"*9} {"─"*9}')
    for t in TIERS:
        for m in ['M_L', 'M_D_endo', 'M_D_exo']:
            tps, avg, mx = SC23[m][t][:3]
            tier = f'{t} TPS' if m == 'M_L' else ''
            print(f'  {tier:>8} {LABELS[m]:<22} {tps:>8.1f}  {avg:>8.2f}s  {mx:>8.2f}s')
        print()

    print(SEP)
    print('OVERHEAD vs M_L  (+ = worse, - = better)')
    print(SEP)
    print(f'  {"Tier":>8} {"Variant":<22} {"TPS Δ":>9} {"Lat Δ":>9}')
    print(f'  {"─"*8} {"─"*22} {"─"*9} {"─"*9}')
    for t in TIERS:
        for var in ['M_D_endo', 'M_D_exo']:
            tps_delta = (SC23[var][t][0] - SC23['M_L'][t][0]) / SC23['M_L'][t][0] * 100
            lat_delta = (SC23[var][t][1] - SC23['M_L'][t][1]) / SC23['M_L'][t][1] * 100
            tier = f'{t} TPS' if var == 'M_D_endo' else ''
            print(f'  {tier:>8} {LABELS[var]:<22} {tps_delta:>+8.1f}%  {lat_delta:>+8.1f}%')
        print()
    print(SEP)


# ─────────────────────────────────────────────────────────────────────────────
# § Main
# ─────────────────────────────────────────────────────────────────────────────

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description='Generate Paper 3 §V figures')
    parser.add_argument('--outdir', default='results',
                        help='Output directory for figures (default: results/)')
    args = parser.parse_args()

    print(f'Writing figures to {args.outdir}/')
    fig_violations(args.outdir)
    fig_throughput(args.outdir)
    fig_latency(args.outdir)
    fig_overhead(args.outdir)
    print('\nDone.')
    print_summary()
