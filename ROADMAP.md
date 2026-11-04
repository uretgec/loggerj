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

## 🔮 Next (not scheduled)

### Measure first

- [x] Profile the async path on Linux (v1.5.0, 4 vCPU runner). The worker spends ~48% of its time in `chanrecv` and ~15% in `sync.Pool.Put`, formatting ~31%; the producer ~54% in `chansend` and ~20% in `sync.Pool.Get`; the mutex profile is entirely the runtime's channel lock. This is the case for the ring buffer below.
- [ ] Profile `Log_Enqueue_Slog` (138 ns on the Linux runner, 80 ns on the M1): where does the time go (attribute conversion, pool, `ctx.Value`)?
- [ ] Defer caller resolution to the worker: store the PC and resolve it with `runtime.CallersFrames`. Verify how `runtime.Callers` with a one-element buffer behaves for inlined frames across Go 1.21-1.24 before shipping.
- [ ] Bounded MPSC ring buffer of pre-allocated entries instead of channel + `sync.Pool` (the v1.4.4 profile showed channel lock and pool contention).
- [ ] Rotation check on the `Flush()` path.

### Sync mode

- [ ] Size-based rotation in sync mode.
- [ ] Shorter `syncMu` hold time: swap the buffer under the lock, write and fsync outside it. Group commit only if that is not enough.

### Output

- [ ] Nested JSON objects (`WithNestedFields` baked at init; allocating encoder interface only for sync mode).
- [ ] OpenTelemetry trace context behind a build tag; Prometheus collector as a sub-package.
- [ ] Redaction: a static key set applied by the worker, rather than per-call hooks.

### Dropped

- Multi-writer fan-out: `io.MultiWriter` with `StartWithWriter` already does it.
- Network shipping: out of scope.
- Plugin / Core interface: conflicts with the zero-overhead design.

---

## Guiding Principles

1. **Zero-allocation hot path is non-negotiable.** Every feature is measured.
2. **Zero external dependencies.** Rotation, rate limiting, everything in-house.
3. **Honest positioning.** We document where we lose, not just where we win.
4. **Think twice, build once.** Each step ships with a benchmark gate.
5. **Sync mode is fixed at creation.** No runtime mode switching.
