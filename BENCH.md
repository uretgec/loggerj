# loggerj — Benchmark Results & Methodology

Detailed performance analysis for `loggerj`.

**The tables below are the v1.4.4 measurements and were NOT re-run for v1.4.4.** v1.4.4 changed the code paths of the slog adapter, JSON string escaping, durations and the rate limiter; re-run the benchmarks on your hardware before relying on a number.

**Environment:** Apple M1 Pro (10 cores), Go 1.24, `-count=6`, `-race` disabled.
*Note: Benchmark numbers vary ±10% across runs due to thermal throttling and CPU frequency scaling. Treat the order of magnitude as meaningful. Always benchmark on your own hardware.*

## Methodology

```bash
# Run all benchmarks
go test -bench=. -benchmem -run='^$' -count=6 ./...

# CPU profiling
go test -bench=. -cpuprofile=cpu.out -run='^$' ./...
go tool pprof -http=:8080 cpu.out
```

All async benchmarks use `io.Discard` to isolate formatting overhead from I/O latency. Sync Mode benchmarks use real temp files (`b.TempDir()`).

---

## How to read the async numbers

The async benchmarks (`BenchmarkLog_*`) measure the **caller-side cost of one log attempt**: the time to filter, copy the entry and hand it to the channel. The worker goroutine formats and writes in the background. If the producer is faster than the worker, the channel fills and the remaining attempts take the cheap drop path (~19 ns), so the ns/op can include drops.

- Since v1.4.4 every async benchmark reports `drops/op` (channel-full drops only; level, sampling and rate-limit rejections are not drops). A non-zero value means ns/op is not the cost of a delivered log.
- `BenchmarkE2E_NoFields_Text` and `BenchmarkE2E_TypedFields_JSON` include the worker drain (`Flush`) in the timed region and report `delivered/s` and `drops/op`. These are the numbers to compare with competitors' end-to-end costs.
- The `logs/s` column below is `1e9 / ns/op`, i.e. **attempts per second**, not delivered logs per second.
- First `drops/op` / `BenchmarkE2E_*` measurements (v1.4.4, Apple M1 Pro, darwin/arm64, single run, `-benchmem`):

| Benchmark | ns/op | drops/op | delivered/s |
|---|---:|---:|---:|
| `BenchmarkLog_NoFields` | 54.5 | 0.84 | n/a (producer side only) |
| `BenchmarkE2E_NoFields_Text` | 52.9 | 0.83 | ~3.1M |
| `BenchmarkE2E_TypedFields_JSON` | 79.2 | 0.78 | ~2.8M |

  In these tight-loop benchmarks about 80% of the attempts are dropped: the single worker delivers roughly 3M entries/s while the producer attempts ~18M/s. The ns/op figures in the async table below are therefore mostly the cost of the drop path, and the delivered rate with a saturating producer is ~3M entries/s. Real services rarely log at a saturating rate, so measure with your own load.

## 1. Async Mode (Intended Usage)

The async pipeline (channel + worker) is the primary usage pattern for high-throughput services.

| Benchmark | ns/op | attempts/s | allocs/op | Use Case |
|---|---|---|---|---|
| **Filtered** | **~2.1** | **483M** | **0** | Debug logs in production (below threshold) |
| Sampling | ~27 | 37M | 0 | High-volume sampled events (1/10) |
| Dropped | ~19 | 53M | 0 | Channel-full backpressure |
| **RateLimited** | **~41** | **24M** | **0** | High-volume events (Lock-Free CAS) |
| JSON | ~52 | 19M | 0 | Structured logging |
| StringAPI | ~58 | 17M | 0 | String messages (zero-copy) |
| **TypedFields** | **~72** | **14M** | **0** | **Dynamic typed fields (0 allocs)** |
| NoFields | ~54 | 18M | 0 | Simple messages |
| Parallel | ~85 | 12M | 0 | Concurrent logging (10+ goroutines) |
| SubProfile Prefix | ~59 | 17M | 0 | Pre-baked static fields |
| WithCaller | ~464 | 2.2M | 2 | Debugging only (runtime.Caller) |

---

## 2. Sync Mode (Audit Trails)

Sync Mode bypasses the async pipeline. Each tier has a different guarantee (see README, "Durability at a glance"). `OSBuffered` is fast because it does not issue a syscall per entry; compare `Direct` with loggers that write per entry.

| Benchmark | ns/op | logs/s | allocs/op | Durability Guarantee |
|---|---|---|---|---|
| **SyncMode_OSBuffered** | **~111** | **9.0M** | **0** | Only flushed entries survive a process crash (memory copy, no syscall per entry) |
| SyncMode_Direct | ~1566 | 639K | 0 | Survives process crash (one `write(2)` per entry) |
| SyncMode_FsyncEveryN (N=100) | ~51891 (v1.4.4) | ~19K | 0 | Entries up to the last fsync (macOS fsync is slow; Linux will differ) |
| SyncMode_FsyncEveryWrite | ~4.4ms | 227 | 0 | Survives OS crash / power loss if the disk honors fsync |
| SyncMode_Parallel | ~238 | 4.2M | 0 | Concurrent sync (mutex contention) |

*Note on Parallel Sync:* Under parallel load, `OSBuffered` and `FsyncEveryN` tiers hold `syncMu` during BOTH the buffer copy AND the flush/fsync syscall because `bufio.Writer` is not thread-safe. This causes a ~2x slowdown compared to single-threaded sync. We accept this to prevent data loss on `Close()` that would occur with a per-goroutine writer pool.

---

## 3. Rate-Limit Hot-Path Cost Breakdown

loggerj uses a lock-free, single-word CAS design for rate limiting.

| Component | ns/op | % of Total | Allocs |
|---|---:|---:|---:|
| Pure CAS + Bitwise Math (`WithoutTime`) | ~2.5 | ~7% | 0 |
| `time.Now().UnixMilli()` (clock read; vDSO on Linux) | ~34.0 | ~93% | 0 |
| **Total Real-World Cost** | **~36.5** | **100%** | **0** |

### Contention Behavior

| Scenario | ns/op | Behavior |
|---|---:|---|
| **Uncontended** | ~2.5 | Pure CAS, no backoff triggered |
| **Saturated (over-limit)** | ~0.3 | Rejection before CAS. Measured with a fixed timestamp, so it excludes the ~34 ns clock read every real call pays first |
| **High Contention** | ~213-257 | Bounded backoff (`runtime.Gosched()`) prevents CPU spin |

### Why No Coarse-Time Cache?

We intentionally do not use an async-updated coarse timestamp cache. While it would save ~34ns/op, it introduces severe jitter for sub-second rate-limit windows (e.g., a 50ms window might effectively last 40ms or 60ms depending on cache alignment). The ~34ns clock-read cost is accepted to guarantee exact, deterministic window boundaries.

---

## 4. slog.Handler & Standard Library

| Benchmark | ns/op | allocs/op | Description |
|---|---|---|---|
| `SlogHandler_Handle` | ~143 | **0** | Handler internals only (pre-built Record) |
| `AsWriter_Write` (Go 1.22+) | ~70 | **0** | `bytes.TrimRight`, zero-copy |
| `AsWriter_Write` (Go 1.21) | ~70 | **1** | Compiler escape analysis limitation |

---

## 5. Profile Lookup Adaptive Strategy

`getProfile` uses an adaptive strategy:

- **n ≤ 8:** linear scan (~8ns, cache-friendly)
- **n > 8:** `map[string]*SubProfile` (~8ns, O(1))

Threshold 8 is benchmark-driven: map is 2.7x faster than linear at n=16 on Apple M1 Pro.

---

## Reproducing These Results

```bash
git clone https://github.com/uretgec/loggerj.git
cd loggerj
go test -bench=. -benchmem -run='^$' -count=6 ./...
```

For fair apples-to-apples comparisons with `zap` and `zerolog`, see **[COMPARISON.md](COMPARISON.md)**.
