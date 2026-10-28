package loggerj

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// -----------------------------------------------------------------------------
// Benchmark: No Fields (Fastest Path)
// -----------------------------------------------------------------------------

func BenchmarkLog_NoFields(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Log(LevelInfo, "TEST", []byte("message"))
	}
	reportDrops(b, logger)
}

// BenchmarkLog_VeryLongMessage measures the cost of a 10KB message.
// This exceeds the Entry.Reset() threshold (4096 bytes), so the Msg slice
// is released to GC on Reset and re-allocated on the next use (1 alloc/op).
// This is an expected trade-off: preventing permanent memory retention
// in the pool for rare large messages.
func BenchmarkLog_VeryLongMessage(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	veryLongMsg := strings.Repeat("x", 10240) // 10KB

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Log(LevelInfo, "TEST", []byte(veryLongMsg))
	}
	reportDrops(b, logger)
}

// -----------------------------------------------------------------------------
// Benchmark: With Fields (Inline)
// -----------------------------------------------------------------------------

func BenchmarkLog_WithFields(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Log(LevelInfo, "TEST", []byte("message"),
			"key1", "value1", "key2", "value2")
	}
	reportDrops(b, logger)
}

// -----------------------------------------------------------------------------
// Benchmark: JSON Format
// -----------------------------------------------------------------------------

func BenchmarkLog_JSON(b *testing.B) {
	logger := NewLogger(Config{
		JSONOutput:    true,
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Log(LevelInfo, "TEST", []byte("message"),
			"key1", "value1", "key2", "value2")
	}
	reportDrops(b, logger)
}

// -----------------------------------------------------------------------------
// Benchmark: With Caller Info (Slow)
// -----------------------------------------------------------------------------

func BenchmarkLog_WithCaller(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: true, // Enable caller info
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Log(LevelInfo, "TEST", []byte("message"))
	}
	reportDrops(b, logger)
}

// -----------------------------------------------------------------------------
// Benchmark: Rate Limited (Lock-Free CAS)
// -----------------------------------------------------------------------------

func BenchmarkLog_RateLimited(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})

	// Rate limit is defined via RegisterSub (v2 architecture).
	// Limit: 100 logs per second.
	logger.RegisterSub("TEST", WithRateLimit(100, time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// No limit parameter passed; the SubProfile enforces it atomically.
		logger.Log(LevelInfo, "TEST", []byte("message"))
	}
	reportDrops(b, logger)
}

// -----------------------------------------------------------------------------
// Benchmark: Parallel (Concurrent)
// -----------------------------------------------------------------------------

func BenchmarkLog_Parallel(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			logger.Log(LevelInfo, "TEST", []byte("message"))
		}
	})
	reportDrops(b, logger)
}

// -----------------------------------------------------------------------------
// Benchmark: Level Helpers
// -----------------------------------------------------------------------------

func BenchmarkLevelHelpers(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			logger.Info("TEST", []byte("message"), "key", "value")
		}
	})
	reportDrops(b, logger)
}

// -----------------------------------------------------------------------------
// Benchmark: Many Fields
// -----------------------------------------------------------------------------

func BenchmarkLog_ManyFields(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Log(LevelInfo, "TEST", []byte("message"),
			"key1", "value1",
			"key2", "value2",
			"key3", "value3",
			"key4", "value4",
			"key5", "value5",
			"key6", "value6",
			"key7", "value7",
			"key8", "value8",
		)
	}
	reportDrops(b, logger)
}

// -----------------------------------------------------------------------------
// Benchmark: Long Message
// -----------------------------------------------------------------------------

func BenchmarkLog_LongMessage(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	longMsg := "This is a very long log message that simulates real-world usage patterns. " +
		"It contains multiple sentences and should test the buffer allocation strategy. " +
		"The logger should handle this efficiently without excessive allocations."

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Log(LevelInfo, "TEST", []byte(longMsg))
	}
	reportDrops(b, logger)
}

// -----------------------------------------------------------------------------
// Benchmark: Filtered (Below Level)
// -----------------------------------------------------------------------------

func BenchmarkLog_Filtered(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})

	// Only ERROR and above will pass the level filter
	logger.SetLevelValue(LevelError)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// INFO is below ERROR threshold — filtered in ~2ns/op
		logger.Log(LevelInfo, "TEST", []byte("message"))
	}
	reportDrops(b, logger)
}

// -----------------------------------------------------------------------------
// Benchmark: Dropped (Channel Full)
// -----------------------------------------------------------------------------

func BenchmarkLog_Dropped(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:  1 * time.Second, // Long timeout to keep channel full
		ChannelSize:   1,               // Very small channel
		IncludeCaller: false,
	})

	// No worker started — channel fills immediately, all entries are dropped.

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Log(LevelInfo, "TEST", []byte("message"))
	}
	reportDrops(b, logger)
}

// -----------------------------------------------------------------------------
// Benchmark: String API (Zero-Copy via unsafe)
// -----------------------------------------------------------------------------

func BenchmarkLog_StringAPI(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.InfoString("TEST", "message", "key1", "value1")
	}
	reportDrops(b, logger)
}

// -----------------------------------------------------------------------------
// Benchmark: Custom Buffer Sizes
// -----------------------------------------------------------------------------

func BenchmarkLog_LargeBuffer(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:     50 * time.Millisecond,
		ChannelSize:      4096,
		WorkerBufferSize: 16384,
		FlushThreshold:   16384,
		IncludeCaller:    false,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Log(LevelInfo, "TEST", []byte("message"))
	}
	reportDrops(b, logger)
}

func BenchmarkLog_SmallBuffer(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:     50 * time.Millisecond,
		ChannelSize:      4096,
		WorkerBufferSize: 1024,
		FlushThreshold:   1024,
		IncludeCaller:    false,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Log(LevelInfo, "TEST", []byte("message"))
	}
	reportDrops(b, logger)
}

// -----------------------------------------------------------------------------
// Benchmark: JSON Escaping Paths
// -----------------------------------------------------------------------------

func BenchmarkLog_JSON_NoEscape(b *testing.B) {
	logger := NewLogger(Config{
		JSONOutput:    true,
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Log(LevelInfo, "TEST", []byte("simple message without special chars"))
	}
	reportDrops(b, logger)
}

func BenchmarkLog_JSON_WithEscape(b *testing.B) {
	logger := NewLogger(Config{
		JSONOutput:    true,
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Log(LevelInfo, "TEST", []byte("message with \"quotes\" and \\backslash"))
	}
	reportDrops(b, logger)
}

// =============================================================================
// Pre-Compiled SubProfile Benchmarks (Architecture Proofs)
// =============================================================================

// -----------------------------------------------------------------------------
// Benchmark: SubProfile Pre-Baked Prefix vs Inline Fields
// -----------------------------------------------------------------------------
// Proves the core architectural claim: fields registered via RegisterSub
// cost zero CPU in the hot path. Prefixes are formatted once at init-time
// and injected via a single memcpy in the worker.

func BenchmarkLog_SubProfile_Prefix(b *testing.B) {
	logger := NewLogger(Config{
		JSONOutput:    true,
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})

	// Prefixes are formatted at init-time. Hot path performs only a memcpy.
	logger.RegisterSub("API", WithFields("env", "prod", "service", "gateway", "region", "eu"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Only dynamic fields are passed; pre-baked prefixes are injected automatically.
		logger.Log(LevelInfo, "API", []byte("request received"), "path", "/api/v1/users")
	}
	reportDrops(b, logger)
}

// -----------------------------------------------------------------------------
// Benchmark: Lock-Free Sampling
// -----------------------------------------------------------------------------
// Measures the overhead of atomic-counter-based sampling (1 out of N).

func BenchmarkLog_Sampling(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})

	// Log 1 out of every 10 entries (lock-free atomic counter)
	logger.RegisterSub("SAMPLE", WithSampleRate(10))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Log(LevelInfo, "SAMPLE", []byte("sampled message"))
	}
	reportDrops(b, logger)
}

// -----------------------------------------------------------------------------
// Benchmark: Sync-Equivalent (Fair Comparison with zap/zerolog)
// -----------------------------------------------------------------------------
// Forces Flush() after every log to simulate synchronous behavior.
// This is NOT the intended usage pattern for loggerj (which is async),
// but provides a fair apples-to-apples comparison with sync loggers.
// Expect ~3 allocs/op here due to Flush() channel synchronization overhead.

func BenchmarkLog_SyncEquivalent(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Log(LevelInfo, "TEST", []byte("message"))
		logger.Flush()
	}
	reportDrops(b, logger)
}

// -----------------------------------------------------------------------------
// Benchmark: Sync Mode

// The write strategy depends on `DurabilityTier`:

// - **OSBuffered / FsyncEveryN:** shared `bufio.Writer` protected by a mutex
//   that is held during the buffer copy AND the flush/fsync. Not lock-free.
// - **Direct / FsyncEveryWrite:** one `write(2)` per entry to an `O_APPEND`
//   file handle. No loggerj mutex (os.File.Write serializes internally).
// -----------------------------------------------------------------------------

// BenchmarkSyncMode_NoFields measures sync-mode performance with no fields.
// Target: <400ns/op, ≤1 alloc/op.
func BenchmarkSyncMode_NoFields(b *testing.B) {
	tmpDir := b.TempDir()
	logger := NewLogger(Config{
		SyncMode:   true,
		OutputFile: tmpDir + "/bench.log",
	})
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		logger.InfoString("TEST", "sync message")
	}
	b.StopTimer()
	logger.Close()
}

// BenchmarkSyncMode_TypedFields measures sync-mode with typed Field API.
// Target: 0 allocs/op (caller-side zero alloc is real).
func BenchmarkSyncMode_TypedFields(b *testing.B) {
	tmpDir := b.TempDir()
	logger := NewLogger(Config{
		SyncMode:   true,
		JSONOutput: true,
		OutputFile: tmpDir + "/bench.json",
	})
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		logger.InfoFields("HTTP", []byte("request"),
			Int("status", 200),
			Dur("latency", 50*time.Millisecond),
		)
	}
	b.StopTimer()
	logger.Close()
}

// BenchmarkSyncMode_Parallel measures sync-mode under concurrent load.
// The default tier (OSBuffered) serializes on syncMu, so throughput does not
// scale with cores; see BENCH.md.
func BenchmarkSyncMode_Parallel(b *testing.B) {
	tmpDir := b.TempDir()
	logger := NewLogger(Config{
		SyncMode:   true,
		OutputFile: tmpDir + "/bench_parallel.log",
	})
	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			logger.InfoString("TEST", "parallel sync message")
		}
	})
	b.StopTimer()
	logger.Close()
}

// BenchmarkSyncMode_OSBuffered measures OSBuffered tier throughput.
// No syscall per entry: do not compare with loggers that write(2) per entry.
func BenchmarkSyncMode_OSBuffered(b *testing.B) {
	tmpDir := b.TempDir()
	logger := NewLogger(Config{
		SyncMode:       true,
		OutputFile:     tmpDir + "/bench.log",
		DurabilityTier: OSBuffered,
	})
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		logger.InfoString("TEST", "osbuffered message")
	}
	b.StopTimer()
	time.Sleep(20 * time.Millisecond) // Wait for periodic flush
	logger.Close()
}

// BenchmarkSyncMode_Direct measures Direct tier throughput.
// Expected: ~1550ns/op (syscall overhead).
func BenchmarkSyncMode_Direct(b *testing.B) {
	tmpDir := b.TempDir()
	logger := NewLogger(Config{
		SyncMode:       true,
		OutputFile:     tmpDir + "/bench.log",
		DurabilityTier: Direct,
	})
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		logger.InfoString("TEST", "direct message")
	}
	b.StopTimer()
	logger.Close()
}

// BenchmarkSyncMode_FsyncEveryWrite measures FsyncEveryWrite tier throughput.
// Expected: ~5000-10000ns/op (fsync on every log).
func BenchmarkSyncMode_FsyncEveryWrite(b *testing.B) {
	tmpDir := b.TempDir()
	logger := NewLogger(Config{
		SyncMode:       true,
		OutputFile:     tmpDir + "/bench.log",
		DurabilityTier: FsyncEveryWrite,
	})
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		logger.InfoString("TEST", "fsync message")
	}
	b.StopTimer()
	logger.Close()
}

// BenchmarkTypedFields measures the typed Field API throughput.
// Target: 0 allocs/op — caller-side zero alloc is real, not claimed.
// This is the equivalent of zap's Int/Bool/Duration fields, but without
// the mutex overhead that zap's sync mode imposes.
func BenchmarkTypedFields(b *testing.B) {
	logger := NewLogger(Config{
		JSONOutput:    true,
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		logger.InfoFields("HTTP", []byte("request"),
			Int("status", 200),
			Dur("latency", 50*time.Millisecond),
			Bool("cached", true),
			Str("method", "GET"),
		)
	}
	reportDrops(b, logger)
}

// BenchmarkTypedFields_String compares typed vs string-field API overhead.
// Typed fields should be FASTER because they avoid strconv.Itoa/Sprintf
// allocations on the caller side.
func BenchmarkTypedFields_String(b *testing.B) {
	logger := NewLogger(Config{
		JSONOutput:    true,
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		logger.InfoString("HTTP", "request",
			"status", "200", // string literal: no alloc
			"latency", "50ms", // string literal: no alloc
			"cached", "true",
			"method", "GET")
	}
	reportDrops(b, logger)
}

// BenchmarkTypedFields_Dynamic shows the REAL advantage: when the caller
// has non-string values (int, bool, duration), typed fields avoid the
// fmt.Sprintf / strconv.Itoa allocations that the string API requires.
func BenchmarkTypedFields_Dynamic(b *testing.B) {
	logger := NewLogger(Config{
		JSONOutput:    true,
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	status := 200
	latency := 50 * time.Millisecond
	cached := true
	method := "GET"

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		logger.InfoFields("HTTP", []byte("request"),
			Int("status", status),
			Dur("latency", latency),
			Bool("cached", cached),
			Str("method", method),
		)
	}
	reportDrops(b, logger)
}

// -----------------------------------------------------------------------------
// Benchmark: Profile Lookup (Adaptive Strategy)
// -----------------------------------------------------------------------------

// BenchmarkGetProfile_SmallRegistry measures linear-scan performance for
// a typical small registry (8 profiles). This is the common case.
func BenchmarkGetProfile_SmallRegistry(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})
	// Register 8 profiles (below threshold)
	for i := 0; i < 8; i++ {
		logger.RegisterSub(fmt.Sprintf("TYPE_%d", i), WithFields("idx", fmt.Sprintf("%d", i)))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	// Cycle through all 8 profiles to prevent branch-predictor shortcuts
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		logType := fmt.Sprintf("TYPE_%d", i%8)
		logger.Info(logType, []byte("msg"))
	}
	reportDrops(b, logger)
}

// BenchmarkGetProfile_LargeRegistry measures map-based O(1) lookup for
// a large registry (200 profiles). This proves the adaptive strategy
// prevents performance degradation at scale.
func BenchmarkGetProfile_LargeRegistry(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})
	// Register 200 profiles (well above threshold)
	for i := 0; i < 200; i++ {
		logger.RegisterSub(fmt.Sprintf("TYPE_%d", i), WithFields("idx", fmt.Sprintf("%d", i)))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	// Cycle through all 200 profiles — the map should give O(1) lookup
	// regardless of registry size
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		logType := fmt.Sprintf("TYPE_%d", i%200)
		logger.Info(logType, []byte("msg"))
	}
	reportDrops(b, logger)
}

// BenchmarkGetProfile_LinearScanDirect isolates the pure linear-scan
// cost without format/dispatch overhead, for precise comparison with
// the map lookup path.
func BenchmarkGetProfile_LinearScanDirect(b *testing.B) {
	logger := NewLogger(Config{ChannelSize: 100})
	for i := 0; i < 16; i++ {
		logger.RegisterSub(fmt.Sprintf("TYPE_%d", i))
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// Direct call to getProfile, bypassing format/dispatch
		_ = logger.getProfile("TYPE_8") // middle of registry
	}
}

// BenchmarkGetProfile_MapLookupDirect isolates the pure map-lookup cost
// for direct comparison with linear scan.
func BenchmarkGetProfile_MapLookupDirect(b *testing.B) {
	logger := NewLogger(Config{ChannelSize: 100})
	for i := 0; i < 200; i++ {
		logger.RegisterSub(fmt.Sprintf("TYPE_%d", i))
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = logger.getProfile("TYPE_100") // middle of registry
	}
}

// -----------------------------------------------------------------------------
// Benchmark: slog.Handler Adapter
// -----------------------------------------------------------------------------

// BenchmarkSlogHandler measures the adapter's throughput when slog calls
// are routed through loggerj's typed-field pipeline. Includes slog's own
// caller-side overhead (slog.Info variadic) plus handler conversion.
func BenchmarkSlogHandler(b *testing.B) {
	logger := NewLogger(Config{
		JSONOutput:    true,
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	handler := NewSlogHandler(logger, "SLOG")
	slogger := slog.New(handler)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		slogger.Info("request", "method", "GET", "status", 200)
	}
	reportDrops(b, logger)
}

// BenchmarkSlogHandler_Handle isolates the Handle() method cost by
// pre-building a Record, excluding slog's caller-side variadic overhead.
func BenchmarkSlogHandler_Handle(b *testing.B) {
	logger := NewLogger(Config{
		JSONOutput:    true,
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	handler := NewSlogHandler(logger, "SLOG")
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "request", 0)
	r.AddAttrs(slog.String("method", "GET"), slog.Int("status", 200))

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = handler.Handle(context.Background(), r)
	}
	reportDrops(b, logger)
}

func BenchmarkLog_RateLimited_HighContention(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   65536,
		IncludeCaller: false,
	})

	// Single profile with the maximum exact packed-state limit.
	// This keeps the benchmark inside the representable rate-limit range.
	logger.RegisterSub("HOT", WithRateLimit(rlMaxExactLimit, time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	// Slam all CPU cores into a single rate-limit state
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			logger.Log(LevelInfo, "HOT", []byte("message"))
		}
	})
	reportDrops(b, logger)
}

// BenchmarkTimeNowUnixMilli isolates the vDSO cost of time.Now().UnixMilli()
// on the host OS. This is the unavoidable baseline cost paid on every
// rate-limited log call in the current design.
func BenchmarkTimeNowUnixMilli(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = time.Now().UnixMilli()
	}
}

// BenchmarkCheckAtomicRateLimit_WithoutTime isolates the pure CAS and
// bitwise math cost of the rate limiter, completely excluding the
// time.Now() syscall.
func BenchmarkCheckAtomicRateLimit_WithoutTime(b *testing.B) {
	p := &SubProfile{
		rlLimit:    1_000_000,
		rlWindowMs: 1000,
		rlStartMs:  time.Now().UnixMilli(),
	}
	// Fixed timestamp prevents window transitions during the benchmark.
	now := p.rlStartMs + 500

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = checkAtomicRateLimitAt(p, now)
	}
}

// BenchmarkCheckAtomicRateLimit_RealWorld measures the actual hot-path
// cost including the time.Now() vDSO call.
func BenchmarkCheckAtomicRateLimit_RealWorld(b *testing.B) {
	p := &SubProfile{
		rlLimit:    1_000_000,
		rlWindowMs: 1000,
		rlStartMs:  time.Now().UnixMilli(),
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// This calls time.Now() inside, exactly like the real hot-path.
		_ = checkAtomicRateLimitAt(p, time.Now().UnixMilli())
	}
}

// -----------------------------------------------------------------------------
// v1.4.4: drop reporting, end-to-end throughput, FsyncEveryN
// -----------------------------------------------------------------------------

// reportDrops reports how many of the b.N attempts were dropped because the
// channel was full. ns/op of the async benchmarks above is the caller-side
// cost of an attempt; when drops/op is not 0, part of that time is the cheap
// drop path and the numbers are not delivered-log costs. Level, sampling and
// rate-limit rejections are not drops and are not counted here.
func reportDrops(b *testing.B, l *Logger) {
	b.ReportMetric(float64(l.Drops())/float64(b.N), "drops/op")
}

// benchmarkE2E measures delivered throughput: it includes the worker drain
// (Flush) in the timed region and reports the drop ratio and the number of
// entries actually delivered per second.
func benchmarkE2E(b *testing.B, cfg Config, logFn func(l *Logger)) {
	logger := NewLogger(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)
	<-logger.started

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logFn(logger)
	}
	logger.Flush()
	elapsed := b.Elapsed()
	b.StopTimer()

	drops := logger.Drops()
	delivered := uint64(b.N) - drops
	b.ReportMetric(float64(drops)/float64(b.N), "drops/op")
	if elapsed > 0 {
		b.ReportMetric(float64(delivered)/elapsed.Seconds(), "delivered/s")
	}
}

func BenchmarkE2E_NoFields_Text(b *testing.B) {
	benchmarkE2E(b, Config{
		FlushTimeout: 50 * time.Millisecond,
		ChannelSize:  4096,
	}, func(l *Logger) {
		l.Log(LevelInfo, "TEST", []byte("message"))
	})
}

func BenchmarkE2E_TypedFields_JSON(b *testing.B) {
	status, latency := 200, 50*time.Millisecond
	benchmarkE2E(b, Config{
		JSONOutput:   true,
		FlushTimeout: 50 * time.Millisecond,
		ChannelSize:  4096,
	}, func(l *Logger) {
		l.InfoFields("HTTP", []byte("request"),
			Int("status", status),
			Dur("latency", latency),
			Bool("cached", true),
			Str("method", "GET"),
		)
	})
}

// BenchmarkSyncMode_FsyncEveryN measures the FsyncEveryN tier (fsync every
// 100 entries).
func BenchmarkSyncMode_FsyncEveryN(b *testing.B) {
	tmpDir := b.TempDir()
	logger := NewLogger(Config{
		SyncMode:         true,
		OutputFile:       tmpDir + "/bench.log",
		DurabilityTier:   FsyncEveryN,
		FsyncEveryNCount: 100,
	})
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		logger.InfoString("TEST", "fsync-n message")
	}
	b.StopTimer()
	logger.Close()
}
