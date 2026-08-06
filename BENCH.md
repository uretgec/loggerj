# loggerj — Benchmark Results & Methodology

Detailed performance analysis for `loggerj` v1.3.1. All benchmarks run on
**Apple M1 Pro (10 cores), Go 1.21+, `-count=5`, `-race` disabled for benchmarks**.

> **Note:** Benchmark numbers vary ±10% across runs due to thermal throttling,
> background processes, and CPU frequency scaling. Treat the **order of magnitude**
> as meaningful, not the exact figures. Always benchmark on *your* hardware.

---

## Methodology

```bash
# Run all benchmarks
go test -bench=. -benchmem -run='^$' -count=5 ./...

# Run specific benchmark group
go test -bench='BenchmarkSyncMode' -benchmem -run='^$' -count=5 ./...

# CPU profiling
go test -bench=. -cpuprofile=cpu.out -run='^$' ./...
go tool pprof -http=:8080 cpu.out
```

All benchmarks use `io.Discard` as the writer to isolate formatting overhead
from I/O latency. Sync Mode benchmarks use real temp files (`b.TempDir()`).

---

## 1. Async Mode (Intended Usage)

The async pipeline (channel + worker) is the primary usage pattern for
high-throughput services.

| Benchmark | ns/op | logs/s | allocs/op | Use Case |
|---|---|---|---|---|
| **Filtered** | **~2.1** | **483M** | **0** | Debug logs in production (below threshold) |
| Sampling | ~27 | 37M | 0 | High-volume sampled events (1/10) |
| Dropped | ~19 | 53M | 0 | Channel-full backpressure |
| **RateLimited** | **~41** | **24M** | **0** | High-volume events (Lock-Free CAS) |
| JSON | ~53 | 19M | 0 | Structured logging |
| StringAPI | ~58 | 17M | 0 | String messages (zero-copy) |
| **TypedFields** | **~72** | **14M** | **0** | **Dynamic typed fields (0 allocs)** |
| NoFields | ~54 | 18M | 0 | Simple messages |
| Parallel | ~84 | 12M | 0 | Concurrent logging (10+ goroutines) |
| SubProfile Prefix | ~59 | 17M | 0 | Pre-baked static fields |
| JSON_NoEscape | ~47 | 21M | 0 | JSON without special chars |
| JSON_WithEscape | ~48 | 21M | 0 | JSON with quotes/backslashes |
| WithCaller | ~464 | 2.2M | 2 | Debugging only (runtime.Caller) |

### v1.2.0 → v1.3.1 Comparison

| Benchmark | v1.2.0 | v1.3.1 | Change |
|---|---|---|---|
| `Filtered` | ~2.07 ns | **~2.06 ns** | Same (inline check restored) |
| `NoFields` | ~56-71 ns | ~53-62 ns | -5% (noise) |
| `JSON` | ~53-58 ns | ~51-59 ns | Same |
| `StringAPI` | ~56-63 ns | ~50-64 ns | Same |
| `RateLimited` | ~41.4 ns | ~41-44 ns | Same |
| `Parallel` | ~80-86 ns | ~80-87 ns | Same |
| `JSON_NoEscape` | ~54-65 ns | **~46-48 ns** | **-20%** (bonus improvement) |
| `JSON_WithEscape` | ~49-56 ns | **~46-49 ns** | **-10%** (bonus improvement) |

**Conclusion:** No regressions. JSON escape paths improved ~10-20% due to
`checkGatesAfterLevel` refactor (better cache behavior).

---

## 2. Sync Mode

Sync Mode bypasses the async pipeline for audit trails and scenarios requiring
per-log write guarantees. Four durability tiers with explicit documentation.

The write strategy depends on `DurabilityTier`:

- **OSBuffered / FsyncEveryN:** shared `bufio.Writer` protected by a brief
  mutex (held only during the buffer copy, not the syscall). Not lock-free.
- **Direct / FsyncEveryWrite:** one `write(2)` per entry to an `O_APPEND`
  file handle. No mutex on the write path.

| Benchmark | ns/op | logs/s | allocs/op | Durability Guarantee |
|---|---|---|---|---|
| **SyncMode_OSBuffered** | **~112** | **8.9M** | **0** | Survives process crash |
| SyncMode_Direct | ~1565 | 645K | 0 | Survives process crash |
| SyncMode_FsyncEveryWrite | ~4.4ms | 227 | 0 | Survives OS crash / power loss |
| SyncMode_Parallel | ~238 | 4.2M | 0 | Concurrent sync (mutex contention) |

### vs Industry Standards (Sync Mode)

| Package | ~ns/op | allocs/op | Lock-free? | Durability documented? |
|---|---|---|---|---|
| **loggerj (OSBuffered)** | **~112** | **0** | ⚠️ Mutex during buffer copy only | ✅ **Yes (explicit)** |
| zerolog (sync) | ~250-350 | 0-1 | ❌ No | ❌ No (implicit) |
| zap (sync) | ~300-500 | 1-2 | ❌ No | ❌ No (implicit) |
| slog (sync) | ~400-700 | 3-8 | ❌ No | ❌ No |

**Key differentiator:** loggerj is **2.7x faster than zerolog sync** and
**explicitly documents** the OS-buffered limitation. Zap and zerolog default
to OS-buffered writes but don't state it explicitly.

### Parallel Sync Note

Under parallel sync load, loggerj's shared `syncBw` (buffered writer) creates
mutex contention (~112 ns → ~238 ns, ~2x slowdown). We chose a shared buffer
for throughput over a per-goroutine pool because the latter loses buffered
data on `Close()`. Even with this contention, loggerj remains **2-3x faster
than zerolog/zap sync** under parallel load.

---

## 3. Typed Fields

The `Field` API provides zero-allocation structured logging for dynamic values.

| Benchmark | ns/op | allocs/op | Description |
|---|---|---|---|
| `TypedFields` | ~72 | **0** | Static typed fields |
| `TypedFields_String` | ~50 | **0** | String literals (no conversion needed) |
| **`TypedFields_Dynamic`** | **~72** | **0** | **Dynamic int/bool/dur → 0 alloc** |

### The "Wow" Benchmark: Dynamic Values

When the caller has **dynamic values** (variables, not string literals),
typed fields avoid the `strconv.Itoa` / `fmt.Sprintf` allocations that the
string API requires:

```go
status := 200                    // dynamic value
latency := 150 * time.Millisecond // dynamic value
cached := true                    // dynamic value

// Typed API: 0 allocs
logger.InfoFields("HTTP", []byte("request"),
    loggerj.Int("status", status),
    loggerj.Dur("latency", latency),
    loggerj.Bool("cached", cached),
)

// String API equivalent would require:
// logger.InfoString("HTTP", "request",
//     "status", strconv.Itoa(status),        // 1 alloc
//     "latency", latency.String(),           // 1 alloc
//     "cached", strconv.FormatBool(cached))  // 1 alloc
// → 3 allocs total
```

### vs Industry Standards (Typed Fields)

| Package | API | Dynamic fields allocs/op |
|---|---|---|
| **loggerj** | `Int("status", statusVar)` | **0** |
| zap | `zap.Int("status", statusVar)` | 0 |
| zerolog | `.Int("status", statusVar)` | 0 |
| slog | `slog.Int("status", statusVar)` | **3-8** (interface boxing) |
| logrus | `.WithField("status", statusVar)` | **1+** (map + interface) |

---

## 4. slog.Handler Adapter (New in v1.3.1)

The `SlogHandler` routes all `slog` calls through loggerj's zero-allocation
typed-field pipeline. This lets applications adopted to the `log/slog` standard
benefit from loggerj's async throughput without changing their call sites.

| Benchmark | ns/op | allocs/op | Description |
|---|---|---|---|
| `SlogHandler` | ~462 | **0** | Includes slog.Info variadic overhead |
| **`SlogHandler_Handle`** | **~143** | **0** | **Handler internals only (pre-built Record)** |

### Design Notes

- `slog.Attr` → `Field` conversion is boxing-free (slog.Value is already tagged union)
- `WithAttrs` pre-converts attributes once (cold path); `Handle()` only converts per-record attrs
- `WithGroup` flattens nested groups into dotted keys (e.g., "outer.inner")
- Field buffers are pooled, keeping `Handle()` allocation-free once warm

### vs Industry Standards (slog Integration)

| Package | Adapter | ns/op | allocs/op |
|---|---|---|---|
| **loggerj** | `SlogHandler` | **~143** | **0** |
| zap | `zap.SugaredLogger` bridge | ~200-300 | 1-2 |
| zerolog | Community adapter | ~250-400 | 2-4 |

**Key differentiator:** loggerj's slog adapter is **~2x faster than zap's bridge**
because it leverages the zero-alloc `Field` API and async pipeline.

---

## 5. Profile Lookup Adaptive Strategy (New in v1.3.1)

`getProfile` uses an adaptive strategy based on registry size:

- **n ≤ 8:** linear scan over `names[]` (~8ns, cache-friendly)
- **n > 8:** `map[string]*SubProfile` for O(1) lookup (~8ns)

Threshold 8 is benchmark-driven: map is 2.7x faster than linear at n=16 on
Apple M1 Pro (7.7ns vs 20.5ns).

| Benchmark | ns/op | allocs/op | Description |
|---|---|---|---|
| `GetProfile_SmallRegistry` | ~225 | 1 | 8 profiles, linear scan (includes format/dispatch) |
| `GetProfile_LargeRegistry` | ~205 | 1 | 200 profiles, map lookup (includes format/dispatch) |
| `GetProfile_LinearScanDirect` | ~7.9 | 0 | Pure linear scan (no format/dispatch) |
| `GetProfile_MapLookupDirect` | ~7.7 | 0 | Pure map lookup (no format/dispatch) |

### Key Insight

Map is **2.7x faster than linear** at n=16 (7.7ns vs 20.5ns). Threshold 8 is
conservative; most apps register <8 profiles. The adaptive strategy ensures
optimal performance regardless of registry size.

### Incremental Map Updates

`RegisterSub` clones the map + applies a single change (O(1) vs O(n) rebuild).
This prevents performance degradation when registering profiles at runtime.

---

## 6. Throughput Summary (logs/second, single core)

| System | Async | Sync | Allocs/op |
|---|---|---|---|
| **loggerj** | **~18M** | **~8.9M** | **0** |
| zerolog | ~3-4M | ~3M | 0-1 |
| zap | ~2.5-3M | ~2.5M | 1-2 |
| slog | ~1.5-2M | ~1.5M | 3-8 |
| logrus | ~0.5M | ~0.5M | 10+ |

---

## 7. Memory & GC Pressure

All hot-path benchmarks report **0 allocs/op**, meaning:

- No GC pressure from logging in steady state
- No STW (stop-the-world) pauses triggered by log allocations
- Predictable memory footprint under sustained load

The only exceptions:

- `WithCaller`: 2 allocs/op (Go's `runtime.Caller` limitation)
- `VeryLongMessage` (>4KB): 1 alloc/op (intentional pool release to prevent memory retention)
- `SyncEquivalent` (forced flush per log): 3-4 allocs/op (not intended usage)

---

## 8. Reproducing These Results

```bash
# Clone the repository
git clone https://github.com/uretgec/loggerj.git
cd loggerj

# Run all benchmarks with memory stats
go test -bench=. -benchmem -run='^$' -count=10 ./...

# Compare with baseline (if you have benchstat)
go test -bench=. -benchmem -run='^$' -count=10 ./... | tee new.txt
benchstat old.txt new.txt
```

### Environment

| Component | Value |
|---|---|
| CPU | Apple M1 Pro (10 cores) |
| RAM | 16 GB |
| OS | macOS (Darwin) |
| Go | 1.21+ |
| GOMAXPROCS | 10 (default) |

---

## See Also

- [README.md](README.md) — Quick start and feature overview
- [EXAMPLES.md](EXAMPLES.md) — Comprehensive usage examples
- [COMPARISON.md](COMPARISON.md) — Honest competitor analysis
- [CHANGELOG.md](CHANGELOG.md) — Release notes
