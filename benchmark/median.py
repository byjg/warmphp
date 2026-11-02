#!/usr/bin/env python3
"""Writes benchmark/results.tsv: for each run (suite, target, server, OPcache), the round with the
median journeys/s, whole, plus the spread of journeys/s between the rounds."""
import csv, glob, statistics

rounds = [list(csv.DictReader(open(f), delimiter='\t')) for f in sorted(glob.glob('benchmark/results-round*.tsv'))]
fields = list(rounds[0][0]) + ['spread']
key = lambda r: (r['suite'], r['target'], r['server'], r['opcache'])
with open('benchmark/results.tsv', 'w', newline='') as out:
    writer = csv.DictWriter(out, fields, delimiter='\t', lineterminator='\n')
    writer.writeheader()
    for first in rounds[0]:
        same = sorted((r for rows in rounds for r in rows if key(r) == key(first)), key=lambda r: float(r['journeys/s']))
        median = same[(len(same) - 1) // 2]
        values = [float(r['journeys/s']) for r in same]
        spread = (max(values) - min(values)) / statistics.median(values) if statistics.median(values) else 0
        writer.writerow({**median, 'spread': f'{100 * spread:.0f}%'})
