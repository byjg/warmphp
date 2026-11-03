#!/usr/bin/env python3
"""Writes benchmark/results.tsv: for each run (suite, target, server, OPcache), the round with the
median journeys/s, whole, plus the spread of journeys/s between the rounds. Stops if the rounds are
not complete: the same runs in each, none with an error."""
import csv, glob, statistics, sys

rounds = [list(csv.DictReader(open(f), delimiter='\t')) for f in sorted(glob.glob('benchmark/results-round*.tsv'))]
if not rounds:
    sys.exit('no rounds in benchmark/')
fields = list(rounds[0][0]) + ['spread']
key = lambda r: (r['suite'], r['target'], r['server'], r['opcache'])
runs = sorted(key(r) for r in rounds[0])
servers = ['fpm', 'frankenphp-classic', 'worker', 'prefork', 'frankenphp']
for n, rows in enumerate(rounds, 1):
    if sorted(key(r) for r in rows) != runs or len(set(runs)) != len(runs):
        sys.exit(f'round {n} does not have the same runs as round 1')
    for r in rows:
        if r['errors'] != '0' or r['first error']:
            sys.exit(f'round {n}, {key(r)}: {r["errors"]} errors, {r["first error"]}')
with open('benchmark/results.tsv', 'w', newline='') as out:
    writer = csv.DictWriter(out, fields, delimiter='\t', lineterminator='\n')
    writer.writeheader()
    # The rounds run in a random order: the results do not
    for first in sorted(rounds[0], key=lambda r: (r['suite'], r['target'], r['opcache'] != 'on', servers.index(r['server']))):
        same = sorted((r for rows in rounds for r in rows if key(r) == key(first)), key=lambda r: float(r['journeys/s']))
        median = same[(len(same) - 1) // 2]
        values = [float(r['journeys/s']) for r in same]
        spread = (max(values) - min(values)) / statistics.median(values) if statistics.median(values) else 0
        writer.writerow({**median, 'spread': f'{100 * spread:.0f}%'})
