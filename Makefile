# warmphp
#
#   make build       the gateway binary, ./warmphp
#   make test        the gateway's tests, with real PHP workers
#   make benchmark   the crud benchmark on gluo-framework-comparison (long: see docs/benchmark.md)
#
# The tests run PHP through WARMPHP_PHP, which must pass stdin through; the image loads Xdebug, so
# it is turned off, as in the benchmark. The benchmark takes the BENCH_* variables of
# docs/benchmark.md, e.g. make benchmark BENCH_ONLY=gluo BENCH_JOURNEYS=10

BINARY := warmphp
WARMPHP_PHP ?= docker run --rm -i --init -v $(CURDIR):$(CURDIR) -v /tmp:/tmp -u $(shell id -u):$(shell id -g) byjg/php:8.5-fpm-nginx php -d xdebug.mode=off

.PHONY: build test benchmark

# Static (no cgo): one binary for any Linux, Alpine images included. GOOS and GOARCH pick another
# platform: make build GOOS=darwin GOARCH=arm64
build:
	CGO_ENABLED=0 go build -o $(BINARY) .

test:
	WARMPHP_PHP="$(WARMPHP_PHP)" go test -race ./...

benchmark:
	go test -tags compat -run TestBenchmark -v -timeout 6h ./...
