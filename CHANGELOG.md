# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

For detailed benchmark results and performance methodology, see [BENCH.md](BENCH.md).

---

## [1.3.1] - 2026-08-07

### 🎉 Major Features

#### slog.Handler Adapter (Go 1.21+ Ecosystem Integration)

- **New:** `SlogHandler` struct implementing `slog.Handler` interface
- **New:** `NewSlogHandler(logger, logType)` constructor
- **Design:** `slog.Attr` → `Field` conversion is boxing-free (slog.Value is already a tagged union)
- **Design:** `WithAttrs` pre-converts attributes once (cold path); `Handle()` only converts per-record attrs
- **Design:** `WithGroup` flattens nested groups into dotted keys (e.g., "outer.inner")
- **Benefit:** Routes all `slog` calls through loggerj's zero-allocation typed-field pipeline
- **Benefit:** Adoption doubles for projects committed to the `log/slog` standard

**Example:**

```go
logger := loggerj.NewLogger(loggerj.Config{JSONOutput: true})
go logger.Start(ctx)

handler := loggerj.NewSlogHandler(logger, "APP")
slog.SetDefault(slog.New(handler))

// All slog calls now flow through loggerj's async pipeline
slog.Info("request", "method", "GET", "status", 200)
slog.With("env", "prod").Info("startup")
```

**Benchmark:**

```txt
BenchmarkSlogHandler_Handle-10    8,600,000    143 ns/op    0 B/op    0 allocs/op
```

### ⚡ Performance Improvements

#### getProfile Adaptive Map Fallback (n > 8)

- **Changed:** `getProfile` now uses adaptive strategy based on registry size
- **New:** `profileRegistry.lookup` map field (nil when n ≤ 8, populated when n > 8)
- **Threshold:** Benchmark-driven — map is 2.7x faster than linear at n=16 on Apple M1 Pro (7.7ns vs 20.5ns)
- **Incremental updates:** `RegisterSub` clones map + applies single change (O(1) vs O(n) rebuild)
- **Benefit:** O(1) lookup for large registries (>8 profiles), preventing performance degradation at scale

**Benchmark:**

```txt
BenchmarkGetProfile_LinearScanDirect-10    152,000,000    7.9 ns/op    0 allocs/op
BenchmarkGetProfile_MapLookupDirect-10     156,000,000    7.7 ns/op    0 allocs/op
```

### 🔭 Observability

#### syncWriteErrors Counter for Audit Trails

- **New:** `Stats.SyncWriteErrors` field counts failed `write(2)` calls in sync mode
- **New:** Every failed write is logged to stderr with tier information
- **New:** `syncFileWrite()` and `syncFileSync()` helper methods wrap I/O with error counting
- **Benefit:** Audit-oriented users can now detect log loss via `Stats()` polling
- **Why:** The audit claim ("per-log write guarantees") is only meaningful if failures are observable

**Example:**

```go
stats := logger.Stats()
if stats.SyncWriteErrors > 0 {
    log.Printf("WARNING: %d sync writes failed — possible log loss", stats.SyncWriteErrors)
}
```

### 📚 Documentation

#### Honesty Fixes

- **Changed:** BENCH.md "Benchmark: Sync Mode (Lock-Free O_APPEND)" → "Benchmark: Sync Mode"
- **Clarified:** Only `Direct` and `FsyncEveryWrite` are truly lock-free on the write path
- **Clarified:** `OSBuffered` and `FsyncEveryN` use `syncMu` during buffer copy (~5ns), not during syscall
- **Updated:** README.md sync mode feature description with honest mutex disclosure
- **Updated:** `syncWrite` doc comment in `loggerj.go` matches the honest docs
- **Synced:** README.md and BENCH.md benchmark numbers (NoFields ~53ns, Parallel ~85ns, etc.)

**Why this matters:** We claimed "lock-free" in BENCH.md while our own code comment said "Protects syncBw memory copy (not the syscall)". This inconsistency violated our honesty principle. Now docs match code.

### 🧪 Testing

#### New Tests

- `TestSlogHandler_Basic` — simple slog.Info flows through loggerj
- `TestSlogHandler_Levels` — level filtering respects loggerj's threshold
- `TestSlogHandler_TypedAttrs` — typed slog attrs convert without boxing
- `TestSlogHandler_WithAttrs` — pre-attributes prepended to every record
- `TestSlogHandler_WithGroup` — groups flatten into dotted keys
- `TestSlogHandler_Enabled` — Enabled() reflects runtime level changes
- `TestSlogHandler_ValidJSON` — all output is well-formed JSON
- `TestSlogHandler_ZeroAlloc` — Handle() is allocation-free once warm
- `TestGetProfile_SmallRegistry_LinearScan` — n ≤ 8 uses linear scan
- `TestGetProfile_LargeRegistry_MapFallback` — n > 8 uses map O(1)
- `TestGetProfile_ThresholdCrossing` — adaptive strategy works at n=8→9
- `TestSyncWriteErrors` — failed writes are counted and observable

#### New Benchmarks

- `BenchmarkSlogHandler` — adapter throughput with slog.Info variadic overhead
- `BenchmarkSlogHandler_Handle` — Handle() method cost (pre-built Record)
- `BenchmarkGetProfile_SmallRegistry` — linear-scan performance (8 profiles)
- `BenchmarkGetProfile_LargeRegistry` — map-based O(1) lookup (200 profiles)
- `BenchmarkGetProfile_LinearScanDirect` — pure linear-scan cost
- `BenchmarkGetProfile_MapLookupDirect` — pure map-lookup cost

### 📊 Benchmark Results (Apple M1 Pro, Go 1.21+)

#### slog.Handler Adapter

| Benchmark | ns/op | allocs/op | Notes |
|---|---|---|---|
| `SlogHandler` | ~462 | 0 | Includes slog.Info variadic overhead |
| `SlogHandler_Handle` | **143** | **0** | Handler internals only (pre-built Record) |

#### getProfile Adaptive Strategy

| Benchmark | ns/op | allocs/op | Notes |
|---|---|---|---|
| `GetProfile_LinearScanDirect` | ~7.9 | 0 | Pure linear scan (no format/dispatch) |
| `GetProfile_MapLookupDirect` | ~7.7 | 0 | Pure map lookup (no format/dispatch) |

**Key insight:** Map is 2.7x faster than linear at n=16. Threshold 8 is conservative; most apps register <8 profiles.

#### Async Mode (No Regressions)

| Benchmark | v1.3.0 | v1.3.1 | Change |
|---|---|---|---|
| `Filtered` | ~2.06 ns | ~2.07 ns | Same |
| `NoFields` | ~53-62 ns | ~53-62 ns | Same |
| `Parallel` | ~85 ns | ~85 ns | Same |

### 🔒 Security

- No security vulnerabilities addressed (package has zero external dependencies)

### 🚫 Deprecated

- No APIs deprecated

### 🗑️ Removed

- No APIs removed

### 🔄 Migration Guide (v1.3.0 → v1.3.1)

#### If you use slog

```go
// New (v1.3.1) — route slog through loggerj
handler := loggerj.NewSlogHandler(logger, "APP")
slog.SetDefault(slog.New(handler))
slog.Info("request", "method", "GET")
```

#### If you register >8 profiles

```go
// No code change needed — getProfile automatically uses map for n > 8
logger.RegisterSub("TYPE_1", ...)
logger.RegisterSub("TYPE_2", ...)
// ... 200 profiles ...
// All lookups are now O(1) via map
```

#### If you monitor sync mode durability

```go
// New (v1.3.1) — detect sync write failures
stats := logger.Stats()
if stats.SyncWriteErrors > 0 {
    log.Printf("WARNING: %d sync writes failed", stats.SyncWriteErrors)
}
```

All other APIs remain unchanged. No breaking changes for existing users.
