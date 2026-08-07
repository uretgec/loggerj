GO ?= go

# Protected hot-path benchmarks for v1.4.0 benchmark gate.
# This list is used by local benchmark commands; PR-2 will use the same
# list in CI via tools/benchgate.
HOT_BENCHMARKS := $(shell paste -sd'|' hotpath.txt)

.PHONY: verify build vet test race cover bench bench-hot bench-base tidy

verify: build vet test

build:
	$(GO) build ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test .

race:
	$(GO) test -race -count=1 .

cover:
	$(GO) test -race -count=1 -coverprofile=coverage.out -covermode=atomic .
	$(GO) tool cover -func=coverage.out

bench:
	$(GO) test -run='^$$' -bench=. -benchmem -count=1 .

bench-hot:
	$(GO) test -run='^$$' \
	  -bench='$(HOT_BENCHMARKS)' \
	  -benchmem \
	  -benchtime=2s \
	  -count=6 \
	  -timeout=30m \
	  .

bench-base:
	mkdir -p bench
	$(GO) test -run='^$$' \
	  -bench='$(HOT_BENCHMARKS)' \
	  -benchmem \
	  -benchtime=2s \
	  -count=6 \
	  -timeout=30m \
	  . | tee bench/base.txt

tidy:
	$(GO) mod tidy

fuzz-smoke:
	$(GO) test -run='^$$' -fuzz=FuzzAppendJSONString -fuzztime=30s .
	$(GO) test -run='^$$' -fuzz=FuzzAppendDuration -fuzztime=30s .
	$(GO) test -run='^$$' -fuzz=FuzzRateLimitWindow -fuzztime=30s .

fuzz-long:
	$(GO) test -run='^$$' -fuzz=FuzzAppendJSONString -fuzztime=60m .
	$(GO) test -run='^$$' -fuzz=FuzzAppendDuration -fuzztime=60m .
	$(GO) test -run='^$$' -fuzz=FuzzRateLimitWindow -fuzztime=60m .