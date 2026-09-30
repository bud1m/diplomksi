#!/usr/bin/env python3
"""Summarise repeated experiment runs into median and range.

Reads experiments/results/runs/<exp>.run<N>.txt and prints a Markdown table
ready to paste into chapter 6, plus the same table to
experiments/results/SAZETAK.md.

A metric that is identical in every run is reported as a single value rather
than a median of one repeated number; that is the honest way to show a result
that does not vary.
"""
import pathlib
import re
import statistics
from collections import defaultdict

HERE = pathlib.Path(__file__).resolve().parent
RUNS = HERE / 'results' / 'runs'
OUT = HERE / 'results' / 'SAZETAK.md'

# Only the metrics chapter 6 actually cites.
WANTED = {
    'publish attempts': 'pokušaja objave',
    'succeeded': 'uspešnih',
    'failed': 'neuspešnih',
    'client outage window': 'prozor nedostupnosti',
    'pod Ready again after': 'pod ponovo Ready',
    'longest unbroken gap': 'najduži neprekidni prekid',
    'failure rate after': 'stopa otkaza',
    'node marked NotReady': 'čvor označen NotReady',
    'attempts after split': 'pokušaja posle podele',
    'failed after split': 'neuspešnih posle podele',
    'attempts during delay': 'pokušaja tokom kašnjenja',
    'first eviction': 'prva eviction',
    'second eviction': 'druga eviction',
    'Raft members during': 'Raft članova tokom otkaza',
    'leadership stable': 'liderstvo nepromenjeno',
}

NUM = re.compile(r'(-?\d+(?:[.,]\d+)?)')


def parse(path):
    out = {}
    for line in path.read_text(encoding='utf8').splitlines():
        m = re.match(r'\s{2,}(\S.*?)\s{2,}(.+)$', line)
        if not m:
            continue
        key, val = m.group(1).strip(), m.group(2).strip()
        if key in WANTED:
            out[key] = val
    return out


def main():
    if not RUNS.exists():
        print('nema runs/ direktorijuma — pokreni run-repeated.sh')
        return

    by_exp = defaultdict(list)
    for f in sorted(RUNS.glob('*.run*.txt')):
        exp = f.name.split('.run')[0]
        by_exp[exp].append(parse(f))

    lines = ['# Sažetak ponovljenih merenja', '']
    for exp, runs in sorted(by_exp.items()):
        runs = [r for r in runs if r]
        if not runs:
            continue
        lines += [f'## {exp}  ({len(runs)} prolaza)', '',
                  '| Veličina | Medijana | Najmanje | Najviše | Raspon |',
                  '| :- | -: | -: | -: | -: |']
        for key, label in WANTED.items():
            vals = [r[key] for r in runs if key in r]
            if not vals:
                continue
            nums = []
            for v in vals:
                m = NUM.search(v)
                if m:
                    nums.append(float(m.group(1).replace(',', '.')))
            if len(nums) == len(vals) and nums:
                med, lo, hi = statistics.median(nums), min(nums), max(nums)
                fmt = (lambda x: f'{x:.0f}') if all(n == int(n) for n in nums) else (lambda x: f'{x:.1f}')
                spread = '—' if lo == hi else f'{fmt(hi - lo)}'
                lines.append(f'| {label} | {fmt(med)} | {fmt(lo)} | {fmt(hi)} | {spread} |')
            else:
                uniq = sorted(set(vals))
                same = 'isto u svim prolazima' if len(uniq) == 1 else ' / '.join(uniq)
                lines.append(f'| {label} | {same} | | | |')
        lines.append('')

    text = '\n'.join(lines)
    OUT.write_text(text, encoding='utf8')
    print(text)
    print(f'\nzapisano u {OUT}')


if __name__ == '__main__':
    main()
