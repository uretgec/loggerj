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

## 🔮 v1.5.0 — Future

Theme: **Ecosystem Integration & Production Durability.**

### Phase 0: Gate Order — Sampling First (decided)

- [ ] **Run sampling before the rate limit.** Sampling then costs one atomic add, the clock read is only paid for sampled entries, and `WithRateLimit` caps the output (`min(input/sampleRate, limit)`). This changes the output of profiles that use both options, so it is a behavior change: update `TestLog_RateLimitAndSampling_Order`, godoc, EXAMPLES §7 and COMPARISON, and call it out in the changelog.

### Phase 1: Coarse-Time Cache (P0)

- [ ] **`Config.RateLimitCoarseTime`** — Opt-in async-updated coarse timestamp for rate limiting (~1ns read vs 34ns vDSO).
- [ ] **Documentation** — Warn that sub-second windows <50ms are not recommended with coarse time.

### Phase 2: True Nested JSON Objects (P1)

- [ ] **Pre-compiled nested profiles** — `WithNestedFields` for static nested JSON baked at init time (0ns hot-path cost).
- [ ] **`ObjectEncoder` interface** — zap-compatible dynamic nested JSON for sync mode (allocates; documented trade-off).

### Phase 3: OpenTelemetry & Observability (P1)

- [ ] **OTel trace context extraction** — Opt-in via build tag. Zero cost when not compiled in.
- [ ] **Prometheus metrics collector** — Optional sub-package exporting `loggerj_drops_total`, etc.

### Phase 4: Hook System & PII Masking (P2)

- [ ] **Pre-format hook API** — `AddHook(func(e *Entry))` runs on caller's goroutine.
- [ ] **Field-level hook API** — `AddFieldHook(func(f Field) Field)` for per-field masking (PII redaction).

### Phase 5: Advanced Output (P2)

- [ ] **Multi-writer fan-out** — `Config.Writers []io.Writer` for simultaneous stderr + file + network output.
- [ ] **Network shipping** — Optional `loggerj/network` sub-package for TCP/UDP log shipping.

### Candidates from the v1.4.3 review (not decided yet)

- [ ] `Close()` stops and waits for the worker (idempotent); a way to wait for worker start so `Flush()` cannot discard entries.
- [ ] Cheaper rate-limit clock (monotonic `time.Since` instead of `time.Now`) before considering a coarse-time cache.
- [ ] Per-profile handle API (`logger.Sub(...)`) to skip the profile lookup and avoid typos in the log type.
- [ ] Escape newlines in text output; reserved-key check in `WithFields`.
- [ ] Per-level drop counters or an option to never drop errors.
- [ ] Defer caller resolution (`runtime.Callers` PC) to the worker.
- [ ] Numeric durations in JSON and a configurable timestamp format.

### Deferred (Research Only)

- [ ] **Group Commit (Option B)** — Leader/follower batching for sync logging (`writev`).
- [ ] **Plugin / Core interface** — Intentionally deferred; conflicts with zero-overhead design.

---

## Guiding Principles

1. **Zero-allocation hot path is non-negotiable.** Every feature is measured.
2. **Zero external dependencies.** Rotation, rate limiting, everything in-house.
3. **Honest positioning.** We document where we lose, not just where we win.
4. **Think twice, build once.** Each step ships with a benchmark gate.
5. **Sync mode is fixed at creation.** No runtime mode switching.
