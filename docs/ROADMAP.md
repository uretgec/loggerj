# loggerj Roadmap

Living document. Ordered by strategic leverage. Every item ships with a benchmark gate — no item lands if it regresses the hot path.

---

## ✅ v1.4.x — Completed

Theme: **Trust, Validation, Completeness.** (v1.4.0 through v1.4.3)

### Validation Infrastructure

- [x] **CI benchmark automation (benchstat)** — GitHub Actions blocks PRs with >10% hot-path regression.
- [x] **Go version matrix CI** — Tested on Go 1.21–1.24.
- [x] **Fuzzing targets** — `appendJSONString`, `appendDuration`, rate-limit window calculation.
- [x] **Coverage reporting** — Sync-mode and rotation paths covered.

### Hot-Path Honesty & Rate-Limit Refactor

- [x] **CAS backoff** — Bounded retry with `runtime.Gosched()` after 8 failed CAS attempts.
- [x] **Profile-relative 40-bit window index** — Eliminated epoch-based overflow risks for sub-millisecond windows.
- [x] **Exact rate limits capped at 16,777,215** — Due to 24-bit counter limit.
- [x] **Documented `time.Now()` vDSO cost** (~34ns). Decided against coarse-time cache to preserve sub-second window accuracy.

### Ecosystem & Completeness

- [x] **slog.Group flattening** — Multi-key groups correctly emit dotted keys.
- [x] **`math.Float64bits`** — Replaced `unsafe.Pointer` type-punning with compiler intrinsics.
- [x] **SyncMode mutex disclosure** — Documented that `syncMu` is held during flush/fsync syscalls for buffered tiers.
- [x] **AsWriter Go version notes** — Clarified 0-alloc on Go 1.22+ vs 1-alloc on Go 1.21.

---

## ✅ v1.4.4 — Completed

Theme: **Correctness of docs and sync mode.** See CHANGELOG.md for the list. Behavior of the gate order was intentionally not changed.

---

## ✅ v1.5.0 — Completed

Theme: **Throughput, lifecycle and cleanup.** See CHANGELOG.md.

- [x] Worker batch drain with one clock read per batch (measured +44% to +50% delivered/s).
- [x] Sampling before the rate limit.
- [x] `StartBackground`, `Close()` stops the worker, `Flush()` keeps entries when no worker runs.
- [x] Monotonic clock for the rate limiter (replaces the coarse-time cache idea; no cache, no jitter).
- [x] `Sub` / `SubLogger` handle.
- [x] `DurationUnit`, `TimestampFormat`, `FileMode`, `ErrorBlockTimeout`, `Stats.DropsByLevel`.
- [x] Text output escapes control characters; reserved `WithFields` keys are ignored.
- [x] slog adapter reads context IDs.
- [x] Drop-free `Log_Enqueue_*` benchmarks as regression gates; `loggerj.go` split into topic files.

---

## ✅ v1.5.1 and v1.5.2 — Completed

- [x] v1.5.1: timestamp rendered once per batch (+9% to +13% delivered/s on the Linux runner).
- [x] v1.5.2: hardening. `Flush()` is bounded, async write errors are counted (`Stats.WriteErrors`) and the worker recovers from them, stderr messages are rate-limited, caveats are documented, a CI compatibility job covers Windows, macOS and newer Go versions.

**The design is frozen from v1.5.2**: bug fixes, Go version support and documentation only. Everything below needs a real workload that asks for it.

---

## Not planned unless a real workload needs it

### Performance (measure first)

- [ ] Bounded MPSC ring buffer of pre-allocated entries instead of channel + `sync.Pool`. The Linux profile of v1.5.0/v1.5.1 shows the worker spending over 60% of its time in queue operations (`chanrecv`, `sync.Pool.Put`) and the producer about 58% in `chansend` plus 24% in `sync.Pool.Get`; the mutex profile is entirely the runtime's channel lock. It needs a written design (wake-up of an idle worker, `Flush`/`Close`, `ChannelSize`, drop semantics) and a prototype that beats the channel by a wide margin before it is integrated.
- [ ] Defer caller resolution to the worker: store the PC and resolve it with `runtime.CallersFrames`. Verify how `runtime.Callers` with a one-element buffer behaves for inlined frames across Go versions first.
- [ ] Pre-bake `"type":"..."` per profile (one escaper call less per JSON entry); the escaper cost is per call on short strings, not per byte.
- [ ] Rotation check on the `Flush()` path.

### Behavior

- [ ] Copy string arguments at call time (safe by default for fasthttp/Fiber values), with an opt-out. Costs time on the calling goroutine.
- [ ] `Config.MaxMessageBytes` to bound the memory held by the queue.
- [ ] Timestamp taken at call time (opt-in). Costs one clock read per call.

### Sync mode

- [ ] Size-based rotation in sync mode.
- [ ] Shorter `syncMu` hold time: swap the buffer under the lock, write and fsync outside it. Group commit only if that is not enough.

### Output

- [ ] Nested JSON objects (`WithNestedFields` baked at init; an allocating encoder interface only for sync mode).
- [ ] OpenTelemetry trace context behind a build tag; Prometheus collector as a sub-package.
- [ ] Redaction: a static key set applied by the worker, rather than per-call hooks.

### Dropped

- Multi-writer fan-out: `io.MultiWriter` with `StartWithWriter` already does it.
- Network shipping: out of scope.
- Plugin / Core interface: conflicts with the zero-overhead design.

---

## v2 ideas (not planned)

Collected so that they are not forgotten. A v2 needs real user feedback, a `/v2` module path and a migration guide. None of this is scheduled.

1. Module path `.../loggerj/v2`.
2. Stop exporting `Entry`, `SubProfile`, `Entry.Reset`, `StdLogWriter` and the `FieldType` constants.
3. Remove the deprecated `Config.RateLimitWindow`.
4. `Start` does not block (`StartBackground` becomes `Start`); same for `StartWithWriter`.
5. The default sync tier is `Direct` (today it is `OSBuffered`, the weakest).
6. String arguments are copied at call time by default; a no-copy mode is opt-in.
7. The timestamp is taken at call time by default.
8. A consistent JSON layout: static and per-call fields in the same place, configurable key names.
9. Text output quotes values with spaces (logfmt style).
10. `RegisterSub(logType string, ...)` is replaced by the typed `Sub` handle only.
11. `SetLevelValue` / `GetLevel` become `SetLevel` / `Level`.
12. The slog adapter moves to its own package (`loggerj/slog`), so the core does not depend on slog.
13. A default message size limit (`MaxMessageBytes`).
14. The internal queue (for example a ring buffer) can be replaced at that point; it does not change the API, but a major release is a good time to share the risk.

---

## Guiding Principles

1. **Zero-allocation hot path is non-negotiable.** Every feature is measured.
2. **Zero external dependencies.** Rotation, rate limiting, everything in-house.
3. **Honest positioning.** We document where we lose, not just where we win.
4. **Think twice, build once.** Each step ships with a benchmark gate.
5. **Sync mode is fixed at creation.** No runtime mode switching.
