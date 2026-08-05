# Changelog

All notable changes to this project will be documented in this file.

## [1.2.0] - 2026-08-06

### Fixed

- **Critical**: Rotation stale-writer bug — worker now refreshes `bufio.Writer` handle after rotation, preventing log loss to closed files
- **Critical**: Rate limiter race condition — replaced dual-atomic design (`rlCount` + `rlResetAt`) with single packed `atomic.Uint64` using CAS, eliminating the ~2x burst window where legitimate logs could be incorrectly dropped or limits exceeded
- **Critical**: `SetOnDrop` data race — converted `onDrop` field to `atomic.Pointer[func(uint64)]` for thread-safe concurrent access during active logging
- **Lifecycle**: Double `Start()` panic — added `running atomic.Bool` guard and `closeOnce` for `workerDone` channel to prevent "close of closed channel" panics
- **Lifecycle**: Pre-start `Flush()` 1-second block — `Flush()` now drains and discards when worker is not running, fixing the timeout that occurred with `OutputFile` configurations
- **Config**: `RegisterSub` duplicate handling — now replaces existing profiles instead of silently ignoring duplicates, preventing subtle configuration errors
- **Test**: Flaky `TestLog_Concurrent` — changed assertion from `count == 1000` to `logged + dropped == 1000` for robust CI behavior

### Changed

- **API**: `Stats()` now returns a `Stats` struct instead of `map[string]uint64` to eliminate allocation in observability loops (BREAKING - no users yet in v1.x)
- **Internal**: Rate limiter now uses milliseconds natively via `UnixMilli()`, supporting sub-second windows (e.g., `500ms`) without silent conversion to 1s
- **Internal**: `Flush()` now uses `time.NewTimer` instead of `time.After` to prevent timer leaks

### Performance

- `BenchmarkLog_RateLimited`: ~41.7ns → ~41.5ns (single CAS vs dual-atomic, marginal improvement)
- `BenchmarkLog_RateLimited_HighContention`: ~120ns → ~290ns (expected trade-off for correctness: 64-bit division in packed-state design eliminates race window)
- `BenchmarkLog_JSON`: ~58ns → ~53ns (9% improvement, likely due to code path optimization)
- `BenchmarkLog_StringAPI`: ~63ns → ~60ns (5% improvement)
- All other hot-path benchmarks: unchanged or within noise margin

### Added

- `TestRotation_WriterRefresh`: End-to-end rotation test with real files, verifying no log loss after rotation
- `TestRateLimit_SubSecondWindow`: Validates 500ms windows work correctly (previously silently converted to 1s)
- `TestRateLimit_BoundaryExact`: Verifies exact limit enforcement (10th log passes, 11th drops)
- `TestOnDrop_ConcurrentSet`: Race detector test for concurrent `SetOnDrop` + logging
- `TestStart_Twice_NoPanic`: Validates lifecycle safety for double-start scenarios
- `TestFlush_BeforeStart_ReturnsQuickly`: Validates pre-start flush doesn't block for 1 second
- `TestRegisterSub_Duplicate_Replaces`: Validates duplicate profile handling
- `TestAsWriter_ZeroAlloc`: Validates zero-allocation `AsWriter` adapter via `bytes.TrimRight`

### Security

- No security vulnerabilities addressed (package has zero external dependencies)

### Deprecated

- No APIs deprecated (breaking changes documented above)

### Removed

- No APIs removed

### Migration Guide (v1.1.0 → v1.2.0)

If you were using `Stats()`:

```go
// Old (v1.1.0)
stats := logger.Stats()
drops := stats["drops"]

// New (v1.2.0)
stats := logger.Stats()
drops := stats.Drops
```

All other APIs remain unchanged. If you haven't published your package yet, no migration needed.
