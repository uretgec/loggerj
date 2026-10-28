# loggerj vs. the Go Logging Ecosystem

An engineering comparison. We show where `loggerj` wins, where it loses, and the architectural trade-offs we made to achieve zero-allocation hot paths.

*Note: Competitor numbers are approximate values taken from public benchmarks. Treat the order of magnitude as meaningful.*

---

## Executive Summary

- **High-throughput async services**: the async API has a low caller-side cost (see BENCH.md), but those numbers measure enqueueing an entry, not delivered logs. Competitor numbers include formatting and the write, so the two are not directly comparable. Run `BenchmarkE2E_*` (delivered throughput and `drops/op`) and your own workload before choosing.
- **Sync / audit-critical logging**: `SyncMode` documents what each tier survives. `OSBuffered` is fast (~111 ns/op) because it does not issue a syscall per entry, so it does NOT survive a process crash for unflushed entries. The like-for-like comparison with zerolog/zap writing to a file is the `Direct` tier (~1.5 µs/op).
- **Structured logging**: `loggerj`'s `Field` API gives **0 allocs/op** for dynamic values, matching zap and beating slog variadic call sites.
- **slog ecosystem**: Native `slog.Handler` adapter routes calls through loggerj's pipeline (~143 ns/op, 0 allocs). Groups are flattened to dotted keys.
- **logrus**: `loggerj` wins everywhere; `logrus` is in maintenance mode.
- **No configurable gate order (sampling vs rate limit).** In v1.4.x the rate limit runs before sampling. With both set, the rate limit counts entries before sampling, so the output is roughly `limit / sampleRate` per window, and every call pays one clock read (~34 ns) because the rate limiter runs first. v1.5.0 is planned to run sampling first (a behavior change); see ROADMAP.md.

---

## Feature Matrix

| Feature | loggerj | zap | zerolog | slog | logrus |
|---|---|---|---|---|---|
| **Zero-alloc hot path** | ✅ Real (0 allocs) | ⚠️ Partial | ✅ Yes | ❌ Attr alloc | ❌ No |
| **Async pipeline** | ✅ Built-in | ❌ | ❌ | ❌ | ❌ |
| **Sync mode** | ✅ 4 explicit tiers | ✅ Sync | ✅ Sync | ✅ Sync | ✅ Sync |
| **Typed fields (0 alloc)** | ✅ `Field` union | ✅ `zap.Field` | ✅ Fluent | ⚠️ Attr boxing | ❌ |
| **Durability tiers (explicit)** | ✅ Documented | ⚠️ Implicit | ⚠️ Implicit | ❌ | ❌ |
| **Lock-free rate limiting** | ✅ CAS, bounded backoff | ❌ | ❌ | ❌ | ❌ |
| **Log rotation** | ⚠️ Size-based only, async mode only, no compression, no time-based | ❌ lumberjack | ❌ lumberjack | ❌ | ❌ lumberjack |
| **slog.Handler adapter** | ✅ Native | ✅ Bridge | ⚠️ Community | ✅ Native | ❌ |
| **External dependencies** | ✅ **0** | 2 | ✅ 0 | ✅ 0 (stdlib) | 1 |

> **Rotation scope note:** loggerj's built-in rotation is size-based only, and only in async mode (`MaxFileSize` is ignored in `SyncMode`) —
> no time-based (daily) rotation, no gzip compression. This is a deliberate
> subset of lumberjack's functionality, traded for zero external dependencies.
> For production systems requiring daily rotation, compression, or remote
> shipping, use lumberjack as the underlying `io.Writer` via
> `StartWithWriter()`. loggerj's rotation is "simple but sufficient" for
> basic size-capped log management, not a lumberjack replacement.

---

## Durability Tiers: The Honest Difference

Loggers that call `write(2)` once per entry (zap and zerolog writing to a file, loggerj `Direct`) leave the data in the kernel page cache: it survives a process crash but not an OS crash or power loss. loggerj's `SyncMode` makes the tier explicit and adds `fsync` tiers and two buffered tiers. The buffered tiers keep entries in process memory, which is why they are faster and why they lose entries on a process crash.

| Tier | Survives process crash | Survives OS crash / power loss | Throughput (reference machine) |
|---|---|---|---|
| **loggerj OSBuffered** | ❌ Unflushed entries are lost (no background flush timer) | ❌ | ~111 ns/op (no syscall per entry) |
| **loggerj Direct** | ✅ Yes | ❌ No (page cache) | ~1566 ns/op |
| **loggerj FsyncEveryN** | ⚠️ Up to the last fsync; the last N-1 entries are lost | ✅ Up to the last fsync | see BENCH.md (`BenchmarkSyncMode_FsyncEveryN`) |
| **loggerj FsyncEveryWrite** | ✅ Yes | ✅ Yes, if the disk honors fsync | ~4.4 ms/op |
| zerolog / zap writing to a file | ✅ Yes | ❌ No | one `write(2)` per entry; compare with `Direct` |

The process-crash column is verified by `TestDurability_ProcessKill` (SIGKILL on a helper process). The OS-crash column is not tested.

---

## Where `loggerj` Loses (Intentional Trade-offs)

We believe in radical transparency. Here is every case where `loggerj` is **not** the right tool.

| Scenario | Why `loggerj` loses | Right tool / Mitigation |
|---|---|---|
| **slog drop-in replacement** | `WithGroup` flattens to dotted keys (`"http.method"`), **not** real nested JSON. stdlib `slog.JSONHandler` produces `{"http":{"method":"GET"}}`. | **slog** (stdlib) if your pipeline depends on nested schema |
| **Nested structured logs** | No `zap.Object` or `zerolog.Dict()` equivalent. `Field` is a flat key-value tagged union. | **zap** or **zerolog** for deep nesting |
| **Plugin / multi-sink composition** | No `zapcore.Core` equivalent (intentional). No tiered multi-sink. | **zap** for composable sinks |
| **Production maturity** | v1.4.3 has comprehensive validation, but production track record is still limited compared to Uber's zap. | **zap** / **zerolog** for conservative teams |
| **Delivery under overload / crash** | The async channel drops entries when full (counted in `Stats().Drops`), leaving gaps, and async mode gives no crash durability. | `SyncMode` (`Direct` / `FsyncEveryWrite`) |
| **Timestamps** | In async mode `ts` is taken by the worker when it formats the entry, not when the log call is made; under backpressure it can lag the event. | **zap** / **zerolog** |
| **Sync-mode rotation** | `MaxFileSize` / `MaxBackupFiles` are ignored in `SyncMode`. | logrotate (copytruncate) or lumberjack |
| **Text-mode escaping** | Text output does not escape newlines or spaces in values; untrusted input can forge log lines. | Use `JSONOutput` for untrusted data |
| **Rate-limit exact limit >16M** | Packed atomic state caps exact counting at 16,777,215 per window. | Use external rate limiter |

### Why No Nested JSON Objects?

Dynamic nested JSON objects are intentionally not supported. All evaluated implementations (variadic slice, inline array, interface boxing, closure) break the zero-allocation hot-path guarantee. Dotted-key flattening (e.g., `"http.method":"GET"`) is the correct design for modern log aggregation pipelines (Loki, Elasticsearch, Datadog) and preserves zero-alloc guarantees.

### Why No Dynamic Rate Limiting Per Call?

Rate limits are bound to `SubProfile` at init-time (`RegisterSub`). This eliminates hot-path map lookups and mutex locks. If you need dynamic limits per request, you must implement external throttling.

---

## Where `loggerj` Wins vs zap / zerolog

| Scenario | Why `loggerj` wins | Gain |
|---|---|---|
| **API gateway / proxy** | Async + 0 alloc on the caller path + pre-baked prefix | Low caller-side cost (measure end-to-end) |
| **High-QPS microservice** | Channel buffering absorbs bursts | Drops (counted) instead of blocking when full |
| **Noisy rate-limited logs** | Lock-free CAS with bounded backoff | No mutex, no CPU spin |
| **Zero-dependency policy** | Even rotation is hand-rolled | No supply-chain risk |
| **GC-sensitive workloads** | No heap allocation on the caller path in the documented APIs | Less GC pressure from logging |

---

## Migration Guide

### From zerolog

```go
// zerolog
log.Info().Str("method", "GET").Int("status", 200).Msg("request")

// loggerj
logger.RegisterSub("HTTP", loggerj.WithFields("service", "api"))
logger.InfoFields("HTTP", []byte("request"),
    loggerj.Str("method", "GET"),
    loggerj.Int("status", 200))
```

### From zap

```go
// zap
logger.Info("request", zap.String("method", "GET"), zap.Int("status", 200))

// loggerj
logger.InfoFields("HTTP", []byte("request"),
    loggerj.Str("method", "GET"),
    loggerj.Int("status", 200))
```

### From slog

```go
// slog
slog.Info("request", "method", "GET", "status", 200)

// loggerj (using slog.Handler adapter)
handler := loggerj.NewSlogHandler(logger, "APP")
slog.SetDefault(slog.New(handler))
slog.Info("request", "method", "GET", "status", 200) 
```
