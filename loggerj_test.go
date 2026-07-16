package loggerj

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// -----------------------------------------------------------------------------
// Thread-Safe Buffer (Race-Free)
// -----------------------------------------------------------------------------

type safeBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *safeBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

func (b *safeBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = b.buf[:0]
}

// -----------------------------------------------------------------------------
// Test Helper
// -----------------------------------------------------------------------------

func setupTestLogger(t *testing.T, cfg Config) (*Logger, *safeBuffer, context.CancelFunc) {
	t.Helper()

	logger := NewLogger(cfg)
	buf := &safeBuffer{}

	ctx, cancel := context.WithCancel(context.Background())

	go logger.StartWithWriter(ctx, buf)
	time.Sleep(20 * time.Millisecond) // Worker'ın başlaması için kısa bir bekleme

	t.Cleanup(func() {
		cancel()
		time.Sleep(50 * time.Millisecond) // Son logların yazılması için
		logger.Close()
	})

	return logger, buf, cancel
}

// -----------------------------------------------------------------------------
// Basic Tests
// -----------------------------------------------------------------------------

func TestLog_Basic(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   false,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.Log(LevelInfo, "TEST", []byte("hello world"))
	time.Sleep(50 * time.Millisecond)

	output := buf.String()
	if !strings.Contains(output, "hello world") {
		t.Errorf("expected 'hello world' in output, got: %s", output)
	}
	if !strings.Contains(output, "INFO") {
		t.Errorf("expected 'INFO' in output, got: %s", output)
	}
}

func TestLog_AllLevels(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	// YENİ API: SetLevelValue kullan
	logger.SetLevelValue(LevelDebug)

	tests := []struct {
		level Level
		want  string
	}{
		{LevelDebug, "DEBUG"},
		{LevelInfo, "INFO"},
		{LevelWarn, "WARN"},
		{LevelError, "ERROR"},
	}

	for _, tt := range tests {
		logger.Log(tt.level, "TEST", []byte("msg"))
	}

	time.Sleep(50 * time.Millisecond)
	output := buf.String()

	for _, tt := range tests {
		if !strings.Contains(output, tt.want) {
			t.Errorf("expected %s in output", tt.want)
		}
	}
}

// -----------------------------------------------------------------------------
// JSON Tests
// -----------------------------------------------------------------------------

func TestLog_JSON(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.Log(LevelInfo, "TEST", []byte("hello"), "key1", "val1")
	time.Sleep(50 * time.Millisecond)

	output := buf.String()

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nOutput: %s", err, output)
	}

	if result["level"] != "INFO" {
		t.Errorf("expected level=INFO, got %v", result["level"])
	}
	if result["type"] != "TEST" {
		t.Errorf("expected type=TEST, got %v", result["type"])
	}
	if result["msg"] != "hello" {
		t.Errorf("expected msg=hello, got %v", result["msg"])
	}

	fields, ok := result["fields"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected fields object, got %T", result["fields"])
	}
	if fields["key1"] != "val1" {
		t.Errorf("expected key1=val1, got %v", fields["key1"])
	}
}

func TestLog_JSON_MultipleFields(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.Log(LevelInfo, "DB", []byte("query"),
		"host", "localhost",
		"port", "5432",
		"duration", "150ms")
	time.Sleep(50 * time.Millisecond)

	output := buf.String()

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	fields := result["fields"].(map[string]interface{})
	if fields["host"] != "localhost" {
		t.Errorf("expected host=localhost, got %v", fields["host"])
	}
	if fields["port"] != "5432" {
		t.Errorf("expected port=5432, got %v", fields["port"])
	}
	if fields["duration"] != "150ms" {
		t.Errorf("expected duration=150ms, got %v", fields["duration"])
	}
}

func TestLog_JSON_NoFields(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.Log(LevelInfo, "TEST", []byte("no fields"))
	time.Sleep(50 * time.Millisecond)

	output := buf.String()

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if _, ok := result["fields"]; ok {
		t.Error("expected no fields key")
	}
}

func TestLog_JSON_SpecialChars(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.Log(LevelInfo, "TEST", []byte("msg with \"quotes\" and \\backslash"),
		"key", "value with \"quotes\"")
	time.Sleep(50 * time.Millisecond)

	output := buf.String()

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nOutput: %s", err, output)
	}
}

func TestRegisterSub_JSON_PrefixMerge(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	// 1. Prefix'i kaydet (Pre-baked)
	logger.RegisterSub("API", WithFields("env", "prod", "region", "eu"))

	// 2. Inline field ile log at
	logger.Info("API", []byte("request"), "user_id", "123", "action", "login")
	time.Sleep(50 * time.Millisecond)

	output := buf.String()

	// 3. JSON'un bozulmadığını kanıtla
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("JSON bozuk! Prefix ve Inline field birleşimi hata verdi: %v\nOutput: %s", err, output)
	}

	// 4. Hem prefix hem inline field'ın geldiğini kontrol et
	if result["env"] != "prod" {
		t.Errorf("prefix field 'env' eksik")
	}
	if result["region"] != "eu" {
		t.Errorf("prefix field 'region' eksik")
	}

	fields := result["fields"].(map[string]interface{})
	if fields["user_id"] != "123" {
		t.Errorf("inline field 'user_id' eksik")
	}
}

// -----------------------------------------------------------------------------
// Level Filter Tests
// -----------------------------------------------------------------------------

func TestLog_LevelFilter(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.SetLevelValue(LevelWarn)

	logger.Log(LevelDebug, "TEST", []byte("debug msg"))
	logger.Log(LevelInfo, "TEST", []byte("info msg"))
	logger.Log(LevelWarn, "TEST", []byte("warn msg"))
	logger.Log(LevelError, "TEST", []byte("error msg"))

	time.Sleep(50 * time.Millisecond)

	output := buf.String()
	if strings.Contains(output, "debug msg") {
		t.Error("debug should be filtered")
	}
	if strings.Contains(output, "info msg") {
		t.Error("info should be filtered")
	}
	if !strings.Contains(output, "warn msg") {
		t.Error("warn should be logged")
	}
	if !strings.Contains(output, "error msg") {
		t.Error("error should be logged")
	}
}

func TestLog_RuntimeLevelChange(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.SetLevelValue(LevelError)
	logger.Log(LevelInfo, "TEST", []byte("should not appear"))
	time.Sleep(30 * time.Millisecond)

	logger.SetLevelValue(LevelInfo)
	logger.Log(LevelInfo, "TEST", []byte("should appear"))
	time.Sleep(30 * time.Millisecond)

	output := buf.String()
	if strings.Contains(output, "should not appear") {
		t.Error("first message should be filtered")
	}
	if !strings.Contains(output, "should appear") {
		t.Error("second message should be logged")
	}
}

// -----------------------------------------------------------------------------
// Rate Limit & Sampling Tests (YENİ MİMARİYE UYARLANDI)
// -----------------------------------------------------------------------------

func TestLog_RateLimit(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	// YENİ MİMARİ: Rate limit sadece RegisterSub ile çalışır!
	logger.RegisterSub("TEST", WithRateLimit(2, time.Second))

	for i := 0; i < 10; i++ {
		logger.Log(LevelInfo, "TEST", []byte("rate limited"))
	}

	time.Sleep(50 * time.Millisecond)

	output := buf.String()
	count := strings.Count(output, "rate limited")
	// 2 saniyelik pencerede limit 2 olduğu için en fazla 2-3 log görmeliyiz
	if count > 3 {
		t.Errorf("expected ~2 logs due to rate limit, got %d", count)
	}
}

func TestLog_RateLimit_DifferentTypes(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.RegisterSub("TYPE_A", WithRateLimit(2, time.Second))
	logger.RegisterSub("TYPE_B", WithRateLimit(2, time.Second))

	for i := 0; i < 5; i++ {
		logger.Log(LevelInfo, "TYPE_A", []byte("msg A"))
		logger.Log(LevelInfo, "TYPE_B", []byte("msg B"))
	}

	time.Sleep(50 * time.Millisecond)

	output := buf.String()
	countA := strings.Count(output, "msg A")
	countB := strings.Count(output, "msg B")

	if countA > 3 || countB > 3 {
		t.Errorf("expected ~2 logs per type, got A=%d B=%d", countA, countB)
	}
}

func TestLog_Sampling(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	// Her 10 logdan 1'i yazılacak
	logger.RegisterSub("SAMPLE", WithSampleRate(10))

	for i := 0; i < 50; i++ {
		logger.Log(LevelInfo, "SAMPLE", []byte("sampled"))
	}

	time.Sleep(50 * time.Millisecond)

	output := buf.String()
	count := strings.Count(output, "sampled")

	// 50 log atıldı, rate 10. Beklenen 5 log. Toleransla 3-7 arası kabul edilebilir.
	if count < 3 || count > 8 {
		t.Errorf("expected ~5 logs due to sampling, got %d", count)
	}
}

// -----------------------------------------------------------------------------
// Fields & SubProfile Tests (YENİ)
// -----------------------------------------------------------------------------

func TestLog_Fields(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.Log(LevelInfo, "TEST", []byte("with fields"), "host", "localhost", "port", "5432")
	time.Sleep(50 * time.Millisecond)

	output := buf.String()
	if !strings.Contains(output, "host=localhost") {
		t.Errorf("expected field host=localhost, got: %s", output)
	}
	if !strings.Contains(output, "port=5432") {
		t.Errorf("expected field port=5432, got: %s", output)
	}
}

func TestLog_Fields_OddCount(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.Log(LevelInfo, "TEST", []byte("odd fields"), "key1", "val1", "key2")
	time.Sleep(50 * time.Millisecond)

	output := buf.String()
	if !strings.Contains(output, "odd fields") {
		t.Errorf("expected message, got: %s", output)
	}
}

func TestRegisterSub_Prefixes(t *testing.T) {
	// TEXT FORMAT
	loggerText, bufText, _ := setupTestLogger(t, Config{
		JSONOutput:   false,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})
	loggerText.RegisterSub("PREFIX_TEST", WithFields("env", "prod", "region", "eu"))
	loggerText.Log(LevelInfo, "PREFIX_TEST", []byte("msg"))
	time.Sleep(50 * time.Millisecond)

	outText := bufText.String()
	if !strings.Contains(outText, "env=prod region=eu ") {
		t.Errorf("expected pre-baked text prefix, got: %s", outText)
	}

	// JSON FORMAT
	loggerJSON, bufJSON, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})
	loggerJSON.RegisterSub("PREFIX_TEST", WithFields("env", "prod", "region", "eu"))
	loggerJSON.Log(LevelInfo, "PREFIX_TEST", []byte("msg"))
	time.Sleep(50 * time.Millisecond)

	outJSON := bufJSON.String()
	if !strings.Contains(outJSON, `"env":"prod","region":"eu"`) {
		t.Errorf("expected pre-baked json prefix, got: %s", outJSON)
	}
}

// -----------------------------------------------------------------------------
// Drop Counter Tests
// -----------------------------------------------------------------------------

func TestLog_DropCounter(t *testing.T) {
	logger := NewLogger(Config{
		FlushTimeout: 100 * time.Millisecond,
		ChannelSize:  1, // Bilerek çok küçük veriyoruz
	})

	logger.ResetDrops()

	for i := 0; i < 100; i++ {
		logger.Log(LevelInfo, "TEST", []byte("overflow"))
	}

	drops := logger.Drops()
	if drops == 0 {
		t.Error("expected drops > 0")
	}
}

// -----------------------------------------------------------------------------
// Level Helpers Tests
// -----------------------------------------------------------------------------

func TestLevelHelpers(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	// YENİ API: SetLevelValue kullan
	logger.SetLevelValue(LevelDebug)

	logger.Debug("TEST", []byte("debug msg"))
	logger.Info("TEST", []byte("info msg"))
	logger.Warn("TEST", []byte("warn msg"))
	logger.Error("TEST", []byte("error msg"))

	time.Sleep(50 * time.Millisecond)

	output := buf.String()

	if !strings.Contains(output, "DEBUG") {
		t.Error("expected DEBUG")
	}
	if !strings.Contains(output, "INFO") {
		t.Error("expected INFO")
	}
	if !strings.Contains(output, "WARN") {
		t.Error("expected WARN")
	}
	if !strings.Contains(output, "ERROR") {
		t.Error("expected ERROR")
	}
}

func TestLevelHelpers_WithFields(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.Info("TEST", []byte("with fields"), "key", "value")
	time.Sleep(50 * time.Millisecond)

	output := buf.String()
	if !strings.Contains(output, "key=value") {
		t.Errorf("expected field, got: %s", output)
	}
}

// -----------------------------------------------------------------------------
// Flush Tests
// -----------------------------------------------------------------------------

func TestFlush(t *testing.T) {
	logger := NewLogger(Config{
		FlushTimeout: 10 * time.Second, // Bilerek uzun veriyoruz, Flush'ı test etmek için
		ChannelSize:  100,
	})

	buf := &safeBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go logger.StartWithWriter(ctx, buf)
	time.Sleep(20 * time.Millisecond)

	logger.Log(LevelInfo, "TEST", []byte("flush test"))
	time.Sleep(5 * time.Millisecond)

	logger.Flush()
	time.Sleep(5 * time.Millisecond)

	output := buf.String()
	if !strings.Contains(output, "flush test") {
		t.Errorf("expected flush test, got: %s", output)
	}
}

func TestFlush_EmptyChannel(t *testing.T) {
	logger := NewLogger(Config{
		ChannelSize: 100,
	})

	// Worker yok, Flush() direkt return etmeli
	done := make(chan struct{})
	go func() {
		logger.Flush()
		close(done)
	}()

	select {
	case <-done:
		// Başarılı
	case <-time.After(1 * time.Second):
		t.Fatal("Flush() should return immediately when worker is not running")
	}
}

// -----------------------------------------------------------------------------
// Stats Tests
// -----------------------------------------------------------------------------

func TestStats(t *testing.T) {
	logger := NewLogger(Config{
		ChannelSize: 100,
	})

	stats := logger.Stats()
	if stats["channel_cap"] != 100 {
		t.Errorf("expected channel_cap=100, got %d", stats["channel_cap"])
	}
	if stats["drops"] != 0 {
		t.Errorf("expected drops=0, got %d", stats["drops"])
	}
}

// -----------------------------------------------------------------------------
// Concurrent Tests
// -----------------------------------------------------------------------------

func TestLog_Concurrent(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  1000,
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				logger.Log(LevelInfo, "CONCURRENT", []byte("msg"), "goroutine", string(rune('0'+(id%10))))
			}
		}(i)
	}

	wg.Wait()
	time.Sleep(100 * time.Millisecond)

	output := buf.String()
	count := strings.Count(output, "CONCURRENT")
	if count != 1000 {
		t.Errorf("expected 1000 logs, got %d", count)
	}
}

func TestLog_ConcurrentRateLimit(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  1000,
	})

	// YENİ MİMARİ: Rate limit RegisterSub ile tanımlanmalı
	logger.RegisterSub("RATE", WithRateLimit(10, time.Second))

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				logger.Log(LevelInfo, "RATE", []byte("msg"))
			}
		}()
	}

	wg.Wait()
	time.Sleep(100 * time.Millisecond)

	output := buf.String()
	count := strings.Count(output, "RATE")
	// 10 goroutine * 100 = 1000 log denendi. Limit 10/saniye.
	// Race condition'da CAS sayesinde tam 10 veya 11 görmeliyiz.
	if count > 15 {
		t.Errorf("expected ~10 logs due to concurrent rate limit, got %d", count)
	}
}

// -----------------------------------------------------------------------------
// Edge Cases
// -----------------------------------------------------------------------------

func TestLog_EmptyMessage(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.Log(LevelInfo, "TEST", []byte(""))
	time.Sleep(50 * time.Millisecond)

	output := buf.String()
	if !strings.Contains(output, "[TEST]") {
		t.Errorf("expected log type, got: %s", output)
	}
}

func TestLog_EmptyType(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.Log(LevelInfo, "", []byte("msg"))
	time.Sleep(50 * time.Millisecond)

	output := buf.String()
	if !strings.Contains(output, "msg") {
		t.Errorf("expected message, got: %s", output)
	}
}

func TestLog_LongMessage(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	longMsg := strings.Repeat("a", 10000)
	logger.Log(LevelInfo, "TEST", []byte(longMsg))
	time.Sleep(50 * time.Millisecond)

	output := buf.String()
	if !strings.Contains(output, longMsg) {
		t.Error("expected long message")
	}
}

func TestLevel_String(t *testing.T) {
	tests := []struct {
		level Level
		want  string
	}{
		{LevelDebug, "DEBUG"},
		{LevelInfo, "INFO"},
		{LevelWarn, "WARN"},
		{LevelError, "ERROR"},
		{Level(99), "LEVEL(99)"},
	}

	for _, tt := range tests {
		if got := tt.level.String(); got != tt.want {
			t.Errorf("Level(%d).String() = %s, want %s", tt.level, got, tt.want)
		}
	}
}

// -----------------------------------------------------------------------------
// Config Tests
// -----------------------------------------------------------------------------

func TestConfig_DefaultValues(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.WorkerBufferSize != 4096 {
		t.Errorf("expected WorkerBufferSize=4096, got %d", cfg.WorkerBufferSize)
	}
	if cfg.FlushThreshold != 4096 {
		t.Errorf("expected FlushThreshold=4096, got %d", cfg.FlushThreshold)
	}
	if cfg.WriterBufferSize != 8192 {
		t.Errorf("expected WriterBufferSize=8192, got %d", cfg.WriterBufferSize)
	}
	if cfg.RateLimitWindow != 1 {
		t.Errorf("expected RateLimitWindow=1, got %d", cfg.RateLimitWindow)
	}
}

func TestConfig_MinValues(t *testing.T) {
	logger := NewLogger(Config{
		WorkerBufferSize: 100, // Below minimum (256)
		FlushThreshold:   100, // Below minimum (256)
		WriterBufferSize: 100, // Below minimum (512)
		RateLimitWindow:  0,   // Below minimum (1)
	})

	if logger.cfg.WorkerBufferSize != 4096 {
		t.Errorf("expected WorkerBufferSize=4096 (default), got %d", logger.cfg.WorkerBufferSize)
	}
	if logger.cfg.FlushThreshold != 4096 {
		t.Errorf("expected FlushThreshold=4096 (default), got %d", logger.cfg.FlushThreshold)
	}
	if logger.cfg.WriterBufferSize != 8192 {
		t.Errorf("expected WriterBufferSize=8192 (default), got %d", logger.cfg.WriterBufferSize)
	}
	if logger.cfg.RateLimitWindow != 1 {
		t.Errorf("expected RateLimitWindow=1 (default), got %d", logger.cfg.RateLimitWindow)
	}
}

func TestConfig_FlushThresholdValidation(t *testing.T) {
	logger := NewLogger(Config{
		WorkerBufferSize: 2048,
		FlushThreshold:   4096, // Greater than WorkerBufferSize
	})

	if logger.cfg.FlushThreshold != 2048 {
		t.Errorf("expected FlushThreshold=2048 (capped to WorkerBufferSize), got %d", logger.cfg.FlushThreshold)
	}
}

// -----------------------------------------------------------------------------
// String API Tests
// -----------------------------------------------------------------------------

func TestLog_StringAPI(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	// YENİ API: SetLevelValue kullan
	logger.SetLevelValue(LevelDebug)

	logger.DebugString("TEST", "debug message")
	logger.InfoString("TEST", "info message", "key", "value")
	logger.WarnString("TEST", "warn message")
	logger.ErrorString("TEST", "error message")

	time.Sleep(50 * time.Millisecond)

	output := buf.String()
	if !strings.Contains(output, "debug message") {
		t.Error("expected debug message")
	}
	if !strings.Contains(output, "info message") {
		t.Error("expected info message")
	}
	if !strings.Contains(output, "warn message") {
		t.Error("expected warn message")
	}
	if !strings.Contains(output, "error message") {
		t.Error("expected error message")
	}
	if !strings.Contains(output, "key=value") {
		t.Error("expected field key=value")
	}
}

// -----------------------------------------------------------------------------
// Caller Info Test
// -----------------------------------------------------------------------------

func TestLog_IncludeCaller(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout:  10 * time.Millisecond,
		ChannelSize:   100,
		IncludeCaller: true, // Aktif ediyoruz
	})

	// Info metodu artık skip=2 kullanıyor, bu yüzden loggerj_test.go'yu gösterecek
	logger.Info("TEST", []byte("caller test"))
	time.Sleep(50 * time.Millisecond)

	output := buf.String()

	// Dosya adı "loggerj_test.go" içermeli
	if !strings.Contains(output, "loggerj_test.go") {
		t.Errorf("expected filename 'loggerj_test.go' in output, got: %s", output)
	}
	// Satır numarası ayracı ":" içermeli
	if !strings.Contains(output, ":") {
		t.Errorf("expected line number separator ':' in output, got: %s", output)
	}
}

// -----------------------------------------------------------------------------
// Critical test
// -----------------------------------------------------------------------------
func TestLog_CAS_ThunderingHerd(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  10000, // Çok büyük channel
	})

	// Sadece 10 loga izin ver
	logger.RegisterSub("STRESS", WithRateLimit(10, time.Second))

	var wg sync.WaitGroup
	// 1000 Goroutine, her biri 100 log atsın (Toplam 100.000 istek)
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				logger.Info("STRESS", []byte("stress test"))
			}
		}()
	}

	wg.Wait()
	time.Sleep(200 * time.Millisecond)

	output := buf.String()
	count := strings.Count(output, "stress test")

	// CAS sayesinde tam olarak 10 (veya pencere geçişiyle max 20) log geçmeli.
	// Mutex olsaydı burada ya deadlock olurdu ya da binlerce log geçerdi.
	if count > 25 {
		t.Errorf("CAS Lock-Free mekanizması başarısız! Beklenen ~10, Gelen: %d", count)
	}
}

func BenchmarkLog_RateLimited_HighContention(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   65536,
		IncludeCaller: false,
	})

	// Tek bir profile, çok yüksek limit (hiç drop etmesin, sadece CAS'i ölçelim)
	logger.RegisterSub("HOT", WithRateLimit(10000000, time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, io.Discard)

	b.ResetTimer()
	b.ReportAllocs()

	// Tüm CPU çekirdeklerini tek bir rate limit state'ine çarpıştır
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			logger.Log(LevelInfo, "HOT", []byte("message"))
		}
	})
}
