package loggerj

import (
	"context"
	"io"
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
}

// -----------------------------------------------------------------------------
// Benchmark: Rate Limited (DARK SIDE: Lock-Free)
// -----------------------------------------------------------------------------
func BenchmarkLog_RateLimited(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})

	// 🌑 YENİ MİMARİ: Rate limit artık RegisterSub ile tanımlanır!
	// Saniyede 100 log limiti.
	logger.RegisterSub("TEST", WithRateLimit(100, time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Artık parametre olarak limit geçmiyoruz, SubProfile bunu atomic olarak yönetiyor.
		logger.Log(LevelInfo, "TEST", []byte("message"))
	}
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

	// Sadece ERROR ve üstü
	logger.SetLevelValue(LevelError)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// INFO olduğu için filtrelenmeli (sadece ~2ns/op harcamalı)
		logger.Log(LevelInfo, "TEST", []byte("message"))
	}
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

	// Worker'ı başlatmıyoruz, channel anında dolacak ve drop edecek.

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Log(LevelInfo, "TEST", []byte("message"))
	}
}

// -----------------------------------------------------------------------------
// Benchmark: String API
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
}

// -----------------------------------------------------------------------------
// Benchmark: JSON Fast Path
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
}

// =============================================================================
// YENİ MİMARİYE ÖZEL BENCHMARK'LAR (DARK SIDE PROOFS)
// =============================================================================

// -----------------------------------------------------------------------------
// Benchmark: SubProfile Pre-Baked Prefix vs Inline Fields
// -----------------------------------------------------------------------------
// Bu test, yeni mimarinin en büyük iddiasını kanıtlar:
// RegisterSub ile verilen alanlar hot-path'te 0 CPU harcar.
func BenchmarkLog_SubProfile_Prefix(b *testing.B) {
	logger := NewLogger(Config{
		JSONOutput:    true,
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})

	// Prefix'ler init-time'da formatlanır. Hot-path'te sadece memcpy yapılır.
	logger.RegisterSub("API", WithFields("env", "prod", "service", "gateway", "region", "eu"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Sadece dinamik alanları geçiyoruz, prefix'ler otomatik eklenir.
		logger.Log(LevelInfo, "API", []byte("request received"), "path", "/api/v1/users")
	}
}

// -----------------------------------------------------------------------------
// Benchmark: Lock-Free Sampling
// -----------------------------------------------------------------------------
// Atomic counter ile sampling'in ne kadar hızlı olduğunu ölçer.
func BenchmarkLog_Sampling(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   4096,
		IncludeCaller: false,
	})

	// Her 10 logdan 1'i yazılacak (Lock-free atomic)
	logger.RegisterSub("SAMPLE", WithSampleRate(10))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Log(LevelInfo, "SAMPLE", []byte("sampled message"))
	}
}
