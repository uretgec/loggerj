# loggerj Roadmap

Living document. Ordered by strategic leverage, not calendar dates.
Every item ships with a benchmark gate — no item lands if it regresses the hot path.

---

## 🔧 v1.4.0 — In Planning

Theme: **Trust, Validation, Completeness.**

v1.3.x added performance features. v1.4.0 must *prove* those claims under
automation and *close* the honest gaps documented in COMPARISON.md.

### Phase 1: Validation Infrastructure (P0)

Without automation, every performance and safety claim is unverified.

- [ ] **CI benchmark automation (benchstat)** — GitHub Actions runs the full
      benchmark suite on every PR and posts a `benchstat` diff comment.
      Any hot-path regression >10% blocks merge.
- [ ] **Go version matrix CI** — test on Go 1.21–1.24 (and tip).
      Required because `unsafeStringToBytes` is sensitive to compiler changes.
- [ ] **Fuzzing targets** — `go test -fuzz` for `appendJSONString`,
      `appendDuration`, and rate-limit window calculation.
- [ ] **Coverage reporting** — publish coverage; track untested branches
      in sync-mode and rotation paths.

### Phase 2: Hot-Path Honesty (P1)

Close the gaps that partially undermine the "zero-cost hot path" claim.

- [ ] **CAS backoff** — bounded retry with `runtime.Gosched()` after N failed
      CAS attempts in `checkAtomicRateLimit`. Eliminates unbounded spin.
- [ ] **Publish HighContention benchmark** — report
      `BenchmarkLog_RateLimited_HighContention` in BENCH.md with per-core analysis.
- [ ] **Measure & document `time.Now()` rate-limit cost** — quantify the vDSO
      cost. Decide: document as accepted cost, or design a coarse-time cache.

### Phase 3: Structured Logging Completeness (P1)

The two biggest feature losses are nesting and slog-semantic fidelity.

- [ ] **Nested field design spike** — research a `GroupField` / recursive
      `Field` design that emits real nested JSON objects. Must stay zero-alloc
      or the feature is rejected.
- [ ] **Real `slog.Group` nesting** — depends on nested fields landing.
      Stop flattening groups to dotted keys; emit true nested JSON.
- [ ] **JSON `FlatStaticFields` option** — lift pre-baked fields to top level
      for Loki/ES pipeline compatibility.

### Phase 4: Maturity & Documentation (P2)

- [ ] **Release cadence policy** — publish semver discipline and breaking-change
      policy. Minimum stabilization window between minor releases.
- [ ] **Production readiness checklist** — explicit "safe to adopt" vs "wait"
      guidance based on workload type.
- [ ] **`unsafe` security review** — formal review of `unsafeStringToBytes`
      and `floatToBits` against the Go memory model; document invariants.

### Acceptance Gates (all must pass before v1.4.0 ships)

- ✅ CI runs benchstat on every PR; no unreviewed hot-path regression merges
- ✅ All tests green across Go 1.21–1.24 matrix
- ✅ Fuzzers run ≥1h clean on each target
- ✅ No async benchmark regresses >10% in ns/op
- ✅ HighContention benchmark published with per-core analysis
- ✅ CAS backoff lands without regressing single-thread rate-limit cost

---

## 🔮 v1.5.0 — Future

Theme: **Ecosystem Integration & Production Durability.**

v1.4.0 proves the core claims. v1.5.0 integrates loggerj into the broader
Go observability ecosystem and adds production-grade durability features.

### Phase 1: Coarse-Time Cache (P0)

Optional opt-in flag that trades rate-limit window precision for hot-path speed.

- [ ] **`Config.RateLimitCoarseTime`** — async-updated coarse timestamp for
      rate limiting. Hot path reads `atomic.Int64` (~1ns) instead of
      `time.Now()` (~34ns).
- [ ] **`Config.RateLimitCoarseInterval`** — refresh interval (default 10ms).
- [ ] **Benchmark gate** — coarse-time rate-limit hot path <5ns/op.
- [ ] **Documentation** — warn that sub-second windows <50ms are not
      recommended with coarse time.

### Phase 2: True Nested JSON Objects (P1)

New buffer architecture for real nested JSON without allocation.

- [ ] **Pre-compiled nested profiles** — `WithNestedFields` for static nested
      JSON baked at init time. Hot-path cost: 0ns (memcpy of pre-baked prefix).
- [ ] **`ObjectEncoder` interface** — zap-compatible dynamic nested JSON for
      sync mode. Allocates; documented trade-off.
- [ ] **Benchmark vs zap.Object / zerolog.Dict** — publish honest comparison.

### Phase 3: OpenTelemetry & Observability (P1)

- [ ] **OTel trace context extraction** — opt-in via build tag. Zero cost
      when not compiled in.
- [ ] **Prometheus metrics collector** — optional sub-package exporting
      `loggerj_drops_total`, `loggerj_channel_size`, etc.

### Phase 4: Hook System & PII Masking (P2)

- [ ] **Pre-format hook API** — `AddHook(func(e *Entry))` runs on caller's
      goroutine. Zero cost when no hooks registered.
- [ ] **Field-level hook API** — `AddFieldHook(func(f Field) Field)` for
      per-field masking (e.g., PII redaction).

### Phase 5: Advanced Output (P2)

- [ ] **Multi-writer fan-out** — `Config.Writers []io.Writer` for simultaneous
      stderr + file + network output. Caller-side cost: 0ns.
- [ ] **Network shipping** — optional `loggerj/network` sub-package for
      TCP/UDP log shipping with reconnection.

### Deferred (research, not committed)

- [ ] **Group Commit (Option B)** — leader/follower batching so sync logging
      gets faster under concurrency (N logs → 1 `writev`). Significant
      correctness undertaking; research spike first.
- [ ] **Burst-then-decay sampling** — token bucket for legitimate spikes.
- [ ] **Blocking drop policy** — optional `Config.BlockOnFull` for audit cases.
- [ ] **Multi-worker fan-out** — multiple format workers; ordering trade-off.
- [ ] **Plugin / Core interface** — intentionally deferred; revisit only if
      there is real demand, as it conflicts with zero-overhead design.

### Acceptance Gates (all must pass before v1.5.0 ships)

- ✅ Coarse-time cache opt-in, <5ns rate-limit hot path when enabled
- ✅ Pre-compiled nested profiles: 0ns hot-path cost
- ✅ ObjectEncoder for sync mode: documented allocation trade-off
- ✅ OTel trace extraction: 0ns when disabled via build tag
- ✅ Prometheus collector: optional sub-package, no runtime dependency
- ✅ Pre-format hook API: 0ns when no hooks registered
- ✅ Multi-writer fan-out: 0ns caller-side cost
- ✅ All hot-path benchmarks pass benchstat gate (>10% regression blocked)
- ✅ Backward compatibility maintained (semver)

---

## Guiding Principles

1. **Zero-allocation hot path is non-negotiable.** Every feature is measured.
2. **Zero external dependencies.** Rotation, rate limiting, everything in-house.
3. **Honest positioning.** We document where we lose, not just where we win.
4. **Think twice, build once.** Each step ships with a benchmark gate.
5. **Sync mode is fixed at creation.** No runtime mode switching — this keeps both paths lean and branch-predictor friendly.
