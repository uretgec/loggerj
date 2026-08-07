# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [1.4.0] - 2026-08-07

**Theme:** Trust, Validation, Completeness.
v1.4.0 focuses on proving performance claims via automation, fixing edge cases in the lock-free rate limiter, and removing unnecessary `unsafe` usage.

### 🛡️ Validation & CI Infrastructure

- **Added:** GitHub Actions CI matrix testing across Go 1.21, 1.22, 1.23, and 1.24.
- **Added:** Automated `benchstat` regression gate. PRs that regress protected hot-path benchmarks by >10% or increase `allocs/op` are blocked from merging.
- **Added:** Fuzz testing targets for `appendJSONString`, `appendDuration`, and rate-limit window calculations.
- **Added:** `Makefile` targets for local baseline generation and benchmark gating.

### ⚡ Rate-Limit Refactor & Hot-Path Honesty

- **Changed:** Rate-limit state layout. Replaced the epoch-based 32-bit window index with a profile-relative 40-bit window index and 24-bit counter. This eliminates overflow risks for long-running processes with sub-millisecond windows.
- **Changed:** Exact rate limits are now capped at **16,777,215** per window due to the 24-bit counter limit. Larger configured limits are silently capped.
- **Added:** Bounded CAS backoff. Under extreme contention, the rate-limit CAS loop now yields via `runtime.Gosched()` after 8 failures to prevent CPU burning.
- **Fixed:** Over-limit calls now return `false` immediately without executing a CAS instruction, reducing CPU spin in saturated states.
- **Documented:** Published the `time.Now().UnixMilli()` vDSO cost breakdown (~34ns/op). We intentionally do not use a coarse-time cache to preserve exact sub-second window boundaries.

### 🔗 slog.Handler Completeness

- **Fixed:** `SlogHandler` multi-key group flattening. `slog.Group` attributes and nested `WithGroup` calls now correctly emit dotted keys (e.g., `"http.method":"GET"`) instead of falling back to string representations.

### 🔒 Security & Maturity

- **Changed:** Replaced `unsafe.Pointer` type-punning for `float64` bit conversions with `math.Float64bits` and `math.Float64frombits`. The Go compiler optimizes these to identical intrinsic CPU instructions, eliminating `unsafe` usage on this path without impacting hot-path performance.
- **Added:** `docs/rate-limit-cost.md` detailing the exact CPU cost breakdown of the rate-limit hot path.

### 🧪 Testing

#### New Tests

- `TestCheckAtomicRateLimit_BoundaryExact` — Verifies exact boundary behavior.
- `TestCheckAtomicRateLimit_WindowReset` — Verifies profile-relative window resets.
- `TestCheckAtomicRateLimit_WindowIndexBeyond32Bit` — Verifies 40-bit window index prevents overflow.
- `TestCheckAtomicRateLimit_ProfileStartClampsNegative` — Verifies backward clock jumps are clamped.
- `TestCheckAtomicRateLimit_MaxWindowClamp` — Verifies timestamps beyond the 40-bit range are clamped.
- `TestWithRateLimit_CapsExactLimit` — Verifies limits >16M are capped.
- `TestSlogHandler_WithGroup_MultiAttr` — Verifies multi-attribute group flattening.
- `TestSlogHandler_GroupAttr` — Verifies `slog.Group` attribute flattening.

#### New Benchmarks

- `BenchmarkCheckAtomicRateLimit_Uncontended` — Pure CAS cost (~2.5 ns/op).
- `BenchmarkCheckAtomicRateLimit_HighContention` — CAS cost under multi-core contention.
- `BenchmarkCheckAtomicRateLimit_Saturated` — Fast-path rejection cost (~0.3 ns/op).
- `BenchmarkTimeNowUnixMilli` — Isolates vDSO syscall cost (~34 ns/op).

### 📊 Benchmark Results (Apple M1 Pro, Go 1.24)

#### Rate-Limit Cost Breakdown

| Component | ns/op | % of Total | Allocs |
|---|---:|---:|---:|
| Pure CAS + Bitwise Math | ~2.5 | ~7% | 0 |
| `time.Now().UnixMilli()` (vDSO) | ~34.0 | ~93% | 0 |
| **Total Real-World Cost** | **~35.2** | **100%** | **0** |

#### Contention Behavior

| Scenario | ns/op | Behavior |
|---|---:|---|
| **Uncontended** | ~2.5 | Pure CAS, no backoff triggered |
| **Saturated (over-limit)** | ~0.3 | Fast-path rejection before CAS |
| **High Contention** | ~213-257 | Bounded backoff prevents CPU spin |

### 🔄 Migration Guide (v1.3.x → v1.4.0)

#### Rate Limit Caps

If you previously configured rate limits exceeding 16,777,215 per window:

```go
// v1.3.x
logger.RegisterSub("API", loggerj.WithRateLimit(20_000_000, time.Second))

// v1.4.0
// The limit is automatically capped to 16,777,215. 
// No code change is required, but be aware of the exact ceiling.
```

#### slog Group Flattening

If you relied on the previous fallback behavior where multi-key `slog.Group` attributes were stringified:

```go
// v1.3.x output for slog.Group("http", "method", "GET", "status", 200)
// "http": "[method=GET status=200]" (string fallback)

// v1.4.0 output
// "http.method": "GET", "http.status": 200 (dotted keys)
```

This aligns with standard observability pipeline expectations (Loki, Elasticsearch).

All other APIs remain unchanged. No breaking changes for existing users.
