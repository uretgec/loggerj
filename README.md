<p align="center">
  <img src="https://img.shields.io/badge/license-MIT-blue?style=for-the-badge" alt="License: MIT">
  <img src="https://img.shields.io/badge/Go-1.21+-00ADD8?style=for-the-badge&logo=go" alt="Go Version">
  <img src="https://img.shields.io/badge/version-v1.5.2-green?style=for-the-badge" alt="Version: 1.5.2">
</p>

# loggerj

High-performance, zero-allocation asynchronous logging for Go.

`loggerj` uses a **Pre-Compiled Execution Profile** architecture. Rate limits, sampling, and static fields are baked into memory at startup. The hot path consists of atomic operations, memory copies and one channel send: no loggerj-level mutex and no heap allocations in the documented APIs. (The channel send itself takes the Go runtime's channel lock.)

## Features

- **Asynchronous Pipeline**: Non-blocking channel + dedicated worker goroutine.
- **Zero-Allocation Typed Fields**: `Int()`, `Bool()`, `Dur()` without interface boxing.
- **Lock-Free Rate Limiting & Sampling**: `atomic.CompareAndSwap` (CAS) with bounded backoff for rate limiting, an atomic counter for sampling. Both run before the sync/async split, so they apply in both modes. Since v1.5.0 sampling runs before the rate limit (see `WithRateLimit`).
- **Pre-baked SubProfiles**: Static fields formatted once at init, injected via `memcpy` at log time.
- **Sync Mode with Durability Tiers**: 4 explicit tiers. Only `Direct` and `FsyncEveryWrite` survive a process crash for every entry; `OSBuffered` and `FsyncEveryN` keep entries in a process-memory buffer (see the table below). `Direct` and `FsyncEveryWrite` take no loggerj mutex; `OSBuffered` and `FsyncEveryN` hold a mutex around the shared `bufio.Writer` during buffer copy AND flush/fsync.
- **Native log rotation** — size-based with backup retention, zero external dependencies, async mode only (ignored in Sync Mode). No time-based rotation, no compression. For daily rotation or gzip, use lumberjack via `StartWithWriter()`.
- **slog.Handler Adapter**: Routes standard `log/slog` calls through loggerj's zero-alloc pipeline; trace/request/span IDs from the context are added.
- **Simple lifecycle**: `StartBackground(ctx)` + `defer Close()`. `Close()` drains the queue, stops the worker and closes the file.
- **SubLogger handles**: `logger.Sub("HTTP", opts...)` returns a handle bound to a profile, so a typo in the log type cannot silently drop the profile's rate limit, sampling and static fields. It is a safety feature, not a speed-up.
- **Overload visibility**: `Stats().Drops` and `Stats().DropsByLevel`; optional bounded wait for ERROR entries (`ErrorBlockTimeout`).
- **Output options**: numeric JSON durations (`DurationUnit`), formatted timestamps (`TimestampFormat`), log file permissions (`FileMode`).
- **Safe text output**: control characters in messages and values are escaped, so a value cannot start a new log line.

## Status and scope

loggerj is a small library with one maintainer. From v1.5.2 its API and internal design are considered frozen: planned work is limited to bug fixes, support for current Go versions and documentation. New features are added only when a real workload needs them (see ROADMAP.md).

It is built for services that log from many goroutines, want a low and bounded cost on the calling goroutine, and accept dropping entries over blocking the caller. `SyncMode` is for the cases that need a documented durability tier. It is not a general logging framework: there are no hooks, no nested JSON objects, no network shipping and no time-based rotation. It has been benchmarked on Linux and macOS; Windows is only covered by the CI compatibility job.

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
    logger.StartBackground(context.Background())
    defer logger.Close() // drains queued entries, stops the worker, closes the file

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

## Output format

JSON output is one object per line:

```json
{"ts":1704067200123,"level":"INFO","type":"HTTP","env":"prod","msg":"request","fields":{"status":200,"latency":"150ms"}}
```

- `ts`, `level`, `type` and `msg` are always present. `file` and `line` are added with `IncludeCaller`.
- Fields from `WithFields` are top-level keys (baked at registration). Fields passed per call are nested under `"fields"`. `WithFields` keys named `ts`, `level`, `type`, `msg`, `file`, `line` or `fields` are reserved and ignored with a warning.
- Dotted keys are used instead of nested objects (`"http.method"`); see COMPARISON.md.
- `ts` is Unix milliseconds unless `TimestampFormat` is set. In async mode it is taken by the worker, once per batch of queued entries, not when the log call is made.

## Upgrading from v1.4.x

v1.5.0 changes some behavior; see CHANGELOG.md for details.

- **Gate order:** sampling now runs before the rate limit, so the same `WithSampleRate` + `WithRateLimit` configuration writes more. Use `WithRateLimit(limit/sampleRate, window)` to keep the old volume.
- **Shutdown:** `Close()` now stops the worker. `Flush(); cancel(); Close()` still works; `StartBackground` + `defer Close()` is enough.
- **`Flush()` without a running worker** no longer discards queued entries.
- **Text output** escapes control characters; parsers that relied on raw newlines inside values will see `\n`.
- **`WithFields` keys** that collide with the JSON keys listed above are ignored.

## Performance & Trade-offs

`loggerj` is optimized for **async throughput** and **explicit durability guarantees**.

- **Async Mode**: Designed for high-QPS microservices, gateways, and proxies where GC pauses from logging must be avoided. The async ns/op figures in BENCH.md are caller-side costs of an attempt; check `drops/op` and the `BenchmarkE2E_*` results for delivered throughput.
- **Sync Mode**: For cases that need a documented durability tier. Each `DurabilityTier` states what it survives (table above).

For detailed ns/op metrics, hardware benchmarks, and fair apples-to-apples comparisons with `zap`, `zerolog`, and `slog`, see **[BENCH.md](docs/BENCH.md)**.
For architectural trade-offs, missing features (like nested JSON objects), and migration guides, see **[COMPARISON.md](docs/COMPARISON.md)**.

## Known limitations

- **Async mode can lose entries.** On a crash or kill, entries in the queue and in the worker's buffer are lost, and entries are dropped (and counted) when the queue is full. Use `SyncMode` with `Direct` or `FsyncEveryWrite` when that is not acceptable.
- **String arguments are read later in async mode.** The worker reads the strings of a log call after the call returned. Do not pass strings that alias a buffer the caller reuses (fasthttp and Fiber values such as `c.Path()`, unsafe conversions of byte slices); copy them first, for example with `strings.Clone`. `SyncMode` reads them before the call returns.
- **Memory is bounded by the queue, not by the message size.** A queued entry holds a copy of its message, so a full queue holds about `ChannelSize` times the message size. There is no message size limit.
- **Timestamps are taken by the worker** (once per batch), not when the log call is made. Under backpressure they can lag the event. `SyncMode` stamps at call time.
- **Throughput of one worker is limited.** About 3 to 5 million entries per second on the machines measured (see BENCH.md); above that, entries are dropped.
- **Text output does not quote values** that contain spaces, so parsing `key=value` for such values is ambiguous. Control characters are escaped. Use `JSONOutput` for machine parsing.
- **The rate limit uses a fixed window**, so up to twice the limit can pass around a window boundary.
- **One process should own a log file.** Rotation renames the file; several processes writing and rotating the same file will conflict. Rotation is size-based and async-mode only.
- **Write failures** (a full disk, a broken connection) are counted in `Stats().WriteErrors` and printed to stderr at most once per 10 seconds; the entries of the failed batch are lost, and the worker writes again when the writer recovers.

## Documentation

- **[EXAMPLES.md](docs/EXAMPLES.md)** — Comprehensive examples for all features.
- **[BENCH.md](docs/BENCH.md)** — Detailed benchmark methodology and results.
- **[COMPARISON.md](docs/COMPARISON.md)** — Honest comparison with zap, zerolog, slog, logrus.
- **[ROADMAP.md](docs/ROADMAP.md)** — Future vision and development roadmap.
- **[CHANGELOG.md](CHANGELOG.md)** — Release notes and migration guides.

## License

MIT License
