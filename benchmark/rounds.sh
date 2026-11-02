#!/bin/bash
# Runs the benchmark in rounds, one target at a time, with only that target's database up, and
# keeps each round in benchmark/results-round<n>.tsv. `median.py` then writes results.tsv.
#   benchmark/rounds.sh <rounds> <test binary, or "go test">     from the repository's root
# BENCH_* pass through to the benchmark.
set -u
rounds=${1:-3}
bench=${2:-go test}
comparison=${COMPAT_DIR:-../gluo-framework-comparison}
[ "$bench" = "go test" ] && run() { go test -tags compat -run TestBenchmark -v -timeout 3h ./...; } \
                         || run() { "$bench" -test.run TestBenchmark -test.v -test.timeout 3h; }

#        suite     BENCH_ONLY       project                      compose files                               database service
targets="fullstack symfony          fullstack/symfony-official   compose.yaml,compose.override.yaml          database
fullstack cakephp                   fullstack/cakephp            compose.yaml                                database
fullstack codeigniter               fullstack/codeigniter        compose.yaml                                database
fullstack laravel                   fullstack/laravel            compose.yaml                                mysql
api       gluo                      gluo                         docker-compose.yml                          gluo-database
api       api-symfony               api/symfony                  compose.yaml                                database
api       api-cakephp               api/cakephp                  compose.yaml                                database
api       api-codeigniter           api/codeigniter              compose.yaml                                database
api       api-laravel               api/laravel                  docker-compose.yml                          mysql"

for round in $(seq 1 "$rounds"); do
    rm -f benchmark/results.tsv
    while read -r -u 3 suite only project files service; do
        compose=(docker compose --project-directory "$comparison/frameworks/$project")
        for file in ${files//,/ }; do compose+=(-f "$comparison/frameworks/$project/$file"); done
        echo "== round $round $only $(date +%H:%M)"
        "${compose[@]}" up -d --wait "$service" 2>&1 | tail -1
        # On an empty volume, MySQL reports healthy on a temporary server, then restarts: TCP is the real one
        for _ in $(seq 1 120); do
            "${compose[@]}" exec -T "$service" sh -c 'mysql -h127.0.0.1 -uroot -p"$MYSQL_ROOT_PASSWORD" -e "select 1"' >/dev/null 2>&1 && break
            sleep 1
        done
        BENCH_SUITE=$suite BENCH_ONLY=$only run 2>&1 | grep -E -- '^\s+--- FAIL|^FAIL|journeys failed|docker run:|nothing answers|reseed'
        "${compose[@]}" down 2>&1 | tail -1
    done 3<<< "$targets"
    mv benchmark/results.tsv "benchmark/results-round$round.tsv"
done
date +%H:%M
