<p align="center">
  <img src="https://img.shields.io/badge/license-MIT-blue?style=for-the-badge" alt="License: MIT">
  <img src="https://img.shields.io/badge/Go-1.21+-00ADD8?style=for-the-badge&logo=go" alt="Go Version">
  <img src="https://img.shields.io/badge/version-v1.4.4-green?style=for-the-badge" alt="Version: 1.4.4">
</p>

# loggerj

High-performance, zero-allocation asynchronous logging for Go.

`loggerj` uses a **Pre-Compiled Execution Profile** architecture. Rate limits, sampling, and static fields are baked into memory at startup. The hot path consists of atomic operations, memory copies and one channel send: no loggerj-level mutex and no heap allocations in the documented APIs. (The channel send itself takes the Go runtime's channel lock.)

## Features

- **Asynchronous Pipeline**: Non-blocking channel + dedicated worker goroutine.
- **Zero-Allocation Typed Fields**: `Int()`, `Bool()`, `Dur()` without interface boxing.
- **Lock-Free Rate Limiting & Sampling**: `atomic.CompareAndSwap` (CAS) with bounded backoff for rate limiting, an atomic counter for sampling. Both run before the sync/async split, so they apply in both modes. In v1.4.x the rate limit runs before sampling (see `WithRateLimit`).
- **Pre-baked SubProfiles**: Static fields formatted once at init, injected via `memcpy` at log time.
- **Sync Mode with Durability Tiers**: 4 explicit tiers. Only `Direct` and `FsyncEveryWrite` survive a process crash for every entry; `OSBuffered` and `FsyncEveryN` keep entries in a process-memory buffer (see the table below). `Direct` and `FsyncEveryWrite` take no loggerj mutex; `OSBuffered` and `FsyncEveryN` hold a mutex around the shared `bufio.Writer` during buffer copy AND flush/fsync.
- **Native log rotation** — size-based with backup retention, zero external dependencies, async mode only (ignored in Sync Mode). No time-based rotation, no compression. For daily rotation or gzip, use lumberjack via `StartWithWriter()`.
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
    go logger.Start(ctx)
    defer func() {
        logger.Flush() // drain the channel while the worker is still running
        cancel()       // stop the worker
        logger.Close() // close the file
    }()

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

## Durability at a glance

| Mode / tier | Survives process crash (panic, SIGKILL) | Survives OS crash / power loss |
|---|---|---|
| Async (default) | No: entries in the channel and the worker buffer (up to `FlushTimeout`) are lost | No |
| Sync `OSBuffered` | Only entries already flushed; there is no background flush timer | No |
| Sync `Direct` | Yes | No (page cache) |
| Sync `FsyncEveryN` | Entries up to the last fsync; the last N-1 are lost | Entries up to the last fsync |
| Sync `FsyncEveryWrite` | Yes | Yes, if the disk honors fsync |

In Sync Mode, `Flush()` and `Close()` flush the buffer of `OSBuffered` and `FsyncEveryN` (and fsync for `FsyncEveryN`). The process-crash column is verified by `TestDurability_ProcessKill`; OS crash behavior is not tested.

## Performance & Trade-offs

`loggerj` is optimized for **async throughput** and **explicit durability guarantees**.

- **Async Mode**: Designed for high-QPS microservices, gateways, and proxies where GC pauses from logging must be avoided. The async ns/op figures in BENCH.md are caller-side costs of an attempt; check `drops/op` and the `BenchmarkE2E_*` results for delivered throughput.
- **Sync Mode**: For cases that need a documented durability tier. Each `DurabilityTier` states what it survives (table above).

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
