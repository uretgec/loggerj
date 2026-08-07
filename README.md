<p align="center">
  <img src="https://img.shields.io/badge/license-MIT-blue?style=for-the-badge" alt="License: MIT">
  <img src="https://img.shields.io/badge/Go-1.21+-00ADD8?style=for-the-badge&logo=go" alt="Go Version">
  <img src="https://img.shields.io/badge/version-v1.4.0-green?style=for-the-badge" alt="Version: 1.4.0">
</p>

# loggerj

Ultra-high-performance, lock-free, asynchronous logging for Go. **19M logs/s async, 9M logs/s sync, zero allocations in the hot path.**

## Philosophy

Most loggers sacrifice performance for convenience. `loggerj` takes a different approach: the **"Pre-Compiled Execution Profile"** architecture bakes rate limits, sampling, and static fields into memory once at startup. The hot path is pure atomic operations and memory copies — zero mutex locks, zero heap allocations, zero GC pressure.

## Features

- 🚀 **19M logs/s async throughput** — lock-free channel + worker pipeline
- 🎯 **Zero-allocation typed fields** — `Int()`, `Bool()`, `Dur()` with no interface boxing
- ⚡ **Lock-free rate limiting & sampling** — `atomic.CompareAndSwap` (CAS) with bounded backoff
- 🧠 **Pre-baked SubProfiles** — static fields formatted once at init, zero CPU at log time
- 📋 **Copy-on-write registry** — `atomic.Pointer` for lock-free reads, adaptive linear/map lookup
- 🛡️ **Non-blocking & drop-monitored** — safe drops with atomic counter + `SetOnDrop` callback
- 🔄 **Native log rotation** — size-based with backup retention, zero external dependencies
- ⏱️ **Sync mode with durability tiers** — 4 explicit tiers (OSBuffered, Direct, FsyncEveryN, FsyncEveryWrite)
- 🎛️ **Runtime level control** — ~2ns atomic level changes
- 🔗 **Standard library compatible** — intercept `std log` via `io.Writer` adapter (zero-alloc on Go 1.22+, 1 alloc on Go 1.21 due to compiler escape analysis)
- 🌐 **slog.Handler adapter** — routes `slog` calls through loggerj's zero-alloc pipeline
- ✅ **Deterministic flush** — `Flush()` drains channel before writing, no log loss
- 🧪 **Fuzz-tested** — `appendJSONString`, `appendDuration`, rate-limit window calculation

## Quick Start

```go
package main

import (
    "context"
    "time"

    "github.com/uretgec/loggerj"
)

func main() {
    // 1. Initialize Logger
    logger := loggerj.NewLogger(loggerj.Config{
        JSONOutput:   true,
        FlushTimeout: 50 * time.Millisecond,
    })

    // 2. Define SubProfiles (COLD PATH: do this once at startup)
    logger.RegisterSub("HTTP",
        loggerj.WithRateLimit(1000, time.Second),
        loggerj.WithFields("env", "prod", "service", "gateway"),
    )

    // 3. Start the async worker
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    go logger.Start(ctx)
    defer logger.Close()

    // 4. Log messages (HOT PATH: ultra-fast, zero allocation)
    logger.InfoString("HTTP", "request received", "method", "GET", "path", "/api")
    
    // 5. Typed fields for dynamic values (zero alloc)
    status := 200
    latency := 150 * time.Millisecond
    logger.InfoFields("HTTP", []byte("request completed"),
        loggerj.Int("status", status),
        loggerj.Dur("latency", latency),
        loggerj.Bool("cached", true),
    )

    // 6. slog integration (optional)
    slogger := slog.New(loggerj.NewSlogHandler(logger, "APP"))
    slogger.Info("slog request", "method", "POST", "status", 201)

    // 7. Ensure all logs are written before exit
    logger.Flush()
}
```

## Standard Library Integration

`loggerj` provides an `io.Writer` adapter to intercept logs from Go's standard `log` package and third-party libraries:

```go
log.SetFlags(0) // Disable std log timestamps; loggerj adds its own
log.SetOutput(logger.AsWriter(loggerj.LevelInfo, "STDLIB"))
log.Println("This message flows through loggerj's async pipeline")
```

> **Version note:** The `AsWriter` adapter is zero-allocation on **Go 1.22+**. On Go 1.21, it shows 1 alloc/op due to less mature escape analysis across the `io.Writer` interface boundary. This is a compiler limitation, not a code bug. The hot-path `InfoString`/`InfoFields` APIs are unaffected.

## Performance Highlights

Benchmarks on Apple M1 Pro (10 cores), Go 1.24. See [BENCH.md](BENCH.md) for full results and methodology.

### Async Mode (Intended Usage)

| Mode | ns/op | logs/s | Allocs | Use Case |
|------|-------|--------|--------|----------|
| **Filtered** | 2.1 | 483M | 0 | Debug logs in production |
| **RateLimited** | 41 | 24M | 0 | Lock-free CAS rate limiting |
| **JSON** | 52 | 19M | 0 | Structured logging |
| **TypedFields** | 72 | 14M | 0 | Dynamic values (zero alloc) |
| **Parallel** | 85 | 11.8M | 0 | Concurrent (10 goroutines) |
| **slog.Handler** | 143 | 7.0M | 0 | slog adapter (zero alloc) |

### Sync Mode (Audit Trails)

| Tier | ns/op | logs/s | Survives | Strategy |
|------|-------|--------|----------|----------|
| **OSBuffered** | 111 | 9.0M | Process crash | Shared buffer + brief mutex |
| **Direct** | 1566 | 639K | Process crash | One `write()` syscall (O_APPEND) |
| **FsyncEveryN** | 5000 | 200K | OS crash | `fsync()` every N logs |
| **FsyncEveryWrite** | 4.4ms | 227 | OS crash | `fsync()` per entry (audit-grade) |

> **Honest note:** `loggerj` is the only logger that **explicitly documents** durability guarantees. Zap and zerolog default to OS-buffered writes but don't state it. For audit trails, choose `FsyncEveryN` or `FsyncEveryWrite`.

### Rate Limiting Under Contention

v1.4.0 added bounded CAS backoff (`runtime.Gosched()`) to prevent CPU burning under extreme contention:

| Scenario | ns/op | Behavior |
|----------|-------|----------|
| **Uncontended** | 2.5 | Pure CAS, no backoff triggered |
| **Saturated (over-limit)** | 0.3 | Fast-path rejection before CAS |
| **High Contention** | 213-257 | Bounded backoff prevents CPU spin |

### vs Industry Standards

| System | logs/s | Allocs | Model |
|--------|--------|--------|-------|
| **loggerj (async)** | **~19M** | **0** | Lock-free async |
| **loggerj (sync)** | **~9M** | **0** | OSBuffered tier |
| zerolog | ~3.5M | 0-1 | Sync |
| zap | ~2.5M | 1-2 | Sync |
| slog | ~1.5M | 3-8 | Sync |
| logrus | ~0.5M | 10+ | Sync |

See [COMPARISON.md](COMPARISON.md) for detailed feature matrix and decision guide.

## Key Concepts

### The SubProfile Paradigm

Define rate limits, sampling, and static fields **once at startup** via `RegisterSub()`. The hot path performs zero map lookups and zero mutex locks.

```go
// ✅ CORRECT: Define rules once, log cleanly forever.
logger.RegisterSub("AUTH", loggerj.WithRateLimit(50, time.Second))
logger.InfoString("AUTH", "login attempt", "user", "admin")

// ❌ INCORRECT: The Log method does not accept dynamic rate limits.
// logger.Log(LevelInfo, "AUTH", []byte("msg"), 50, nil) // API removed.
```

**Rate Limit Exact Counting:** v1.4.0 uses a packed atomic state (40-bit window index + 24-bit counter). Exact per-window counting supports limits up to **16,777,215**. Larger configured limits are capped to this value.

### Typed Field API (Zero-Allocation)

Avoid `strconv.Itoa` / `fmt.Sprintf` allocations with typed constructors:

```go
status := 200  // dynamic value
latency := 150 * time.Millisecond

logger.InfoFields("HTTP", []byte("request"),
    loggerj.Int("status", status),    // 0 allocs
    loggerj.Dur("latency", latency),  // 0 allocs
    loggerj.Bool("cached", true),     // 0 allocs
)
```

**Benchmark:** `BenchmarkTypedFields_Dynamic`: **72 ns/op, 0 allocs/op**

The `Field` struct is 48 bytes, passed by value, with `Num uint64` holding int64/uint64/float64-bits/duration-ns/bool as a tagged union — no interface boxing, no heap escape.

### slog.Handler Adapter

Route `slog` calls through loggerj's zero-allocation pipeline:

```go
logger := loggerj.NewLogger(loggerj.Config{JSONOutput: true})
go logger.Start(ctx)

handler := loggerj.NewSlogHandler(logger, "APP")
slogger := slog.New(handler)

slogger.Info("request", "method", "GET", "status", 200)
// Output: {"ts":...,"level":"INFO","type":"APP","msg":"request","fields":{"method":"GET","status":200}}
```

**Group Handling:** Nested `slog.Group` values are flattened to dotted keys (e.g., `"http.method":"GET"`). This preserves the data while maintaining loggerj's zero-alloc Field API. Modern log pipelines (Loki, Elasticsearch, Datadog) handle dotted keys efficiently.

**Benchmark:** `BenchmarkSlogHandler_Handle`: **143 ns/op, 0 allocs/op**

### Sync Mode (Audit Trails)

Bypass the async pipeline for per-log write guarantees:

```go
logger := loggerj.NewLogger(loggerj.Config{
    SyncMode:       true,
    OutputFile:     "/var/log/audit.log",
    DurabilityTier: loggerj.FsyncEveryWrite, // Maximum durability
})

// No Start() needed — sync mode writes directly
logger.InfoString("AUDIT", "user login", "user_id", "12345")
logger.Close()
```

See [EXAMPLES.md](EXAMPLES.md) for durability tier decision matrix.

## Design Decisions

We believe in radical transparency. Here's what `loggerj` intentionally does and does not do:

### What We Do

- **Lock-free hot path** — zero mutex locks, zero map lookups, zero heap allocations
- **Deterministic rate limiting** — packed atomic state with bounded CAS backoff
- **Explicit durability tiers** — 4 sync-mode tiers with documented crash-survival guarantees
- **slog compatibility** — zero-alloc adapter with dotted-key group flattening
- **Fuzz-tested correctness** — JSON escaping, duration formatting, rate-limit window calculation

### What We Don't Do (And Why)

- **No nested JSON objects** — `slog.Group` values are flattened to dotted keys (e.g., `"http.method":"GET"`). True nested JSON would require `Field` struct changes that either allocate (variadic slices) or bloat memory bandwidth (inline arrays). Dotted keys work well with modern log pipelines and preserve zero-alloc guarantees.

- **No coarse-time cache** — Rate limiting uses `time.Now().UnixMilli()` (~34ns on Apple M1 Pro) instead of an async-updated coarse timestamp. This ensures exact window boundaries for sub-second rate limits. The 34ns cost is ~97% of the rate-limit hot path but is accepted for correctness. At ~35ns/op, rate limiting supports ~28.5M checks/second — well above typical channel throughput.

- **No dynamic rate limiting per call** — rate limits are bound to `SubProfile` at init-time. This eliminates hot-path map lookups and mutex locks.

- **Async log ordering is approximate** — under extreme load, logs may write in slightly different order than generated. For strict ordering, use **Sync Mode**.

- **`WithCaller` allocates** — Go's `runtime.Caller` limitation. Adds ~460ns + 2 allocs per log. Disable in production for maximum performance.

See [COMPARISON.md](COMPARISON.md) for full trade-off analysis.

## Testing & Validation

v1.4.0 introduced comprehensive validation infrastructure:

### CI Benchmark Gate

Every PR runs protected hot-path benchmarks via `benchstat`. Any regression >10% blocks merge:

```bash
# Local benchmark gate
make bench-base          # Generate baseline
make bench-hot           # Run protected benchmarks
go run ./tools/benchgate -baseline=bench/base.txt -candidate=bench/pr.txt
```

### Fuzz Testing

Three fuzz targets verify correctness under adversarial input:

```bash
# Smoke test (30s per target)
go test -fuzz=FuzzAppendJSONString -fuzztime=30s .
go test -fuzz=FuzzAppendDuration -fuzztime=30s .
go test -fuzz=FuzzRateLimitWindow -fuzztime=30s .

# Long fuzz (scheduled CI, 60min per target)
make fuzz-long
```

### Go Version Matrix

Tested on Go 1.21, 1.22, 1.23, and 1.24. The `unsafeStringToBytes` implementation (`unsafe.StringData`) is sensitive to compiler changes; the matrix catches per-version regressions.

```bash
# Run all tests with race detector
go test -race -v ./...

# Run benchmarks
go test -bench=. -benchmem ./...

# CPU Profiling
go test -bench=. -cpuprofile=cpu.out ./...
go tool pprof -http=:8080 cpu.out
```

## Documentation

- **[EXAMPLES.md](EXAMPLES.md)** — Comprehensive examples for all features
  - SubProfile paradigm, typed fields, sync mode, HTTP middleware, context integration
  - Production configurations, buffer tuning, custom writers, slog adapter
- **[BENCH.md](BENCH.md)** — Detailed benchmark methodology and results
  - Async/sync mode performance, typed fields, profile lookup adaptive strategy
  - Rate-limit cost breakdown, HighContention analysis, slog.Handler adapter
  - vs industry standards (zap, zerolog, slog, logrus)
- **[COMPARISON.md](COMPARISON.md)** — Honest comparison with zap, zerolog, slog, logrus
  - Feature matrix, performance tables, decision guide, migration examples
  - Why loggerj doesn't support nested JSON objects or coarse-time cache
- **[ROADMAP.md](ROADMAP.md)** — v1.3.1 ✅, v1.4.0 ✅, v1.5.0 🔮 vision
- **[CHANGELOG.md](CHANGELOG.md)** — Release notes and migration guides

## Production Readiness

### Safe to Adopt

- ✅ Async high-throughput application logging
- ✅ JSON/text output with typed fields
- ✅ Rate limiting within documented limits (≤16,777,215 per window)
- ✅ Sampling (1-out-of-N, lock-free atomic counter)
- ✅ slog adapter for flattened group semantics
- ✅ Log rotation with backup retention
- ✅ Context-aware trace/request/span ID extraction

### Wait If You Need

- ⏸️ True nested JSON objects (dotted keys are the current design)
- ⏸️ Strict no-loss guarantee in async mode (use sync mode for audit trails)
- ⏸️ Full slog semantic nesting (groups are flattened to dotted keys)
- ⏸️ Caller info at maximum throughput (`WithCaller` adds ~460ns + 2 allocs)
- ⏸️ Extremely high exact rate limits above 16,777,215 (packed-state cap)

### Monitoring

Poll `Stats()` for observability:

```go
stats := logger.Stats()
// stats.Drops           — entries dropped due to full channel
// stats.ChannelSize     — current channel occupancy
// stats.ChannelCap      — channel capacity
// stats.SyncWriteErrors — failed write() calls in sync mode
```

For sync mode, monitor `SyncWriteErrors`. A non-zero value means at least one log entry was lost despite the durability tier.

## License

MIT License

---

**Acknowledgments*

This package was developed with architectural guidance from Qwen AI. Core engineering decisions, trade-off analyses, and performance optimizations were driven by rigorous benchmarking and production engineering principles.
