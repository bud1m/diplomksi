#!/usr/bin/env python3
"""Draw the figures for chapter 6 from the raw publisher samples.

Design notes, since a thesis is printed:
  - no colour carries meaning on its own. Success and failure differ by tick
    height and shade, so the figure survives a black-and-white printer.
  - one axis, no second scale.
  - the values that matter are labelled directly on the plot rather than left
    for the reader to measure against the axis.
"""
import json
import pathlib
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt

HERE = pathlib.Path(__file__).resolve().parent
OUT = HERE.parent / 'docs' / 'thesis' / 'slike'
OUT.mkdir(parents=True, exist_ok=True)

INK = '#1a1a1a'
OK = '#b8b8b8'      # success: recessive, it is the uninteresting case
FAIL = '#1a1a1a'    # failure: the thing being looked for
GRID = '#e6e6e6'

plt.rcParams.update({
    'font.family': 'DejaVu Sans',
    'font.size': 8,
    'axes.edgecolor': '#999999',
    'axes.labelcolor': INK,
    'text.color': INK,
    'xtick.color': '#666666',
    'ytick.color': '#666666',
})


def load(name):
    return json.loads((HERE / 'results' / name).read_text())['samples']


def panel(ax, samples, fault_at_s, title, note):
    t0 = samples[0]['t']
    for r in samples:
        x = r['t'] - t0
        if r['ok']:
            ax.vlines(x, 0, 0.35, color=OK, linewidth=0.8)
        else:
            ax.vlines(x, 0, 1.0, color=FAIL, linewidth=1.4)

    ax.axvline(fault_at_s, color=INK, linestyle='--', linewidth=1.0)
    ax.text(fault_at_s + 0.8, 1.18, 'otkaz izazvan', fontsize=7.5,
            style='italic', va='center')

    ax.set_ylim(0, 1.45)
    ax.set_xlim(-1, (samples[-1]['t'] - t0) + 1)
    ax.set_yticks([])
    ax.set_xlabel('vreme od početka merenja (s)')
    ax.set_title(title, fontsize=9, loc='left', pad=10)
    ax.grid(axis='x', color=GRID, linewidth=0.6)
    ax.set_axisbelow(True)
    for side in ('top', 'right', 'left'):
        ax.spines[side].set_visible(False)
    ax.text(0.995, 0.93, note, transform=ax.transAxes, ha='right', va='top',
            fontsize=7.5)


def main():
    e3 = load('exp03-publisher.json')
    e4 = load('exp04-publisher.json')

    fig, axes = plt.subplots(2, 1, figsize=(7.2, 4.4))
    panel(axes[0], e3, 10.0,
          'Eksperiment 3 — zaustavljen radni čvor sa Raft liderom (treći prolaz)',
          '169 od 180 objava uspešno · najduži prekid 50,0 s')
    panel(axes[1], e4, 10.0,
          'Eksperiment 4 — mrežna particija jednog brokera (treći prolaz)',
          '119 od 132 objava uspešno · najduži prekid 10,0 s')

    # One legend for the whole figure; height already distinguishes the two.
    from matplotlib.lines import Line2D
    fig.legend(
        handles=[Line2D([0], [0], color=OK, lw=2, label='uspešna objava'),
                 Line2D([0], [0], color=FAIL, lw=2, label='neuspešna objava')],
        loc='lower center', ncol=2, frameon=False, fontsize=8,
        bbox_to_anchor=(0.5, -0.02))
    fig.tight_layout(rect=(0, 0.05, 1, 1))
    fig.savefig(OUT / 'slika-6-1-vremenska-osa.png', dpi=300)
    print('wrote slika-6-1-vremenska-osa.png')


if __name__ == '__main__':
    main()
