# Changelog

All notable changes to this project will be documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [1.5.2] - 2026-11-05

Hardening release. From this version the design is frozen: bug fixes, Go version support and documentation only.

### Fixed

- **`Flush()` could hang forever.** When the worker was stuck in a blocked writer, the first `Flush` timed out and left its request in the channel; the next `Flush` then blocked on the send without a timeout, holding `flushMu`. The whole call now waits at most one second.
- **One failed write stopped all async output.** A `bufio.Writer` refuses every write after its first error, so a transient error (a full disk, a dropped connection) silenced the logger until restart. The worker now clears the error and writes again when the writer recovers. The entries of the failed batch are lost.
- **Write and rotation errors flooded stderr.** A persistent failure printed a line on every flush. Messages for write errors and for rotation errors are now limited to one per 10 seconds each; the counters keep counting every failure.

### Added

- `Stats.WriteErrors`: failed writes and flushes of the async worker. (An addition in a patch release; the field is new and nothing else changes.)
- `compat` CI workflow: tests on Linux, macOS and Windows with the minimum and the latest Go version. Windows has not been run before; failures there are findings, not regressions.
- Tests: a blocked writer, a failing writer that recovers, and a concurrent load test (meant for `-race`).

### Documentation

- README: "Status and scope" and "Known limitations"; package documentation: caveats about string arguments and message size.
- The Fiber example copied no strings; Fiber and fasthttp values alias buffers that are reused, and the async worker reads them later. The example now copies them.
- ROADMAP: the design is frozen; ideas that need a real workload are listed as not planned, and the v2 ideas are collected there.

### Removed (internal)

- The byte-class lookup tables of the JSON and text escapers (added in 1.5.1): they had no measurable effect. The output is unchanged.
- `BenchmarkAppendJSONString` and `BenchmarkAppendTextString`, which were only added to measure them.

---

## [1.5.1] - 2026-11-02

Performance patch: no API, behavior or output changes. Based on the first Linux profile of v1.5.0 (GitHub-hosted amd64 runner, 4 vCPU).

### Performance

- **Timestamp rendered once per batch.** The worker used to format the timestamp digits for every entry; in the Linux profile `appendTimestamp` was ~15% of the worker's time for text output and ~9% for JSON. The digits are now rendered once per batch and copied. Sync mode still stamps each entry when it is logged.
- **Lookup tables in the string escapers.** The JSON string escaper (`appendJSONStringBytes`, ~26% of the worker's time for JSON output in the profile) and the text escaper test each byte with a table lookup instead of several comparisons. The output is identical (`FuzzAppendJSONString`, `TestAppendTextBytes_Table`, and new table tests). **No measurable effect:** in the v1.5.1 profile `appendJSONStringBytes` is still ~27-30% of the JSON worker's time. The strings are short (keys and values of a few bytes), so the cost is per call, not per byte.
- Measured on the Linux runner against v1.5.0 (`benchstat`, `-count=10`): `E2E_NoFields_Text` delivered/s 4.40M -> 4.78M (+8.6%, p=0.002), `E2E_TypedFields_JSON` 2.77M -> 3.13M (+12.9%, p<0.001); per delivered entry 227 -> 209 ns and 361 -> 320 ns. The gain comes from the timestamp change; the tables did not add a measurable share.

### Repository

- `linux-profile` workflow and `profile-inner.sh` for Linux benchmarks and CPU/mutex profiles (manual run). The workflow also profiles `Log_Enqueue_Slog`.
- `.github/dependabot.yml` keeps the GitHub Actions up to date.
- BENCH.md: Linux amd64 results for v1.5.0.

---

## [1.5.0] - 2026-10-30

Minor release: faster worker, simpler lifecycle, new options, and several behavior changes. Read "Upgrading" before updating a service that depends on log volume or on the exact text output.

### Added

- `StartBackground(ctx)` and `StartBackgroundWithWriter(ctx, w)`: start the worker in its own goroutine and return once it is running, so `Flush()` can be called right away. `Start` / `StartWithWriter` are unchanged (blocking, started with `go`).
- `Logger.Sub(logType, opts...)` and `SubLogger`: a handle to a registered profile (`Debug/Info/Warn/Error`, `...String`, `...Fields`), so a typo in the log type cannot silently lose the profile. It is a safety feature, not a speed-up (see BENCH.md). `Sub` without options reuses an existing profile.
- `Config.DurationUnit`: numeric JSON durations in a chosen unit (default: string form).
- `Config.TimestampFormat`: timestamps as a formatted UTC time instead of Unix milliseconds.
- `Config.FileMode`: permissions for the log file and its rotated replacement (default `0o644`).
- `Config.ErrorBlockTimeout`: optional bounded wait for ERROR entries when the channel is full.
- `Stats.DropsByLevel`: drops per level (`[4]uint64`, indexed by `Level`).
- slog adapter: trace/request/span IDs from the context (`TraceIDKey`, `RequestIDKey`, `SpanIDKey`), like the `*Ctx` methods.
- Benchmarks: `BenchmarkLog_Enqueue_*` (caller-side cost without drops, including `Enqueue_Sub` and `Enqueue_Slog`), `BenchmarkMonoMillis`; `BenchmarkE2E_*` also reports `ns/delivered`.

### Performance

- **Worker batch drain:** the worker formats up to 128 already-queued entries per pass instead of going through its four-channel `select` for every entry, and reads the clock once per batch. On the v1.4.4 profile (Apple M1 Pro, saturating producer) `select` was ~57% and `time.Now` ~23% of the worker's time, formatting ~3%. Measured with `benchstat` (`-count=6`, p=0.002): `BenchmarkE2E_NoFields_Text` delivered/s 3.52M -> 5.28M (+50%), `drops/op` 0.80 -> 0.18; `BenchmarkE2E_TypedFields_JSON` delivered/s 2.50M -> 3.61M (+44%), `drops/op` 0.79 -> 0.64. Sync-mode benchmarks and `BenchmarkLog_Enqueue_*` are unchanged. The `ns/op` of the async `BenchmarkLog_*` benchmarks rises because fewer attempts take the cheap drop path.
- **Rate-limit clock:** the limiter reads the monotonic clock only (`time.Since` on a fixed epoch) instead of `time.Now()`. Windows no longer move with wall-clock adjustments. On an Apple M1 Pro the clock read dropped from 33.8 ns (`time.Now().UnixMilli()`) to 16.4 ns (`BenchmarkMonoMillis`), and `BenchmarkLog_RateLimited` from 38.2 ns to 25.2 ns.
- **Sampling before the rate limit:** entries discarded by sampling no longer pay for the limiter's clock read.
- The slog adapter skips the attribute conversion for filtered records and no longer allocates for messages longer than 32 bytes (v1.4.4).
- Removed the `//go:nosplit` pragmas (no measured benefit). `BenchmarkLog_Filtered` and `BenchmarkLog_Enqueue_*` are unchanged without them.

### Changed (behavior)

- **Gate order:** sampling now runs before the rate limit (it was the other way round in v1.4.x). `WithRateLimit` therefore caps what is written, and entries discarded by sampling no longer use up the limit. With `WithRateLimit(100, time.Second)` + `WithSampleRate(10)` and 500 attempts/s the output changes from ~10 to 50 logs/s.
- **`Close()` stops the worker.** It cancels the worker, waits (up to 5 seconds) for it to drain the channel and exit, then flushes and closes the file. `StartBackground(ctx)` + `defer Close()` is a complete lifecycle; `Flush(); cancel(); Close()` still works. `Close()` is idempotent (later calls return nil).
- **`Flush()` without a running worker no longer discards the queued entries.** It returns immediately and the entries are written when a worker starts (or counted in `Drops()` if the channel fills up first).
- **Text output escapes control characters** in messages and field values: `\n` and `\r` are written as the two characters `\n` and `\r`, other control characters and DEL as `\xNN`; tabs are kept. A value can no longer start a new log line. Keys are not escaped.
- **`WithFields` keys `ts`, `level`, `type`, `msg`, `file`, `line` and `fields` are ignored** with a warning (they produced duplicate JSON keys).
- Entries formatted in the same batch carry the same `ts` (millisecond resolution; a batch is formatted within microseconds). Sync mode still stamps at call time.
- `Flush()` writes at `FlushThreshold` while draining instead of accumulating the whole channel in the buffer first. Rotation is still only checked by the worker's threshold and ticker writes, not by `Flush()`.
- The slog adapter's `Handle` returns early for records below the logger's level.

### Deprecated

- `Config.RateLimitWindow`: `WithRateLimit` always sets its own window. The field is kept for compatibility.

### Repository

- `loggerj.go` was split into topic files (`config.go`, `profile.go`, `field.go`, `entry.go`, `logger.go`, `api.go`, `core.go`, `worker.go`, `format.go`, `rotate.go`, `ratelimit.go`, `sublogger.go`); no declaration was changed by the split.
- `hotpath.txt` now protects `Log_Filtered`, `Log_RateLimited` and the `Log_Enqueue_*` benchmarks (including `Enqueue_Slog`). The drop-mixed `BenchmarkLog_*` benchmarks are informational: their `ns/op` depends on how fast the worker is.

### Upgrading

1. If you combine `WithSampleRate` and `WithRateLimit`, expect more output; use `WithRateLimit(limit/sampleRate, window)` for the old volume.
2. If you parse the text format, note the escaping of control characters.
3. If you registered `WithFields` keys that are now reserved, rename them.
4. The previous shutdown order keeps working; you can replace it with `StartBackground` + `defer Close()`.

### Not included (see ROADMAP.md)

Caller resolution in the worker, a ring-buffer queue, rotation in sync mode, a shorter `syncMu` hold time, nested JSON objects, OpenTelemetry and redaction hooks.

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
