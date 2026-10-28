# Changelog

All notable changes to this project will be documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [1.4.4] - 2026-10-26

Patch release: bug fixes and documentation corrections. The gate order (rate limit before sampling) is unchanged; switching to sampling first is planned for v1.5.0.

### Documentation corrections

- **Durability claims:** `OSBuffered` and `FsyncEveryN` do NOT survive a process crash for entries still in the buffer (there is no background flush timer, and the last N-1 `FsyncEveryN` entries are not fsynced). README, COMPARISON, EXAMPLES and godoc were corrected; a durability table was added to README.
- **Gate order:** the v1.4.3 entry below, the `WithRateLimit` / `WithSampleRate` godoc and EXAMPLES §7 described the order backwards. The code has always run the rate limit first. Docs now match the code, including the worked example and the cost of the clock read.
- **Fair comparisons:** removed the "2-3x faster than zerolog sync" and "5-20x" claims. `OSBuffered` has no syscall per entry; the async numbers are caller-side costs, not delivered throughput.
- **Locking wording:** the channel send takes the Go runtime's lock and `os.File.Write` serializes writers internally, so "zero mutex" / "lock-free" claims were narrowed. Removed the incorrect PIPE_BUF statement and the "channel can reorder" row.
- **Sync mode:** rotation is not supported (EXAMPLES §14 no longer shows it); the channel is no longer described as created.
- **EXAMPLES:** the test helper no longer uses unexported fields; shutdown snippets use `Flush(); cancel(); Close()` order instead of `defer Close()` before `defer cancel()`.
- **BENCH:** added "How to read the async numbers"; marked tables as v1.4.3 measurements; `logs/s` renamed `attempts/s`.

### Fixes

- **Rotation:** `MaxBackupFiles=N` now keeps N backups (it kept N-1; `1` kept none). A failed rename no longer truncates the live log; rotation is skipped and the file keeps being appended to.
- **Sync mode:** no longer opens a second, never-closed file handle and no longer allocates the unused channel. `Flush()` now flushes the buffer of `OSBuffered` / `FsyncEveryN` (and fsyncs for `FsyncEveryN`); `Close()` fsyncs for `FsyncEveryN`. Failed buffered writes/flushes are counted in `Stats().SyncWriteErrors`. `FsyncEveryNCount` now defaults to 100 when 0 (it used to fsync on every write).
- **Durations:** `2.005s` was printed as `2.5s`. Fractions are zero-padded, hours are supported, negative values are formatted correctly.
- **JSON:** `NaN`/`±Inf` floats are emitted as strings (they produced invalid JSON); invalid UTF-8 bytes are escaped as `\ufffd`. The two JSON string encoders were merged into one.
- **Rate limiter:** a goroutine holding a stale timestamp no longer resets a newer window.
- **Context methods:** `DebugCtx`/`InfoCtx`/`WarnCtx`/`ErrorCtx` check the level before reading the context.
- **slog adapter:** no allocation for messages longer than 32 bytes; the caller is taken from `Record.PC`; pooled field slices no longer retain string references.
- **Text output:** no double space before the first field when leading fields are skipped.
- **Pools:** `Entry.Reset` clears pooled slices so they do not keep large strings alive.

### Testing

- `TestDurability_ProcessKill`: a helper process writes in each tier and kills itself with SIGKILL; the parent counts surviving lines (Unix only).
- Regression tests for every fix above; fuzz targets now check JSON round-trips for invalid UTF-8 and `time.ParseDuration` round-trips for durations.
- Benchmarks report `drops/op`; added `BenchmarkE2E_*` and `BenchmarkSyncMode_FsyncEveryN`.
- First measurements with `drops/op` (M1 Pro): ~80% of the attempts of the async benchmarks are dropped by the channel-full path; delivered throughput of a saturating producer is ~3M entries/s (see BENCH.md).

### Known limitations (planned for v1.5.0, not changed here)

- `Close()` does not stop the worker, and `Flush()` before the worker goroutine has started discards entries silently.
- Async timestamps are taken when the worker formats the entry.
- Text output does not escape newlines.

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
