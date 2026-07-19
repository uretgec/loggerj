<p align="center">
  <img src="https://img.shields.io/badge/license-MIT-blue?style=for-the-badge" alt="License: MIT">
  <img src="https://img.shields.io/badge/Go-1.21+-00ADD8?style=for-the-badge&logo=go" alt="Go Version">
  <img src="https://img.shields.io/badge/Built%20with-Qwen%20AI-blue?style=for-the-badge" alt="Built with Qwen AI">
</p>

# loggerj

Ultra-high-performance, lock-free, asynchronous logging for Go. Designed for extreme throughput with **zero heap allocations** in the hot path.

## Philosophy

Most loggers sacrifice performance for convenience. `loggerj` takes a different approach: The **"Pre-Compiled Execution Profile"** architecture.

Instead of evaluating rate limits, sampling rules, or formatting static fields on every single log call (which causes mutex contention and allocations), `loggerj` bakes these rules into memory once during initialization. The hot path consists solely of atomic operations and memory copies (`memcpy`), ensuring the garbage collector is never disturbed by your logging.

## Features

- 🚀 **100% Zero-Allocation Hot Path**: No interface boxing, no hidden `strconv` calls, no map lookups during logging. String-to-byte conversion uses `unsafe` zero-copy (Go 1.21+).
- ⚡ **Lock-Free Rate Limiting & Sampling**: Powered by `atomic.CompareAndSwap` (CAS). No shards, no mutexes, no contention.
- 🧠 **Pre-Baked SubProfiles**: Static fields (e.g., `env=prod`) are formatted into `[]byte` once at startup. Zero CPU cost at log time.
- 📋 **Copy-on-Write Profile Registry**: Profiles are stored in an immutable registry accessed via `atomic.Pointer`. Unlimited profiles, zero interface boxing, lock-free reads.
- 🛡️ **Non-Blocking & Drop-Monitored**: Channel-based async architecture. If overloaded, it safely drops logs and increments an atomic counter instead of deadlocking your app. Optional `SetOnDrop` callback for real-time monitoring.
- 🔄 **Native Log Rotation**: Size-based rotation with backup retention, no external dependencies (like `lumberjack`) required.
- 🎛️ **Runtime Level Control**: Atomically change log levels on the fly with ~2ns overhead.
- 🔗 **Standard Library Compatibility**: Seamlessly intercept `std log` and third-party library logs via `io.Writer` adapter (`logger.AsWriter()`).
- 🌐 **Context Integration (Opt-in)**: Extract `trace_id`, `request_id`, `span_id` from `context.Context` with zero cost when unused.
- ⏱️ **Worker-Side Timestamps**: Timestamps are captured by the worker goroutine at format time, removing a vDSO syscall from the hot path.
- ✅ **Deterministic Flush**: `Flush()` drains all pending channel entries before writing, guaranteeing no log loss on explicit flush.

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

    // 2. Define SubProfiles (COLD PATH: Do this once at startup)
    // Rules are baked into memory. No hot-path overhead.
    logger.RegisterSub("HTTP",
        loggerj.WithRateLimit(1000, time.Second),
        loggerj.WithFields("env", "prod", "service", "gateway"),
    )
    logger.RegisterSub("DB",
        loggerj.WithSampleRate(100), // Log 1 out of 100
    )

    // 3. Start the async worker
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    go logger.Start(ctx)
    defer logger.Close()

    // 4. Log messages (HOT PATH: Ultra-fast, zero allocation)
    logger.InfoString("HTTP", "request received", "method", "GET", "path", "/api/v1/users")
    logger.ErrorString("DB", "connection timeout", "host", "localhost", "err", "dial tcp: i/o timeout")

    // 5. Context-aware logging (opt-in, zero cost when unused)
    ctx = context.WithValue(ctx, loggerj.TraceIDKey, "abc-123")
    logger.InfoCtx(ctx, "HTTP", "traced request", "method", "POST")

    // 6. Ensure all logs are written before exit
    logger.Flush()
}
```

## The SubProfile Paradigm

In `loggerj`, you do not pass rate limits or sampling values during the log call. Instead, you define SubProfiles at initialization. This is the key to our lock-free performance.

```go
// ✅ CORRECT: Define rules once, log cleanly forever.
logger.RegisterSub("AUTH", loggerj.WithRateLimit(50, time.Second))
logger.InfoString("AUTH", "login attempt", "user", "admin")

// ❌ INCORRECT: The Log method does not accept dynamic rate limits anymore.
// logger.Log(LevelInfo, "AUTH", []byte("msg"), 50, nil) // This API is removed.
```

## Performance

Benchmarks run on Apple M1 Pro (10 cores), Go 1.21+. `loggerj` consistently outperforms industry standards by eliminating hot-path allocations and lock contention.

| Mode | ns/op | logs/s | Allocs/op | Use Case |
|------|-------|--------|-----------|----------|
| Filtered | 2.1 | 483M | 0 | Debug logs in production |
| Sampling | 24 | 41M | 0 | High-volume sampled events |
| Dropped | 19 | 53M | 0 | Channel-full backpressure |
| RateLimited | 41 | 24M | 0 | High-volume events (Lock-Free CAS) |
| JSON | 55 | 18M | 0 | Structured logging |
| StringAPI | 63 | 16M | 0 | String messages (zero-copy) |
| NoFields | 61 | 16M | 0 | Simple messages |
| Parallel | 80 | 12.5M | 0 | Concurrent logging (10+ goroutines) |
| SubProfile Prefix | 55 | 18M | 0 | Pre-baked static fields |
| WithCaller | 463 | 2.2M | 2 | Debugging only |
| SyncEquivalent | 1082 | 924K | 3 | Fair comparison with sync loggers |

> **Note**: `WithCaller` allocates due to Go's `runtime.Caller` — a fundamental limitation. `SyncEquivalent` forces `Flush()` after every log to simulate synchronous behavior; this is NOT the intended usage pattern.

### vs Industry Standards (Approximate Max Throughput)

| System | logs/s | Allocs/op | Notes |
|--------|--------|-----------|-------|
| logrus | ~500K | High | Reflection-based, sync |
| zap | ~2.5M | Low | Sync, requires manual field typing |
| zerolog | ~3.5M | Low | Sync, fluent API |
| **loggerj (async)** | **~16M** | **Zero** | Async, lock-free, pre-compiled |
| loggerj (sync-equiv) | ~924K | 3 | Forced Flush() per log (not intended usage) |

## Design Decisions & Limitations

We believe in radical transparency. Here is what `loggerj` intentionally does and does not do, and why:

### Context Integration (Opt-in)

`loggerj` provides `InfoCtx`, `DebugCtx`, `WarnCtx`, `ErrorCtx` methods that extract known keys (`TraceIDKey`, `RequestIDKey`, `SpanIDKey`) from `context.Context`. This is **opt-in**: if you never call `*Ctx` methods, there is zero overhead. When used, only known string keys are extracted — no reflection, no `fmt.Sprintf`.

```go
ctx = context.WithValue(ctx, loggerj.TraceIDKey, "abc-123")
logger.InfoCtx(ctx, "HTTP", "request", "method", "GET")
// Output includes: "trace_id":"abc-123"
```

### No Typed Fields (e.g., `zap.Int`, `slog.Any`)

Typed field helpers cause interface boxing allocations on the caller side. `loggerj` forces the caller to use `strconv.Itoa()` or `fmt.Sprintf()`. This keeps the logger's internal hot path strictly zero-allocation and makes the cost of formatting explicit to the developer.

### Async Log Ordering is Not Strictly Guaranteed

Under extreme concurrent load, the order in which logs are written to disk may slightly differ from the order they were generated. Timestamps are captured at format time (worker-side), not at call time. For critical audit trails, call `logger.Flush()` immediately after the log.

### No Dynamic Rate Limiting per Call

Rate limits are bound to the `SubProfile` at init-time. This eliminates map lookups and mutex locks in the hot path, enabling true lock-free performance.

## Buffer Tuning Guide

`loggerj` is highly tunable. Adjust these based on your environment:

| Scenario | ChannelSize | WorkerBufferSize | FlushThreshold | FlushTimeout |
|----------|-------------|------------------|----------------|--------------|
| Low Memory (512MB RAM) | 1024 | 2048 | 2048 | 100ms |
| Balanced (Default) | 4096 | 4096 | 4096 | 50ms |
| High Throughput | 16384 | 16384 | 16384 | 500ms |
| Burst Traffic | 32768 | 8192 | 8192 | 10ms |

## Output Formats

### Text (Default)

```txt
[1704067200123] INFO [HTTP] request received method=GET path=/api/v1/users env=prod service=gateway
```

### JSON

```json
{"ts":1704067200123,"level":"INFO","type":"HTTP","msg":"request received","env":"prod","service":"gateway","fields":{"method":"GET","path":"/api/v1/users"}}
```

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

## Documentation

- [Usage Examples](EXAMPLES.md) — Comprehensive examples for all features
- [Benchmark Results](BENCH.md) — Detailed performance analysis

## License

MIT License

---

🤖 **Acknowledgments**

This package was developed with the architectural guidance, performance optimization, and code generation assistance of Qwen AI. The core engineering decisions, trade-off analyses, and domain expertise were driven by rigorous performance engineering principles to achieve true production-ready quality.
