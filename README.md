<p align="center">
  <img src="https://img.shields.io/badge/license-MIT-blue?style=for-the-badge" alt="License: MIT">
  <img src="https://img.shields.io/badge/Go-1.21+-00ADD8?style=for-the-badge&logo=go" alt="Go Version">
  <img src="https://img.shields.io/badge/version-v1.4.3-green?style=for-the-badge" alt="Version: 1.4.3">
</p>

# loggerj

High-performance, zero-allocation asynchronous logging for Go.

`loggerj` uses a **Pre-Compiled Execution Profile** architecture. Rate limits, sampling, and static fields are baked into memory at startup. The hot path consists purely of atomic operations and memory copies — zero mutex locks, zero heap allocations, zero GC pressure.

## Features

- **Asynchronous Pipeline**: Non-blocking channel + dedicated worker goroutine.
- **Zero-Allocation Typed Fields**: `Int()`, `Bool()`, `Dur()` without interface boxing.
- **Lock-Free Rate Limiting & Sampling**: `atomic.CompareAndSwap` (CAS) with bounded backoff. Applies to the async hot path.
- **Pre-baked SubProfiles**: Static fields formatted once at init, injected via `memcpy` at log time.
- **Sync Mode with Durability Tiers**: 4 explicit tiers for audit trails. Direct and FsyncEveryWrite are lock-free (O_APPEND atomic); OSBuffered and FsyncEveryN hold a mutex around the shared `bufio.Writer` during buffer copy AND flush/fsync.
- **Native log rotation** — size-based with backup retention, zero external dependencies. No time-based rotation, no compression. For daily rotation or gzip, use lumberjack via `StartWithWriter()`.
- **slog.Handler Adapter**: Routes standard `log/slog` calls through loggerj's zero-alloc pipeline.

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

    // 4. Log messages (HOT PATH: zero allocation)
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
    // slogger := slog.New(loggerj.NewSlogHandler(logger, "APP"))
    
    // 7. Ensure all logs are written before exit
    logger.Flush()
}
```

## Standard Library Integration

Intercept logs from Go's standard `log` package and third-party libraries:

```go
log.SetFlags(0) // Disable std log timestamps; loggerj adds its own
log.SetOutput(logger.AsWriter(loggerj.LevelInfo, "STDLIB"))
log.Println("This message flows through loggerj's async pipeline")
```

*Note: The `AsWriter` adapter is zero-allocation on **Go 1.22+**. On Go 1.21, it shows 1 alloc/op due to compiler escape analysis limitations across the `io.Writer` interface boundary. The native `InfoString`/`InfoFields` APIs are always zero-alloc.*

## Performance & Trade-offs

`loggerj` is optimized for **async throughput** and **explicit durability guarantees**.

- **Async Mode**: Designed for high-QPS microservices, gateways, and proxies where log volume exceeds 1M logs/s and GC pauses must be avoided.
- **Sync Mode**: Designed for audit trails and financial logs. Unlike other loggers that implicitly rely on OS page cache, `loggerj` explicitly documents crash-survival guarantees via `DurabilityTier`.

For detailed ns/op metrics, hardware benchmarks, and fair apples-to-apples comparisons with `zap`, `zerolog`, and `slog`, see **[BENCH.md](BENCH.md)**.
For architectural trade-offs, missing features (like nested JSON objects), and migration guides, see **[COMPARISON.md](COMPARISON.md)**.

## Documentation

- **[EXAMPLES.md](EXAMPLES.md)** — Comprehensive examples for all features.
- **[BENCH.md](BENCH.md)** — Detailed benchmark methodology and results.
- **[COMPARISON.md](COMPARISON.md)** — Honest comparison with zap, zerolog, slog, logrus.
- **[ROADMAP.md](ROADMAP.md)** — Future vision and development roadmap.
- **[CHANGELOG.md](CHANGELOG.md)** — Release notes and migration guides.

## License

MIT License
