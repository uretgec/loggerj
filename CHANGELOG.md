# Changelog

All notable changes to this project will be documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [1.4.3] - 2026-08-07

### Documentation Fixes

- **README.md headline clarification:** Added "(async mode)" qualifier to throughput claims to prevent misinterpretation.
- **slog allocs/op attribution fix:** Corrected the claim that `slog.Int()` allocates. Boxing occurs at the `Record`/`any`-typed variadic call sites (`slog.Info(...)`), not at typed constructors. The `SlogHandler.Handle()` method is allocation-free once warm.
- **`WithRateLimit` godoc:** Added "GATE ORDER" section explaining that rate limiting is applied AFTER sampling. Documented the performance rationale (~34ns `time.Now()` avoidance) and provided concrete example showing the semantic difference.
- **`WithSampleRate` godoc:** Added "GATE ORDER" section explaining that sampling is applied BEFORE rate limiting for performance reasons.
- **README.md Features:** Clarified that "Lock-Free Rate Limiting & Sampling" applies to the async hot path only. Explicitly disclosed that Sync Mode's OSBuffered and FsyncEveryN tiers hold a mutex around the shared `bufio.Writer` during buffer copy AND flush/fsync; only Direct and FsyncEveryWrite tiers are truly lock-free (O_APPEND atomic). This prevents readers from inferring that all sync tiers are lock-free when the two features are listed adjacent to each other.

---

## [1.4.2] - 2026-08-07

### Documentation Honesty Fixes

- **syncWrite mutex/syscall disclosure:** Fixed incorrect claim that mutex is held "only during buffer copy". Updated docs to state that `syncMu` is held during BOTH buffer copy AND flush/fsync syscall for OSBuffered/FsyncEveryN tiers.
- **AsWriter godoc cleanup:** Removed stale allocation notes. Added clarification that zero-allocation is verified on Go 1.22+; Go 1.21 shows 1 alloc/op due to compiler escape analysis limitations.
- **Rate-limit cost math fix:** Corrected total real-world cost calculation to ~36.5ns.
- **slog time.Time precision fix:** Changed `time.RFC3339` to `time.RFC3339Nano` in `SlogHandler` to preserve sub-second precision for audit trails.

### Testing

- **Build tag test files:** Split `TestAsWriter_ZeroAlloc` into Go 1.21 and Go 1.22+ specific files.
- **New test:** `TestSlogHandler_TimePrecision` verifies nanosecond preservation.

---

## [1.4.0] - 2026-08-07

**Theme:** Trust, Validation, Completeness.

### 🛡️ Validation & CI Infrastructure

- Added GitHub Actions CI matrix testing across Go 1.21, 1.22, 1.23, and 1.24.
- Added automated `benchstat` regression gate blocking PRs with >10% hot-path regression.
- Added fuzz testing targets for `appendJSONString`, `appendDuration`, and rate-limit windows.

### ⚡ Rate-Limit Refactor

- Replaced epoch-based 32-bit window index with profile-relative 40-bit window index and 24-bit counter.
- Exact rate limits are now capped at **16,777,215** per window.
- Added bounded CAS backoff (`runtime.Gosched()`) under extreme contention.
- Over-limit calls now return `false` immediately without executing CAS.

### 🔗 slog.Handler Completeness

- Fixed multi-key group flattening. `slog.Group` attributes correctly emit dotted keys.

### 🔒 Security & Maturity

- Replaced `unsafe.Pointer` type-punning for `float64` bit conversions with `math.Float64bits` and `math.Float64frombits` compiler intrinsics.
