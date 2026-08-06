<p align="center">
  <img src="https://img.shields.io/badge/license-MIT-blue?style=for-the-badge" alt="License: MIT">
  <img src="https://img.shields.io/badge/Go-1.21+-00ADD8?style=for-the-badge&logo=go" alt="Go Version">
  <img src="https://img.shields.io/badge/Built%20with-Qwen%20AI-blue?style=for-the-badge" alt="Built with Qwen AI">
</p>

# loggerj

Ultra-high-performance, lock-free, asynchronous logging for Go. **19M logs/s async, 9M logs/s sync, zero allocations.**

## Philosophy

Most loggers sacrifice performance for convenience. `loggerj` takes a different approach: the **"Pre-Compiled Execution Profile"** architecture bakes rate limits, sampling, and static fields into memory once at startup. The hot path is pure atomic operations and memory copies — zero mutex locks, zero heap allocations, zero GC pressure.

## Features

- 🚀 **19M logs/s async throughput** — lock-free channel + worker pipeline
- 🎯 **Zero-allocation typed fields** — `Int()`, `Bool()`, `Dur()` with no interface boxing
- ⚡ **Lock-free rate limiting & sampling** — `atomic.CompareAndSwap` (CAS), no mutex
- 🧠 **Pre-baked SubProfiles** — static fields formatted once at init, zero CPU at log time
- 📋 **Copy-on-write registry** — `atomic.Pointer` for lock-free reads, unlimited profiles
- 🛡️ **Non-blocking & drop-monitored** — safe drops with atomic counter + `SetOnDrop` callback
- 🔄 **Native log rotation** — size-based with backup retention, zero external dependencies
- ⏱️ **Sync mode with durability tiers** — 4 explicit tiers, 2.7x faster than zerolog sync
- 🎛️ **Runtime level control** — ~2ns atomic level changes
- 🔗 **Standard library compatible** — intercept `std log` via `io.Writer` adapter
- 🌐 **Context integration (opt-in)** — extract trace/request/span IDs with zero cost when unused
- ✅ **Deterministic flush** — `Flush()` drains channel before writing, no log loss

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

    // 6. Context-aware logging (opt-in)
    ctx = context.WithValue(ctx, loggerj.TraceIDKey, "abc-123")
    logger.InfoCtx(ctx, "HTTP", "traced request", "method", "POST")

    // 7. Ensure all logs are written before exit
    logger.Flush()
}
```

## Performance Highlights

Benchmarks on Apple M1 Pro (10 cores), Go 1.21+. See [BENCH.md](BENCH.md) for full results.

### Async Mode (Intended Usage)

| Mode | ns/op | logs/s | Allocs | Use Case |
|------|-------|--------|--------|----------|
| **Filtered** | 2.1 | 483M | 0 | Debug logs in production |
| **RateLimited** | 41 | 24M | 0 | Lock-free CAS rate limiting |
| **JSON** | 52 | 19M | 0 | Structured logging |
| **TypedFields** | 72 | 14M | 0 | Dynamic values (zero alloc) |
| **Parallel** | 85 | 11.8M | 0 | Concurrent (10 goroutines) |

### Sync Mode (Audit Trails)

| Tier | ns/op | logs/s | Survives | Strategy |
|------|-------|--------|----------|----------|
| **OSBuffered** | 111 | 9.0M | Process crash | Shared buffer + brief mutex |
| **Direct** | 1566 | 639K | Process crash | One `write()` syscall (O_APPEND) |
| **FsyncEveryN** | 5000 | 200K | OS crash | `fsync()` every N logs |
| **FsyncEveryWrite** | 4.4ms | 227 | OS crash | `fsync()` per entry (audit-grade) |

> **Honest note:** `loggerj` is the only logger that **explicitly documents** durability guarantees. Zap and zerolog default to OS-buffered writes but don't state it. For audit trails, choose `FsyncEveryN` or `FsyncEveryWrite`.

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

- **Async log ordering is approximate** — under extreme load, logs may write in slightly different order than generated. For strict ordering, use **Sync Mode**.
- **No dynamic rate limiting per call** — rate limits are bound to `SubProfile` at init-time. This eliminates hot-path map lookups and mutex locks.
- **Sync mode parallel performance** — `OSBuffered` uses a shared buffer with brief mutex (~112 ns → ~238 ns under parallel load). We chose shared buffer for throughput over per-goroutine pool (which loses data on `Close()`). Still 2-3x faster than zerolog/zap sync.
- **`WithCaller` allocates** — Go's `runtime.Caller` limitation. Adds ~460ns + 2 allocs per log. Disable in production.

See [COMPARISON.md](COMPARISON.md) for full trade-off analysis.

## Documentation

- **[EXAMPLES.md](EXAMPLES.md)** — Comprehensive examples for all features
  - SubProfile paradigm, typed fields, sync mode, HTTP middleware, context integration
  - Production configurations, buffer tuning, custom writers
- **[BENCH.md](BENCH.md)** — Detailed benchmark methodology and results
  - Async/sync mode performance, typed fields, profile lookup adaptive strategy
  - slog.Handler adapter, vs industry standards
- **[COMPARISON.md](COMPARISON.md)** — Honest comparison with zap, zerolog, slog, logrus
  - Feature matrix, performance tables, decision guide, migration examples
- **[ROADMAP.md](ROADMAP.md)** — v1.2.0 ✅, v1.3.0 ✅, v1.4.0 🔮 vision
- **[CHANGELOG.md](CHANGELOG.md)** — Release notes and migration guides

## Testing & Profiling

```bash
# Run all tests with race detector
go test -race -v ./...

# Run benchmarks
go test -bench=. -benchmem ./...

# CPU Profiling
go test -bench=. -cpuprofile=cpu.out ./...
go tool pprof -http=:8080 cpu.out
```

## License

MIT License

---

🤖 **Acknowledgments**

This package was developed with the architectural guidance, performance optimization, and code generation assistance of Qwen AI. The core engineering decisions, trade-off analyses, and domain expertise were driven by rigorous performance engineering principles to achieve true production-ready quality.
