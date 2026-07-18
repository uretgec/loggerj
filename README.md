<p align="center">
  <img src="https://img.shields.io/badge/license-MIT-blue?style=for-the-badge" alt="License: MIT">
  <img src="https://img.shields.io/badge/Go-1.21+-00ADD8?style=for-the-badge&logo=go" alt="Go Version">
  <img src="https://img.shields.io/badge/Built%20with-Qwen%20AI-blue?style=for-the-badge" alt="Built with Qwen AI">
</p>

# loggerj

Ultra-high-performance, lock-free, asynchronous logging for Go. Designed for extreme throughput with **zero heap allocations** in the hot path.

## Philosophy

Most loggers sacrifice performance for convenience. `loggerj` takes a different approach: **The "Pre-Compiled Execution Profile" architecture.**

Instead of evaluating rate limits, sampling rules, or formatting static fields on every single log call (which causes mutex contention and allocations), `loggerj` bakes these rules into memory once during initialization. The hot path consists solely of atomic operations and memory copies (`memcpy`), ensuring the garbage collector is never disturbed by your logging.

## Features

- 🚀 **100% Zero-Allocation Hot Path:** No interface boxing, no hidden `strconv` calls, no map lookups during logging.
- ⚡ **Lock-Free Rate Limiting & Sampling:** Powered by `atomic.CompareAndSwap` (CAS). No shards, no mutexes, no contention.
- 🧠 **Pre-Baked SubProfiles:** Static fields (e.g., `env=prod`) are formatted into `[]byte` once at startup. Zero CPU cost at log time.
- 🛡️ **Non-Blocking & Drop-Monitored:** Channel-based async architecture. If overloaded, it safely drops logs and increments an atomic counter instead of deadlocking your app.
- 🔄 **Native Log Rotation:** Size-based rotation with backup retention, no external dependencies (like `lumberjack`) required.
- 🎛️ **Runtime Level Control:** Atomically change log levels on the fly with ~2ns overhead.
- Standard Library Compatibility: Seamlessly intercept std log and third-party library logs via io.Writer adapter (logger.AsWriter()).

---

## Quick Start

```go
package main

import (
    "context"
    "time"
    "uretgec/internal/loggerj"
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

    // 5. Ensure all logs are written before exit
    logger.Flush()
}
```

---

## The SubProfile Paradigm

In `loggerj`, you do not pass rate limits or sampling values during the log call. Instead, you define **SubProfiles** at initialization. This is the key to our lock-free performance.

```go
// ✅ CORRECT: Define rules once, log cleanly forever.
logger.RegisterSub("AUTH", loggerj.WithRateLimit(50, time.Second))
logger.InfoString("AUTH", "login attempt", "user", "admin")

// ❌ INCORRECT: The Log method does not accept dynamic rate limits anymore.
// logger.Log(LevelInfo, "AUTH", []byte("msg"), 50, nil) // This API is removed.
```

---

## Performance

Benchmarks run on Apple M1 Pro. `loggerj` consistently outperforms industry standards by eliminating hot-path allocations and lock contention.

| Mode | ns/op | logs/s | Allocations | Use Case |
| :--- | :--- | :--- | :--- | :--- |
| **Filtered** | 2.1 | 484M | 0 | Debug logs in production |
| **RateLimited** | 43.3 | 23.0M | 0 | High-volume events (Lock-Free CAS) |
| **Parallel** | 102.1 | 9.7M | 0 | Concurrent logging (10+ goroutines) |
| **JSON** | 132.5 | 7.5M | 0 | Structured logging |
| **WithCaller** | 506.7 | 1.9M | 2 | Debugging only |

*Note: `WithCaller` is the only operation that allocates, as it is a fundamental limitation of Go's `runtime.Caller`.*

### vs Industry Standards (Approximate Max Throughput)

| System | logs/s | Allocations | Notes |
| :--- | :--- | :--- | :--- |
| **logrus** | ~500K | High | Reflection-based, sync |
| **zap** | ~2.5M | Low | Sync, requires manual field typing |
| **zerolog** | ~3.5M | Low | Sync, fluent API |
| **loggerj** | **~9.7M** | **Zero** | Async, lock-free, pre-compiled |

---

## Design Decisions & Limitations

We believe in radical transparency. Here is what `loggerj` intentionally does **not** do, and why:

1. **No `context.Context` Integration:**
   Extracting values from `ctx.Value()` is slow and breaks the zero-allocation guarantee. *Solution:* Extract TraceIDs/RequestIDs in your HTTP middleware and pass them as standard fields: `logger.Info("HTTP", msg, "trace_id", traceID)`.
2. **No Typed Fields (e.g., `zap.Int`, `slog.Any`):**
   Typed field helpers cause interface boxing allocations on the *caller* side. `loggerj` forces the caller to use `strconv.Itoa()` or `fmt.Sprintf()`. This keeps the logger's internal hot path strictly zero-allocation and makes the cost of formatting explicit to the developer.
3. **Async Log Ordering is Not Strictly Guaranteed:**
   Under extreme concurrent load, the order in which logs are written to disk may slightly differ from the order they were generated. *Solution:* For critical audit trails, call `logger.Flush()` immediately after the log, or use a synchronous logger for that specific path.
4. **No Dynamic Rate Limiting per Call:**
   Rate limits are bound to the `SubProfile` at init-time. This eliminates map lookups and mutex locks in the hot path, enabling true lock-free performance.

---

## Buffer Tuning Guide

`loggerj` is highly tunable. Adjust these based on your environment:

| Scenario | `ChannelSize` | `WorkerBufferSize` | `FlushThreshold` | `FlushTimeout` |
| :--- | :--- | :--- | :--- | :--- |
| **Low Memory** (512MB RAM) | 1024 | 2048 | 2048 | 100ms |
| **Balanced** (Default) | 4096 | 4096 | 4096 | 50ms |
| **High Throughput** | 16384 | 16384 | 16384 | 500ms |
| **Burst Traffic** | 32768 | 8192 | 8192 | 10ms |

---

## Output Formats

**Text (Default)*

```text
[1704067200123] INFO [HTTP] request received method=GET path=/api/v1/users env=prod service=gateway
```

**JSON*

```json
{"ts":1704067200123,"level":"INFO","type":"HTTP","msg":"request received","env":"prod","service":"gateway","fields":{"method":"GET","path":"/api/v1/users"}}
```

---

## Testing & Profiling

```bash
# Run all tests with race detector
go test -race -v ./internal/loggerj/...

# Run benchmarks
go test -bench=. -benchmem ./internal/loggerj/...

# CPU Profiling
go test -bench=. -cpuprofile=cpu.out ./internal/loggerj/...
go tool pprof -http=:8080 cpu.out
```

---

## Documentation

- [Usage Examples](EXAMPLES.md) - Comprehensive examples for all features
- [Benchmark Results](BENCH.md) - Detailed performance analysis

---

## License

MIT License

---

### 🤖 Acknowledgments

This package was developed with the architectural guidance, performance optimization, and code generation assistance of **Qwen AI**. The core engineering decisions, trade-off analyses, and domain expertise were driven by rigorous performance engineering principles to achieve true production-ready quality.
