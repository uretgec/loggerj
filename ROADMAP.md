# loggerj Roadmap

Living document. Ordered by strategic leverage, not calendar dates.
Every item ships with a benchmark gate — no item lands if it regresses the hot path.

---

## ✅ v1.3.1 — Shipped (2026-08-07)

Theme: **Ecosystem integration, observability, and honest documentation.**

### P0 — Completed

- [x] **slog.Handler adapter** (`loggerj/slog`)
      - Routes `slog` calls through loggerj's zero-allocation typed-field pipeline
      - `slog.Attr` → `Field` conversion is boxing-free (slog.Value is already tagged union)
      - `WithAttrs` pre-converts attributes once (cold path); `Handle()` only converts per-record attrs
      - `WithGroup` flattens nested groups into dotted keys (e.g., "outer.inner")
      - **Benchmark:** `BenchmarkSlogHandler_Handle`: **143 ns/op, 0 allocs/op**
      - Matches zap's slog bridge performance while maintaining loggerj's async throughput

- [x] **getProfile adaptive map fallback** (threshold n > 8)
      - Small registries (n ≤ 8): linear scan (~8ns, cache-friendly)
      - Large registries (n > 8): map O(1) lookup (~8ns, scales to thousands)
      - Benchmark-driven threshold: map is 2.7x faster than linear at n=16 on Apple M1 Pro
      - Incremental map updates on `RegisterSub` (O(1) clone + apply change)
      - **Benchmark:** `BenchmarkGetProfile_MapLookupDirect`: **~8 ns/op** (same as linear)

- [x] **syncWriteErrors observability**
      - New `Stats.SyncWriteErrors` field counts failed write(2) calls in sync mode
      - Every failed write is logged to stderr with tier information
      - Audit-oriented users can now detect log loss via `Stats()` polling
      - Wraps `syncFile.Write()` and `syncFile.Sync()` with error counting

- [x] **Documentation honesty fixes**
      - BENCH.md: Changed "Lock-Free O_APPEND" → "Sync Mode (Shared-Buffer, Brief Mutex)"
      - Clarified: only `Direct` and `FsyncEveryWrite` are truly lock-free
      - `OSBuffered` and `FsyncEveryN` use `syncMu` during buffer copy (~5ns), not syscall
      - README.md: Sync mode feature description updated with honest mutex disclosure
      - README/BENCH.md: Benchmark numbers synchronized (NoFields ~53ns, Parallel ~85ns, etc.)

### P1 — Completed

- [x] **README.md updated** — Sync mode mutex disclosure, benchmark sync
- [x] **BENCH.md updated** — Honest lock-free status per tier
- [x] **EXAMPLES.md updated** — slog.Handler usage examples (if added)

### P2 — Deferred to v1.4.0

- [ ] **CI benchmark automation** — GitHub Actions with `benchstat` for PR diffs
- [ ] **Burst-then-decay sampling** — token bucket for legitimate traffic spikes
- [ ] **JSON `FlatStaticFields` option** — lift pre-baked fields to top level for Loki/ES

### Acceptance Gates — All Met

- ✅ `BenchmarkSlogHandler_Handle`: 143 ns/op, 0 allocs/op (target: <150ns)
- ✅ `BenchmarkGetProfile_MapLookupDirect`: ~8 ns/op (target: <10ns)
- ✅ `TestSyncWriteErrors`: PASS (counter > 0 after closed file write)
- ✅ All 75+ tests PASS under `-race` (0 DATA RACE)
- ✅ README/BENCH.md benchmark numbers synchronized

---

## 🔧 v1.4.0 — In Planning

Theme: **Trust, Validation, Completeness.**
v1.3.x added performance features. v1.4.0 must *prove* those claims under
automation and *close* the honest gaps documented in COMPARISON.md. Every
item ships with a benchmark gate — no item lands if it regresses the hot path.

### Phase 1: Validation Infrastructure (P0) — the trust foundation

Without automation, every performance and safety claim is unverified.

- [ ] **CI benchmark automation (benchstat)** — GitHub Actions runs the full
      benchmark suite on every PR and posts a `benchstat` diff comment
      (baseline vs PR). Any hot-path regression >10% blocks merge.
      This replaces hand-maintained README/BENCH.md numbers with
      machine-verified ones.
- [ ] **Go version matrix CI** — test on Go 1.21, 1.22, 1.23, 1.24 (and tip).
      Required because `unsafeStringToBytes` (`unsafe.StringData` +
      `go:nosplit`) is sensitive to compiler/runtime changes. Catches
      per-version regressions before release.
- [ ] **Fuzzing targets** — `go-fuzz` / `go test -fuzz` for:
      `appendJSONString` (escaping correctness), `appendDuration`,
      and the rate-limit window calculation. Targets the two highest-risk
      parsers.
- [ ] **Coverage reporting** — publish coverage; track untested branches
      in the sync-mode and rotation paths.

### Phase 2: Hot-Path Honesty (P1) — fix what we claimed

Close the gaps that partially undermine the "zero-cost hot path" claim.

- [ ] **CAS backoff** — add bounded retry with `runtime.Gosched()` (or
      exponential pause) after N failed CAS attempts in
      `checkAtomicRateLimit`. Eliminates unbounded spin under extreme
      contention. Gate: HighContention benchmark must not regress.
- [ ] **Publish HighContention benchmark** — report
      `BenchmarkLog_RateLimited_HighContention` results in BENCH.md with a
      per-core-scale analysis. We already run this test but never published it.
- [ ] **Measure & document `time.Now()` rate-limit cost** — quantify the
      vDSO cost currently paid on every rate-limited call. Decide: document
      as accepted cost, or design a coarse-time cache.
- [ ] **Coarse-time cache (design spike)** — *exploratory, may defer to
      v1.5.0.* Optional config flag that uses an async-updated coarse
      timestamp for rate limiting, trading window precision for hot-path
      speed. Must not silently weaken rate-limit accuracy.

### Phase 3: Structured Logging Completeness (P1) — close the feature gap

The two biggest feature losses are nesting and slog-semantic fidelity.

- [ ] **Nested field design spike** — research a `GroupField` / recursive
      `Field` design that emits real nested JSON objects. Compare against
      `zap.Object` and `zerolog.Dict()` on both ergonomics and alloc cost.
      Must stay zero-alloc or the feature is rejected.
- [ ] **Benchmark vs zap.Object / zerolog.Dict** — publish an honest
      nesting-performance comparison before implementing.
- [ ] **Real `slog.Group` nesting** — *depends on nested fields landing.*
      Once nested objects exist, stop flattening groups to dotted keys and
      emit true nested JSON, making the slog adapter a faithful drop-in.
- [ ] **JSON `FlatStaticFields` option** — lift pre-baked fields to the top
      level for Loki/ES pipeline compatibility.

### Phase 4: Maturity & Documentation (P2)

Address the release-cadence and trust signals directly.

- [ ] **Release cadence policy** — publish a semver discipline and
      breaking-change policy. Commit to a minimum stabilization window
      between minor releases to counter the "three releases in one day" signal.
- [ ] **Production readiness checklist** — explicit "safe to adopt" vs
      "wait" guidance based on workload type.
- [ ] **`unsafe` security review** — formal review of `unsafeStringToBytes`
      and `floatToBits` against the Go memory model; document invariants.

### Deferred to v1.5.0+ (research, not committed)

- [ ] **Group Commit (Option B)** — leader/follower batching so sync logging
      gets *faster* under concurrency (N logs → 1 `writev`). Significant
      correctness undertaking; research spike first.
- [ ] **Burst-then-decay sampling** — token bucket for legitimate spikes.
- [ ] **Blocking drop policy** — optional `Config.BlockOnFull` for audit cases.
- [ ] **Multi-worker fan-out** — multiple format workers; ordering trade-off.
- [ ] **Plugin / Core interface** — intentionally deferred; revisit only if
      there is real demand, as it conflicts with the zero-overhead design.

### Acceptance Gates (all must pass before v1.4.0 ships)

- ✅ CI runs benchstat on every PR; no unreviewed hot-path regression merges
- ✅ All tests green across Go 1.21–1.24 matrix
- ✅ Fuzzers run ≥1h clean on each target
- ✅ No async benchmark regresses >10% in ns/op
- ✅ HighContention benchmark published with per-core analysis
- ✅ CAS backoff lands without regressing single-thread rate-limit cost

---

## Guiding Principles

1. **Zero-allocation hot path is non-negotiable.** Every feature is measured.
2. **Zero external dependencies.** Rotation, rate limiting, everything in-house.
3. **Honest positioning.** We document where we lose, not just where we win.
4. **Think twice, build once.** Each step ships with a benchmark gate.
5. **Sync mode is fixed at creation.** No runtime mode switching — this keeps
   both paths lean and branch-predictor friendly.
