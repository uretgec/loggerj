# loggerj vs. the Go Logging Ecosystem

An **honest** comparison. We show where `loggerj` wins *and* where it loses.
This is an engineering comparison, not marketing copy.

> **Methodology:** `loggerj` numbers are from v1.3.1 benchmarks (Apple M1 Pro,
> Go 1.21+, `-count=5`). Competitor numbers are approximate values taken from
> their public benchmarks and community reports; they vary ±30% by hardware
> and version. Treat the **order of magnitude** as meaningful, not the exact
> figures. Always benchmark on *your* hardware with *your* workload.

---

## Quick Verdict

- **High-throughput async services** (gateways, proxies, high-QPS microservices):
  `loggerj` wins by **5-20x**. Nothing else comes close on throughput.
- **Sync / audit-critical logging with per-log durability**: `loggerj`'s
  `SyncMode` (OSBuffered tier, ~112 ns/op) is **2.7x faster than zerolog sync**
  and explicitly documents its durability guarantees.
- **Structured logging with typed fields**: `loggerj`'s `Field` API gives
  **0 allocs/op** for dynamic values — matching zap, beating slog (3-8 allocs).
- **slog ecosystem**: `loggerj` provides a `slog.Handler` adapter (v1.3.1) that
  routes all slog calls through loggerj's async pipeline (~143 ns/op, 0 allocs).
- **`logrus`**: `loggerj` wins everywhere; `logrus` is in maintenance mode.

---

## 1. Performance: Async Hot Path

This is `loggerj`'s home turf. The async pipeline (channel + worker) is the
intended usage pattern.

| Benchmark | loggerj (v1.3.1) | zerolog | zap | slog | logrus |
|---|---|---|---|---|---|
| **Filtered** (below threshold) | **~2.1 ns** | — | — | — | — |
| **NoFields** (simple log) | **~54 ns** | ~250 ns (sync) | ~300 ns (sync) | ~400 ns (sync) | ~2000 ns (sync) |
| **JSON** (2 fields) | **~53 ns** | ~300 ns (sync) | ~400 ns (sync) | ~500 ns (sync) | ~2500 ns (sync) |
| **RateLimited** (lock-free CAS) | **~41 ns** | N/A | N/A | N/A | N/A |
| **Parallel** (10 goroutines) | **~85 ns** | — | — | — | — |
| **Allocs/op** (all async paths) | **0** | 0-1 | 1-2 | 3-8 | 10+ |

> **Note:** Comparing async `loggerj` to sync zerolog/zap is comparing
> different problem classes. `loggerj` is async by design; zerolog/zap are
> sync by default. See Section 2 for the fair sync-vs-sync comparison.

### Throughput (single core, logs/second)

| System | ~logs/s | Allocs/op | Model |
|---|---|---|---|
| **loggerj (async)** | **~18M** | **0** | Async, lock-free |
| zerolog | ~3-4M | 0-1 | Sync |
| zap | ~2.5-3M | 1-2 | Sync |
| slog | ~1.5-2M | 3-8 | Sync |
| logrus | ~0.5M | 10+ | Sync |

---

## 2. Performance: Sync Mode (Fair Apples-to-Apples)

`loggerj` v1.3.0 introduced **Sync Mode** with four explicit durability tiers.
This section compares only the `OSBuffered` tier (the default), which matches
what zerolog and zap do — but is ~2.7x faster.

| Package | Model | ~ns/op | allocs/op | Lock-free? |
|---|---|---|---|---|
| **loggerj** (SyncMode, OSBuffered) | Buffered + shared mutex | **~112** | **0** | ⚠️ Mutex only during buffer copy (~5ns), not during syscall |
| zerolog (sync) | Buffered + mutex | ~250-350 | 0-1 | ❌ No |
| zap (JSONEncoder) | Buffered + mutex | ~300-500 | 1-2 | ❌ No |
| slog (JSONHandler) | Buffered + mutex | ~400-700 | 3-8 | ❌ No |

### Parallel Sync (10 goroutines)

| Package | ~ns/op | Notes |
|---|---|---|
| **loggerj** (SyncMode, OSBuffered) | **~238** | Mutex contention on shared buffer |
| zerolog (sync) | ~500-700 | Heavier mutex |
| zap (sync) | ~600-800 | Heavier mutex |

> **Honest note:** Under parallel sync load, `loggerj`'s shared `syncBw`
> creates mutex contention (112 ns → 238 ns, ~2x slowdown). We chose a
> shared buffer for throughput over a per-goroutine pool because the latter
> loses buffered data on `Close()`. Even with this contention, `loggerj`
> remains **2-3x faster than zerolog/zap sync** under parallel load.

---

## 3. Performance: Typed Fields (Caller-Side Allocation)

This is where the `Field` API shines. When the caller has **dynamic** values
(variables, not string literals), typed fields avoid the `strconv.Itoa` /
`fmt.Sprintf` allocations the string API requires.

| Package | API | Dynamic fields allocs/op | Example |
|---|---|---|---|
| **loggerj** | `Int("status", statusVar)` | **0** | Tagged union, no boxing |
| zap | `zap.Int("status", statusVar)` | 0 | Tagged union |
| zerolog | `.Int("status", statusVar)` | 0 | Fluent builder |
| slog | `slog.Int("status", statusVar)` | **3-8** | Interface boxing |
| logrus | `.WithField("status", statusVar)` | **1+** | Map + interface |

### Benchmark: `loggerj` typed fields, dynamic values

```txt
BenchmarkTypedFields_Dynamic-10    15,700,000    72 ns/op    0 B/op    0 allocs/op
```

Zero allocations, end-to-end. The `Field` struct is 48 bytes, passed by
value, with `Num uint64` holding int64/uint64/float64-bits/duration-ns/bool
as a tagged union — no interface boxing, no heap escape.

---

## 4. Performance: slog.Handler Adapter (v1.3.1)

`loggerj` v1.3.1 introduced a `slog.Handler` adapter that routes all slog
calls through loggerj's zero-allocation typed-field pipeline. This lets
applications adopted to the `log/slog` standard benefit from loggerj's
async throughput without changing their call sites.

| Package | Adapter | ~ns/op | allocs/op |
|---|---|---|---|
| **loggerj** | `SlogHandler` | **~143** | **0** |
| zap | `zap.SugaredLogger` bridge | ~200-300 | 1-2 |
| zerolog | Community adapter | ~250-400 | 2-4 |

### Design Notes

- `slog.Attr` → `Field` conversion is boxing-free (slog.Value is already tagged union)
- `WithAttrs` pre-converts attributes once (cold path); `Handle()` only converts per-record attrs
- `WithGroup` flattens nested groups into dotted keys (e.g., "outer.inner")
- Field buffers are pooled, keeping `Handle()` allocation-free once warm

**Benchmark:**

```txt
BenchmarkSlogHandler_Handle-10    8,600,000    143 ns/op    0 B/op    0 allocs/op
```

**Key differentiator:** loggerj's slog adapter is **~2x faster than zap's bridge**
because it leverages the zero-alloc `Field` API and async pipeline.

---

## 5. Feature Matrix

| Feature | loggerj | zap | zerolog | slog | logrus |
|---|---|---|---|---|---|
| **Zero-alloc hot path** | ✅ Real (0 allocs) | ⚠️ Partial | ✅ Yes | ❌ Attr alloc | ❌ No |
| **Async pipeline** | ✅ Built-in | ❌ | ❌ | ❌ | ❌ |
| **Sync mode** | ✅ 4 explicit tiers | ✅ Sync | ✅ Sync | ✅ Sync | ✅ Sync |
| **Typed fields (0 alloc)** | ✅ `Field` union | ✅ `zap.Field` | ✅ Fluent | ⚠️ Attr boxing | ❌ |
| **Durability tiers (explicit)** | ✅ OSBuffered / Direct / FsyncEveryN / FsyncEveryWrite | ⚠️ Implicit | ⚠️ Implicit | ❌ | ❌ |
| **Lock-free rate limiting** | ✅ CAS, per-profile | ⚠️ Sampling only | ❌ | ❌ | ❌ |
| **Lock-free sampling** | ✅ Atomic counter | ✅ | ❌ | ❌ | ❌ |
| **Log rotation (0 deps)** | ✅ Built-in | ❌ lumberjack | ❌ lumberjack | ❌ | ❌ lumberjack |
| **slog.Handler adapter** | ✅ Native (v1.3.1) | ✅ Bridge | ⚠️ Community | ✅ Native | ❌ |
| **Context integration** | ✅ Opt-in, zero cost when unused | ⚠️ Manual | ⚠️ Manual | ✅ Native | ⚠️ Manual |
| **Runtime level change** | ✅ ~2ns atomic | ✅ | ✅ | ⚠️ Handler swap | ✅ |
| **Caller info** | ✅ Opt-in (~460ns, 2 allocs) | ✅ | ✅ | ✅ | ✅ |
| **Strict log ordering** | ⚠️ Approximate (async) | ✅ Sync | ✅ Sync | ✅ Sync | ✅ Sync |
| **Plugin / Core interface** | ❌ Intentional | ✅ `zapcore.Core` | ✅ Hooks | ⚠️ Handler wrap | ✅ Hooks |
| **External dependencies** | ✅ **0** | 2 (go.uber.org) | ✅ 0 | ✅ 0 (stdlib) | 1 (x/sys) |

---

## 6. Durability Tiers: The Honest Difference

This is where `loggerj` says out loud what other loggers quietly assume.

All three sync loggers (loggerj, zerolog, zap) default to "OS-buffered": a
single `write(2)` syscall that lands data in the kernel page cache. This
**survives process crashes but NOT OS crashes or power loss** — data may
still be in the page cache.

| Tier | Survives process crash | Survives OS crash / power loss | Throughput |
|---|---|---|---|
| **loggerj OSBuffered** (default) | ✅ Yes | ❌ No (page cache) | ~112 ns/op |
| **loggerj Direct** | ✅ Yes | ❌ No (page cache) | ~1550 ns/op |
| **loggerj FsyncEveryN** | ✅ Yes | ✅ Yes (every N logs) | ~5000 ns/op |
| **loggerj FsyncEveryWrite** | ✅ Yes | ✅ Yes (every log) | ~4-5 ms/op |
| zerolog sync | ✅ Yes | ❌ No (not documented) | ~300 ns/op |
| zap sync | ✅ Yes | ❌ No (not documented) | ~400 ns/op |

> `loggerj` is the only logger that **explicitly documents** the OS-buffered
> limitation and offers `fsync`-based tiers for audit-grade durability. If
> you're writing audit logs, financial records, or anything that must
> survive a power outage, choose `FsyncEveryN` or `FsyncEveryWrite`.

---

## 7. Where `loggerj` Loses (Honest Weaknesses)

We believe in radical transparency. Here is every case where `loggerj` is
**not** the right tool, including weaknesses that are easy to miss.

| Scenario | Why `loggerj` loses | Right tool / Mitigation |
|---|---|---|
| **slog drop-in replacement** | `WithGroup` flattens to dotted keys (`"http.method"`), **not** real nested JSON. stdlib `slog.JSONHandler` produces `{"http":{"method":"GET"}}`. If your Loki/ES/Datadog parser expects the stdlib nested schema, it will break. This is documented in EXAMPLES.md as a limitation, but it is a real migration risk, not a win. | **slog** (stdlib) if your pipeline depends on nested schema |
| **Nested structured logs** | No `zap.Object`, `zerolog.Dict()`, or `slog.Group` equivalent. `Field` is a flat key-value tagged union; it cannot emit nested JSON objects. Large microservice logs (request → user → address) hit this wall. | **zap** or **zerolog** for deep nesting |
| **Extreme CAS contention (50+ cores)** | `checkAtomicRateLimit` retries in an unbounded `for { CAS }` loop with **no backoff**. Under extreme thundering-herd contention the retry behavior is measured (`BenchmarkLog_RateLimited_HighContention`) but not yet published per core-scale. | Measure on *your* core count before relying on it |
| **Rate-limited hot path purity** | `time.Now().UnixMilli()` is called on **every** rate-limit check. We moved timestamps off the format path (worker-side), but the rate-limit gate still pays a vDSO/syscall cost per call. This partially undermines the "zero-cost hot path" claim for rate-limited types. | Accept it, or avoid rate limits on your hottest paths |
| **Plugin / multi-sink composition** | No `zapcore.Core` equivalent (intentional). Sentry/Prometheus/custom-sink integration is limited to `AsWriter` or hand-rolled writers. No tiered multi-sink (different levels to different sinks). | **zap** for composable sinks |
| **Production maturity** | v1.2.0, v1.3.0, and v1.3.1 all shipped on the **same day** (2026-08-07). Zero production track record. Single maintainer. The rapid release cadence is itself a risk signal — "how often will a breaking change land?" is a fair question. | **zap** / **zerolog** for conservative teams; wait for loggerj to age |
| **`unsafe` version sensitivity** | `unsafeStringToBytes` relies on Go 1.21+ `unsafe.StringData` + `go:nosplit`. This is sensitive to compiler/runtime changes across Go versions. CI version matrix and fuzzing are **not yet in place** (deferred to v1.4.0). | Wait for the CI matrix before safety-critical adoption |
| **Strict per-log ordering** | Async channel can reorder under load. | **zerolog** or **zap** (sync) |
| **Low-volume CLI / cron** | Async overhead unnecessary. | **slog** or **zerolog** |

---

## 7b. Where Competitors Are Clearly Better

| Competitor | What they do better than loggerj |
|---|---|
| **zap** | Plugin ecosystem (`zapcore.Core`), millions of production lines validating edge cases, tiered sampling, composable multi-sink |
| **zerolog** | Real nested `Dict()` support, the combination of zero-dependency **and** years of maturity |
| **slog** | Stdlib = zero adoption risk, runs everywhere with no supply-chain question, `slog.Group` produces real nested objects |
| **logrus** | Widest hook ecosystem (Sentry, Logstash, Elasticsearch integrations) despite being in maintenance mode |

> **The honest summary:** `loggerj` wins on raw throughput, zero-allocation
> guarantees, and explicit durability documentation. It loses on ecosystem
> breadth, nesting, plugin composability, and — most importantly — maturity.
> If any of the losing rows matters more to you than throughput, use the
> listed tool. We would rather you pick the right tool than adopt loggerj
> and regret it.

---

## 8. Where `loggerj` Wins

| Scenario | Why `loggerj` wins | Gain |
|---|---|---|
| **API gateway / proxy** | Async + 0 alloc + pre-baked prefix | 5-20x throughput |
| **High-QPS microservice** | Channel buffering absorbs bursts | No deadlock under load |
| **Noisy rate-limited logs** | Lock-free CAS, built-in | No mutex, no contention |
| **Zero-dependency policy** | Even rotation is hand-rolled | No supply-chain risk |
| **GC-sensitive workloads** | Hot path truly 0 alloc | No GC pauses from logging |
| **Audit trails with fsync** | Explicit durability tiers | Documented guarantees |
| **Sync mode under concurrency** | Shared-buffer mutex (~5ns critical section) | 2-3x faster than zerolog/zap sync |
| **slog ecosystem integration** | Native handler, zero-alloc pipeline | 2x faster than zap bridge |

---

## 9. Decision Matrix

```txt
Is your log volume > 1M logs/s?
├── YES → loggerj (async) ✅
└── NO
    ├── Do you need per-log write guarantees (audit/finance)?
    │   ├── YES → loggerj (SyncMode + FsyncEveryN) ✅
    │   │         (explicit durability, faster than zerolog sync)
    │   └── NO
    │       ├── Are you committed to the slog ecosystem?
    │       │   ├── YES → slog ✅ (or loggerj slog.Handler for async pipeline)
    │       │   └── NO
    │       │       ├── Do you need strict log ordering?
    │       │       │   ├── YES → zerolog or zap (sync) ✅
    │       │       │   └── NO → loggerj (async) ✅
    │       │       └── Is type safety (zap.Int-style) critical?
    │       │           ├── YES → zap ✅ (mature plugin ecosystem)
    │       │           └── NO → loggerj or zerolog
```

---

## 10. Maturity & Ecosystem (the honest table)

| Criterion | loggerj | zap | zerolog | slog | logrus |
|---|---|---|---|---|---|
| Production history | 🟡 New (v1.3.1, 2026) | 🟢 Years (Uber) | 🟢 Years | 🟢 Go team | 🟢 Years |
| Maintenance | 🟡 Single maintainer | 🟢 Uber team | 🟢 Active | 🟢 Go team | 🔴 Maintenance mode |
| GitHub stars (approx.) | 🟡 New | 🟢 22k+ | 🟢 11k+ | 🟢 (stdlib) | 🟢 24k+ |
| Battle-tested edge cases | 🟡 Limited | 🟢 Thousands of issues | 🟢 Hundreds | 🟢 Go release cycle | 🟢 Thousands |
| External dependencies | 🟢 **0** | 🟡 2 | 🟢 0 | 🟢 0 | 🟡 1 |
| Documentation | 🟢 README + EXAMPLES + BENCH + COMPARISON | 🟢 Comprehensive | 🟢 Good | 🟢 Official | 🟢 Comprehensive |

> **The honest caveat:** `loggerj`'s biggest risk is not performance — it's
> **maturity**. The v1.2.0 and v1.3.0 bugs were caught during development,
> but the edge cases that thousands of production environments surface are
> still largely undiscovered. Early adopters accept this risk; conservative
> teams should wait for v1.5.0+ or stick with zap/zerolog.

---

## 11. Migration: Coming from Other Loggers

### From zerolog

```go
// zerolog
log.Info().Str("method", "GET").Int("status", 200).Msg("request")

// loggerj (async, typed fields)
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

### From logrus

```go
// logrus
logrus.WithFields(logrus.Fields{"method": "GET", "status": 200}).Info("request")

// loggerj
logger.InfoFields("HTTP", []byte("request"),
    loggerj.Str("method", "GET"),
    loggerj.Int("status", 200))
```

### From slog

```go
// slog
slog.Info("request", "method", "GET", "status", 200)

// loggerj (using slog.Handler adapter, v1.3.1)
logger := loggerj.NewLogger(loggerj.Config{JSONOutput: true})
go logger.Start(ctx)
handler := loggerj.NewSlogHandler(logger, "APP")
slog.SetDefault(slog.New(handler))
slog.Info("request", "method", "GET", "status", 200) // flows through loggerj async pipeline
```

---

## Recommended README Blurb

> `loggerj` is optimized for **async throughput** and **explicit durability
> guarantees**. If you need strict per-log ordering, use zerolog or zap (sync).
> If you need 10M+ logs/s with zero GC pressure, `loggerj` is purpose-built for you.

This sentence loses no trust; it **builds** trust, because it helps users
pick the right tool for their workload.

---

## Further Reading

- **[README.md](README.md)** — Quick start, philosophy, performance summary
- **[EXAMPLES.md](EXAMPLES.md)** — Comprehensive usage examples
- **[BENCH.md](BENCH.md)** — Detailed benchmark methodology and results
- **[ROADMAP.md](ROADMAP.md)** — What's coming in v1.3.x and beyond
- **[CHANGELOG.md](CHANGELOG.md)** — Release notes and migration guides
