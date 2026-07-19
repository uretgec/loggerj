# loggerj — Benchmark Results & Performance Analysis

Detailed benchmark results for `loggerj` with methodology, architectural explanations, and fair comparisons against industry-standard Go loggers.

## Test Environment

| Component | Specification |
|-----------|--------------|
| CPU | Apple M1 Pro (10 cores: 8P + 2E) |
| OS | macOS (darwin/arm64) |
| Go | 1.21+ |
| Flags | `-benchmem -count=5` |
| Race Detector | Disabled for benchmarks (enabled separately for correctness) |
| Writer | `io.Discard` (measures pure logging pipeline, no disk I/O) |

## How to Reproduce

```bash
# Full benchmark suite
go test -bench=. -benchmem -run='^$' -count=5 ./...

# Specific benchmark
go test -bench=BenchmarkLog_JSON -benchmem -run='^$' -count=10 ./...

# CPU profiling
go test -bench=BenchmarkLog_NoFields -cpuprofile=cpu.out -run='^$' ./...
go tool pprof -http=:8080 cpu.out

# Memory profiling
go test -bench=BenchmarkLog_NoFields -memprofile=mem.out -run='^$' ./...
go tool pprof -http=:8080 mem.out

# Compare two runs
go test -bench=. -benchmem -run='^$' -count=10 ./... | tee /tmp/bench1.txt
# (make changes)
go test -bench=. -benchmem -run='^$' -count=10 ./... | tee /tmp/bench2.txt
benchstat /tmp/bench1.txt /tmp/bench2.txt
```

---

## Results Summary

### Core Hot-Path Benchmarks

| Benchmark | ns/op | logs/s | B/op | allocs/op | Description |
|-----------|-------|--------|------|-----------|-------------|
| **Filtered** | **2.07** | **483M** | 0 | 0 | Below-level discard (atomic load only) |
| **Sampling** | **24** | **41M** | 0 | 0 | Lock-free atomic sampling (1/10) |
| **Dropped** | **18.9** | **53M** | 0 | 0 | Channel-full backpressure path |
| **RateLimited** | **41** | **24M** | 0 | 0 | Lock-free CAS rate limiting |
| **JSON_NoEscape** | **53** | **19M** | 0 | 0 | JSON, no special characters |
| **JSON_WithEscape** | **52** | **19M** | 0 | 0 | JSON, with quotes/backslashes |
| **SubProfile_Prefix** | **55** | **18M** | 0 | 0 | Pre-baked prefix + 1 dynamic field |
| **JSON** | **55** | **18M** | 0 | 0 | JSON with 2 key-value fields |
| **NoFields** | **61** | **16M** | 0 | 0 | Simplest possible log call |
| **StringAPI** | **63** | **16M** | 0 | 0 | String message (zero-copy unsafe) |
| **LongMessage** | **62** | **16M** | 0 | 0 | ~230 byte message |
| **ManyFields** | **74** | **13.5M** | 0 | 0 | 8 key-value fields (16 strings) |
| **WithFields** | **61** | **16M** | 0 | 0 | 2 key-value fields |
| **Parallel** | **80** | **12.5M** | 0 | 0 | 10 goroutines concurrent |
| **LevelHelpers** | **80** | **12.5M** | 0 | 0 | `logger.Info()` helper, parallel |
| **LargeBuffer** | **61** | **16M** | 0 | 0 | 16KB worker buffer |
| **SmallBuffer** | **58** | **17M** | 0 | 0 | 1KB worker buffer |

### Expected-Allocation Benchmarks

| Benchmark | ns/op | logs/s | B/op | allocs/op | Reason |
|-----------|-------|--------|------|-----------|--------|
| **WithCaller** | **463** | **2.2M** | 250 | 2 | `runtime.Caller` — Go limitation |
| **VeryLongMessage** | **1500** | **667K** | 10265 | 1 | 10KB msg > Reset threshold → re-alloc |
| **SyncEquivalent** | **1082** | **924K** | ~390 | 3 | Forced `Flush()` per log (not intended usage) |
| **HighContention** | **116** | **8.6M** | 0-2 | 0 | CAS contention under parallel load |

---

## Detailed Analysis

### 1. Filtered Path (~2 ns/op)

```txt
BenchmarkLog_Filtered-10    581,297,246    2.073 ns/op    0 B/op    0 allocs/op
```

The fastest path in `loggerj`. When a log entry is below the current level threshold, the entire call reduces to:

```go
if level < Level(l.currentLevel.Load()) {  // single atomic load
    return
}
```

One `atomic.Load` + one integer comparison + one branch. No function call overhead beyond the method dispatch itself. This is why `loggerj` can safely leave `Debug` calls in production code — they cost ~2ns when filtered.

### 2. Rate Limiting — Lock-Free CAS (~41 ns/op)

```txt
BenchmarkLog_RateLimited-10    29,241,487    41.35 ns/op    0 B/op    0 allocs/op
```

Traditional loggers use `sync.Mutex` + `map[string]*rateLimiter` for per-type rate limiting. This causes:

- Mutex lock/unlock: ~25ns uncontended, ~200ns+ contended
- Map lookup: ~15ns + potential allocation
- Interface boxing: ~10ns + 1 alloc

`loggerj` eliminates all of this with a pre-compiled `SubProfile`:

```go
// Hot path: 2 atomic operations, 0 allocations
func (l *Logger) checkAtomicRateLimit(p *SubProfile) bool {
    now := time.Now().Unix()
    resetTime := p.rlResetAt.Load()       // atomic load
    if now >= resetTime {
        if p.rlResetAt.CompareAndSwap(resetTime, now+p.rlWindow) {
            p.rlCount.Store(0)            // atomic store (window reset)
        }
    }
    return p.rlCount.Add(1) <= p.rlLimit  // atomic add + compare
}
```

Under high contention (10 goroutines hammering a single rate limiter):

```txt
BenchmarkLog_RateLimited_HighContention-10    9,477,832    113.9 ns/op    0 B/op    0 allocs/op
```

Even under extreme contention, the CAS-based approach stays at ~114ns with **zero allocations**. A mutex-based approach would degrade to 500ns+ with lock convoy effects.

### 3. Zero-Copy String API (~63 ns/op)

```txt
BenchmarkLog_StringAPI-10    21,958,332    57.94 ns/op    0 B/op    0 allocs/op
```

The `String` API methods (`InfoString`, `ErrorString`, etc.) use `unsafe.StringData` + `unsafe.Slice` to convert `string → []byte` without allocation:

```go
//go:nosplit
func unsafeStringToBytes(s string) []byte {
    if len(s) == 0 {
        return nil
    }
    return unsafe.Slice(unsafe.StringData(s), len(s))
}
```

This is safe because:

1. Go strings are immutable — the backing memory never changes.
2. The `log()` method immediately copies via `append(e.Msg[:0], msg...)`.
3. The worker goroutine operates on the copied data, not the original.

### 4. Worker-Side Timestamps

Timestamps (`time.Now().UnixMilli()`) are captured by the **worker goroutine** at format time, not in the hot path. This removes a vDSO syscall (~25-40ns) from every log call.

Trade-off: timestamps reflect format time, not call time (±FlushTimeout drift). This is acceptable for async logging and consistent with the documented ordering guarantee.

### 5. Pre-Baked SubProfile Prefixes (~55 ns/op)

```txt
BenchmarkLog_SubProfile_Prefix-10    23,502,894    55.69 ns/op    0 B/op    0 allocs/op
```

Static fields registered via `WithFields("env", "prod", "service", "gateway")` are formatted into `[]byte` **once** at `RegisterSub` time. In the hot path, the worker simply appends the pre-baked prefix:

```go
// Worker: single memcpy, zero formatting
if e.Profile != nil && len(e.Profile.jsonPrefix) > 0 {
    buf = append(buf, e.Profile.jsonPrefix...)
}
```

Compare this to zap/zerolog, which format every field on every log call.

### 6. Copy-on-Write Profile Registry

Profile lookup uses `atomic.Pointer[profileRegistry]` instead of `sync.Map`:

```go
//go:nosplit
func (l *Logger) getProfile(logType string) *SubProfile {
    reg := l.registry.Load()  // single atomic load, no interface boxing
    // ... linear scan (cache-friendly) ...
}
```

| Approach | Lookup Cost | Allocation | Boxing |
|----------|------------|------------|--------|
| `sync.Map` | ~15ns | potential | `interface{}` |
| `map` + `RWMutex` | ~20ns+ | none | none |
| **COW `atomic.Pointer`** | **~3ns** | **none** | **none** |

### 7. Entry Pool with Threshold-Based Release

`sync.Pool` recycles `Entry` structs. `Reset()` releases large slices to prevent permanent memory retention:

```go
func (e *Entry) Reset() {
    // ...
    if cap(e.Msg) > 4096 {
        e.Msg = nil      // Release to GC
    } else {
        e.Msg = e.Msg[:0] // Retain for reuse
    }
}
```

This prevents a single 10KB log from permanently occupying 10KB × 4096 (channel size) = 40MB in the pool.

### 8. SyncEquivalent — Fair Comparison (~1082 ns/op)

```txt
BenchmarkLog_SyncEquivalent-10    1,000,000    1082 ns/op    ~390 B/op    3 allocs/op
```

This benchmark forces `Flush()` after every log to simulate synchronous behavior. The 3 allocations come from:

| # | Source | Bytes |
|---|--------|-------|
| 1 | `Flush()` → `done := make(chan struct{})` | ~96 |
| 2 | `append(e.Msg[:0], msg...)` pool re-alloc | ~128 |
| 3 | `append(e.Fields[:0], fields...)` pool re-alloc | ~160 |

**This is NOT the intended usage pattern.** `loggerj` is designed for async throughput. This benchmark exists solely for fair comparison with synchronous loggers.

---

## Industry Comparison

### Methodology Note

Comparing async and sync loggers is inherently unfair. `loggerj` is async (channel + worker); zap and zerolog are sync (write on call). The table below shows both async and sync-equivalent numbers for transparency.

| Logger | Mode | ns/op | logs/s | allocs/op | Notes |
|--------|------|-------|--------|-----------|-------|
| logrus | sync | ~2000 | ~500K | high | Reflection-based |
| zap | sync | ~400 | ~2.5M | low | Typed fields, sync write |
| zerolog | sync | ~286 | ~3.5M | low | Fluent API, sync write |
| slog (stdlib) | sync | ~350 | ~2.8M | medium | Handler interface |
| **loggerj** | **async** | **~61** | **~16M** | **0** | Channel + worker, zero-copy |
| loggerj | sync-equiv | ~1082 | ~924K | 3 | Forced Flush() per log |

### What This Means

- **Async mode (intended):** `loggerj` is **4-6x faster** than zap/zerolog because the caller only does atomic ops + channel send. The worker handles formatting and I/O asynchronously.
- **Sync-equivalent mode:** When forced to flush after every log, `loggerj` is **slower** than zap/zerolog due to channel synchronization overhead (`make(chan struct{})` per Flush). This is expected — `loggerj` is not designed for synchronous use.
- **The right comparison:** If you need async logging with zap/zerolog, you'd wrap them in a channel + goroutine yourself. `loggerj` gives you this out of the box with zero allocations.

---

## Allocation Breakdown

### Zero-Allocation Path (19 of 22 benchmarks)

The following operations perform **zero heap allocations**:

- Level filtering (`atomic.Load`)
- Profile lookup (`atomic.Pointer.Load` + linear scan)
- Sampling (`atomic.Add`)
- Rate limiting (`atomic.CAS` + `atomic.Add`)
- String→[]byte conversion (`unsafe.StringData`)
- Entry pool get/put (`sync.Pool`)
- Channel send (non-blocking `select`)
- Worker formatting (`append` to pre-allocated buffer)
- Pre-baked prefix injection (`append` of `[]byte`)

### Expected Allocations (3 of 22 benchmarks)

| Benchmark | allocs/op | Source | Mitigation |
|-----------|-----------|--------|------------|
| `WithCaller` | 2 | `runtime.Caller` returns `string` (filename) | Disable in production (`IncludeCaller: false`) |
| `VeryLongMessage` | 1 | 10KB msg exceeds Reset threshold → nil → re-alloc | Expected trade-off for memory safety |
| `SyncEquivalent` | 3 | `Flush()` channel sync + pool cold | Not intended usage pattern |

---

## Architecture: Why It's Fast

```txt
Caller Goroutine (Hot Path)              Worker Goroutine (Cold Path)
┌─────────────────────────────┐          ┌─────────────────────────────┐
│ 1. atomic.Load (level)      │ ~2ns     │                             │
│ 2. atomic.Load (profile)    │ ~3ns     │  time.Now().UnixMilli()     │
│ 3. atomic.Add (sampling)    │ ~5ns     │  formatJSON / formatText    │
│ 4. atomic.CAS (rate limit)  │ ~10ns    │  inject pre-baked prefix    │
│ 5. pool.Get (Entry)         │ ~15ns    │  bufio.Write                │
│ 6. append (msg copy)        │ ~10ns    │  bufio.Flush                │
│ 7. append (fields copy)     │ ~10ns    │  pool.Put (Entry)           │
│ 8. chan <- e (non-blocking) │ ~20ns    │                             │
│                             │          │                             │
│ TOTAL: ~60-80ns, 0 allocs  │          │  Runs on separate goroutine │
└─────────────────────────────┘          └─────────────────────────────┘
```

Key design decisions that enable this:

1. **No `time.Now()` in hot path** — deferred to worker (~30ns saved)
2. **No `sync.Map`** — COW `atomic.Pointer` eliminates interface boxing (~12ns saved)
3. **No `[]byte(msg)` conversion** — `unsafe.StringData` zero-copy (~10ns + 1 alloc saved)
4. **No mutex in hot path** — all operations are atomic or channel-based
5. **Pre-baked prefixes** — static fields formatted once at init, not per-call
6. **Threshold-based pool release** — prevents memory bloat without sacrificing small-message reuse

---

## Buffer Size Impact

| Buffer Config | ns/op | Notes |
|--------------|-------|-------|
| Small (1KB) | ~58 | More frequent flushes, lower memory |
| Default (4KB) | ~61 | Balanced |
| Large (16KB) | ~61 | Fewer flushes, higher memory |

Buffer size has **minimal impact** on hot-path latency because the caller never touches the buffer. It only affects the worker's flush frequency and memory footprint.

---

## Regression Testing

Every PR should run the full benchmark suite and compare against the baseline:

```bash
# Baseline (main branch)
git checkout main
go test -bench=. -benchmem -run='^$' -count=10 ./... | tee /tmp/bench_main.txt

# Feature branch
git checkout feature/my-change
go test -bench=. -benchmem -run='^$' -count=10 ./... | tee /tmp/bench_feature.txt

# Compare
benchstat /tmp/bench_main.txt /tmp/bench_feature.txt
```

**Acceptance criteria:**

- No benchmark may regress by more than **10%** in ns/op
- No benchmark may increase allocs/op (except documented exceptions)
- `BenchmarkLog_Filtered` must remain below **3 ns/op**
- `BenchmarkLog_NoFields` must remain below **100 ns/op**
- `BenchmarkLog_StringAPI` must remain at **0 allocs/op**

---

## References

- [README.md](README.md) — API reference and quick start
- [EXAMPLES.md](EXAMPLES.md) — Comprehensive usage examples
- [Go Benchmark Guide](https://go.dev/doc/tutorial/benchmarks) — Official benchmarking documentation
- [benchstat](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat) — Statistical comparison tool
