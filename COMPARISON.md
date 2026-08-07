# loggerj vs. the Go Logging Ecosystem

An engineering comparison. We show where `loggerj` wins, where it loses, and the architectural trade-offs we made to achieve zero-allocation hot paths.

*Note: Competitor numbers are approximate values taken from public benchmarks. Treat the order of magnitude as meaningful.*

---

## Executive Summary

- **High-throughput async services**: `loggerj` async is **2-3x faster than zap buffered-async** and **5-20x faster than sync loggers**.
- **Sync / audit-critical logging**: `loggerj`'s `SyncMode` (OSBuffered tier) is **2-3x faster than zerolog sync** and explicitly documents its durability guarantees.
- **Structured logging**: `loggerj`'s `Field` API gives **0 allocs/op** for dynamic values, matching zap and beating slog variadic call sites.
- **slog ecosystem**: Native `slog.Handler` adapter routes calls through loggerj's pipeline (~143 ns/op, 0 allocs). Groups are flattened to dotted keys.
- **logrus**: `loggerj` wins everywhere; `logrus` is in maintenance mode.
- **No configurable gate order (sampling vs rate limit).** Rate limit ALWAYS runs before sampling. This is a fixed architectural decision to keep the hot path branch-free and to leverage fast-path rate-limit rejection (~0.3ns) to skip sampling CPU cost during floods. If you need sampling to apply to raw input *before* rate limiting, implement it at the call site.

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
| **Log rotation** | ⚠️ Size-based only, no compression, no time-based | ❌ lumberjack | ❌ lumberjack | ❌ | ❌ lumberjack |
| **slog.Handler adapter** | ✅ Native | ✅ Bridge | ⚠️ Community | ✅ Native | ❌ |
| **External dependencies** | ✅ **0** | 2 | ✅ 0 | ✅ 0 (stdlib) | 1 |

> **Rotation scope note:** loggerj's built-in rotation is size-based only —
> no time-based (daily) rotation, no gzip compression. This is a deliberate
> subset of lumberjack's functionality, traded for zero external dependencies.
> For production systems requiring daily rotation, compression, or remote
> shipping, use lumberjack as the underlying `io.Writer` via
> `StartWithWriter()`. loggerj's rotation is "simple but sufficient" for
> basic size-capped log management, not a lumberjack replacement.

---

## Durability Tiers: The Honest Difference

All sync loggers default to "OS-buffered": a single `write(2)` syscall that lands data in the kernel page cache. This survives process crashes but **NOT OS crashes or power loss**.

`loggerj` is the only logger that explicitly documents this and offers `fsync`-based tiers.

| Tier | Survives process crash | Survives OS crash / power loss | Throughput |
|---|---|---|---|
| **loggerj OSBuffered** | ✅ Yes | ❌ No (page cache) | ~111 ns/op |
| **loggerj Direct** | ✅ Yes | ❌ No (page cache) | ~1566 ns/op |
| **loggerj FsyncEveryN** | ✅ Yes | ✅ Yes (every N logs) | ~5000 ns/op |
| **loggerj FsyncEveryWrite** | ✅ Yes | ✅ Yes (every log) | ~4.4 ms/op |
| zerolog / zap sync | ✅ Yes | ❌ No (not documented) | ~300-500 ns/op |

---

## Where `loggerj` Loses (Intentional Trade-offs)

We believe in radical transparency. Here is every case where `loggerj` is **not** the right tool.

| Scenario | Why `loggerj` loses | Right tool / Mitigation |
|---|---|---|
| **slog drop-in replacement** | `WithGroup` flattens to dotted keys (`"http.method"`), **not** real nested JSON. stdlib `slog.JSONHandler` produces `{"http":{"method":"GET"}}`. | **slog** (stdlib) if your pipeline depends on nested schema |
| **Nested structured logs** | No `zap.Object` or `zerolog.Dict()` equivalent. `Field` is a flat key-value tagged union. | **zap** or **zerolog** for deep nesting |
| **Plugin / multi-sink composition** | No `zapcore.Core` equivalent (intentional). No tiered multi-sink. | **zap** for composable sinks |
| **Production maturity** | v1.4.3 has comprehensive validation, but production track record is still limited compared to Uber's zap. | **zap** / **zerolog** for conservative teams |
| **Strict per-log ordering** | Async channel can reorder under extreme load. | **zerolog** or **zap** (sync) |
| **Rate-limit exact limit >16M** | Packed atomic state caps exact counting at 16,777,215 per window. | Use external rate limiter |

### Why No Nested JSON Objects?

Dynamic nested JSON objects are intentionally not supported. All evaluated implementations (variadic slice, inline array, interface boxing, closure) break the zero-allocation hot-path guarantee. Dotted-key flattening (e.g., `"http.method":"GET"`) is the correct design for modern log aggregation pipelines (Loki, Elasticsearch, Datadog) and preserves zero-alloc guarantees.

### Why No Dynamic Rate Limiting Per Call?

Rate limits are bound to `SubProfile` at init-time (`RegisterSub`). This eliminates hot-path map lookups and mutex locks. If you need dynamic limits per request, you must implement external throttling.

---

## Where `loggerj` Wins vs zap / zerolog

| Scenario | Why `loggerj` wins | Gain |
|---|---|---|
| **API gateway / proxy** | Async + 0 alloc + pre-baked prefix | 5-20x throughput |
| **High-QPS microservice** | Channel buffering absorbs bursts | No deadlock under load |
| **Noisy rate-limited logs** | Lock-free CAS with bounded backoff | No mutex, no CPU spin |
| **Zero-dependency policy** | Even rotation is hand-rolled | No supply-chain risk |
| **GC-sensitive workloads** | Hot path truly 0 alloc | No GC pauses from logging |

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
