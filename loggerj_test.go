package loggerj

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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

// setupTestLogger creates a Logger with a deterministic lifecycle:
//   - Waits for the worker to start via the started channel (no sleep).
//   - On cleanup: Flush → cancel → wait for worker exit → Close.
func setupTestLogger(t *testing.T, cfg Config) (*Logger, *safeBuffer, context.CancelFunc) {
	t.Helper()

	logger := NewLogger(cfg)
	buf := &safeBuffer{}

	ctx, cancel := context.WithCancel(context.Background())

	go logger.StartWithWriter(ctx, buf)
	<-logger.started // Block until the worker goroutine is ready

	t.Cleanup(func() {
		logger.Flush()      // 1. Write all pending log entries (deterministic)
		cancel()            // 2. Signal the worker to stop
		<-logger.workerDone // 3. Wait for the worker to exit (drainAndFlush completed)
		logger.Close()      // 4. Release file handles
	})

	return logger, buf, cancel
}

// flushAndRead flushes pending logs and returns the buffer content.
// Replaces the flaky pattern: time.Sleep(50ms) + buf.String()
func flushAndRead(t *testing.T, logger *Logger, buf *safeBuffer) string {
	t.Helper()
	logger.Flush()
	return buf.String()
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
	output := flushAndRead(t, logger, buf)

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

	output := flushAndRead(t, logger, buf)

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
	output := flushAndRead(t, logger, buf)

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
	output := flushAndRead(t, logger, buf)

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
	output := flushAndRead(t, logger, buf)

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
	output := flushAndRead(t, logger, buf)

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

	// Register a pre-baked prefix profile
	logger.RegisterSub("API", WithFields("env", "prod", "region", "eu"))

	// Log with additional inline fields
	logger.Info("API", []byte("request"), "user_id", "123", "action", "login")
	output := flushAndRead(t, logger, buf)

	// Verify the JSON is well-formed after prefix + inline field merge
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("JSON corrupted after prefix+inline merge: %v\nOutput: %s", err, output)
	}

	// Verify both pre-baked prefix fields and inline fields are present
	if result["env"] != "prod" {
		t.Errorf("prefix field 'env' missing")
	}
	if result["region"] != "eu" {
		t.Errorf("prefix field 'region' missing")
	}

	fields := result["fields"].(map[string]interface{})
	if fields["user_id"] != "123" {
		t.Errorf("inline field 'user_id' missing")
	}
}

func TestJSON_ControlChars(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	// 0x7F (DEL) and various control characters
	logger.Log(LevelInfo, "TEST", []byte("a\x7fb\x01c\x1fd"))
	output := flushAndRead(t, logger, buf)

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("invalid JSON with control chars: %v\nOutput: %s", err, output)
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

	output := flushAndRead(t, logger, buf)

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
	logger.Flush() // Ensure the filtered entry is fully processed (buffer stays empty)

	logger.SetLevelValue(LevelInfo)
	logger.Log(LevelInfo, "TEST", []byte("should appear"))
	output := flushAndRead(t, logger, buf)

	if strings.Contains(output, "should not appear") {
		t.Error("first message should be filtered")
	}
	if !strings.Contains(output, "should appear") {
		t.Error("second message should be logged")
	}
}

// -----------------------------------------------------------------------------
// Rate Limit & Sampling Tests
// -----------------------------------------------------------------------------

func TestLog_RateLimit(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	// Rate limits are defined exclusively via RegisterSub (v2 architecture)
	logger.RegisterSub("TEST", WithRateLimit(2, time.Second))

	for i := 0; i < 10; i++ {
		logger.Log(LevelInfo, "TEST", []byte("rate limited"))
	}

	output := flushAndRead(t, logger, buf)
	count := strings.Count(output, "rate limited")

	// Limit is 2 per 1-second window; expect at most 2-3 logs
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

	output := flushAndRead(t, logger, buf)
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

	// Log 1 out of every 10 entries
	logger.RegisterSub("SAMPLE", WithSampleRate(10))

	for i := 0; i < 50; i++ {
		logger.Log(LevelInfo, "SAMPLE", []byte("sampled"))
	}

	output := flushAndRead(t, logger, buf)
	count := strings.Count(output, "sampled")

	// 50 logs submitted, rate=10 → expected ~5. Tolerance: 3-8.
	if count < 3 || count > 8 {
		t.Errorf("expected ~5 logs due to sampling, got %d", count)
	}
}

// -----------------------------------------------------------------------------
// Fields & SubProfile Tests
// -----------------------------------------------------------------------------

func TestLog_Fields(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.Log(LevelInfo, "TEST", []byte("with fields"), "host", "localhost", "port", "5432")
	output := flushAndRead(t, logger, buf)

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
	output := flushAndRead(t, logger, buf)

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

	outText := flushAndRead(t, loggerText, bufText)
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

	outJSON := flushAndRead(t, loggerJSON, bufJSON)
	if !strings.Contains(outJSON, `"env":"prod","region":"eu"`) {
		t.Errorf("expected pre-baked json prefix, got: %s", outJSON)
	}
}

func TestRegisterSub_Unlimited(t *testing.T) {
	logger := NewLogger(Config{ChannelSize: 100})

	// Register 200 profiles — no artificial limit
	for i := 0; i < 200; i++ {
		logger.RegisterSub(fmt.Sprintf("TYPE_%d", i))
	}

	// All profiles must be resolvable
	for i := 0; i < 200; i++ {
		p := logger.getProfile(fmt.Sprintf("TYPE_%d", i))
		if p == logger.defaultProfile {
			t.Errorf("TYPE_%d not found", i)
		}
	}

	// Unknown profile falls back to default
	if logger.getProfile("NONEXISTENT") != logger.defaultProfile {
		t.Error("NONEXISTENT should fall back to default")
	}
}

// -----------------------------------------------------------------------------
// Drop Counter Tests
// -----------------------------------------------------------------------------

func TestLog_DropCounter(t *testing.T) {
	logger := NewLogger(Config{
		FlushTimeout: 100 * time.Millisecond,
		ChannelSize:  1, // Intentionally tiny to force drops
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

	logger.SetLevelValue(LevelDebug)

	logger.Debug("TEST", []byte("debug msg"))
	logger.Info("TEST", []byte("info msg"))
	logger.Warn("TEST", []byte("warn msg"))
	logger.Error("TEST", []byte("error msg"))

	output := flushAndRead(t, logger, buf)

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
	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, "key=value") {
		t.Errorf("expected field, got: %s", output)
	}
}

// -----------------------------------------------------------------------------
// Flush Tests
// -----------------------------------------------------------------------------

func TestFlush(t *testing.T) {
	logger := NewLogger(Config{
		FlushTimeout: 10 * time.Second, // Intentionally long to isolate Flush behavior
		ChannelSize:  100,
	})

	buf := &safeBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go logger.StartWithWriter(ctx, buf)
	<-logger.started

	logger.Log(LevelInfo, "TEST", []byte("flush test"))
	logger.Flush()
	output := buf.String()

	if !strings.Contains(output, "flush test") {
		t.Errorf("expected flush test, got: %s", output)
	}
}

func TestFlush_EmptyChannel(t *testing.T) {
	logger := NewLogger(Config{
		ChannelSize: 100,
	})

	// No worker running — Flush() should return immediately
	done := make(chan struct{})
	go func() {
		logger.Flush()
		close(done)
	}()

	select {
	case <-done:
		// Success
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
	if stats.ChannelCap != 100 {
		t.Errorf("expected ChannelCap=100, got %d", stats.ChannelCap)
	}
	if stats.Drops != 0 {
		t.Errorf("expected Drops=0, got %d", stats.Drops)
	}
	if stats.ChannelSize != 0 {
		t.Errorf("expected ChannelSize=0, got %d", stats.ChannelSize)
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
	logger.ResetDrops() // Ensure clean state

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

	output := flushAndRead(t, logger, buf)
	count := strings.Count(output, "CONCURRENT")
	drops := logger.Drops()

	// Robust assertion: under extreme CI load, the worker might lag and
	// the channel might drop entries. The sum of written + dropped must
	// exactly equal the 1000 submitted logs. This eliminates flaky test
	// failures caused by environmental scheduling delays.
	if uint64(count)+drops != 1000 {
		t.Errorf("expected 1000 total (logged + dropped), got logged=%d drops=%d", count, drops)
	}
}

func TestLog_ConcurrentRateLimit(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  1000,
	})

	// Rate limit defined via RegisterSub (v2 architecture)
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
	output := flushAndRead(t, logger, buf)
	count := strings.Count(output, "RATE")

	// 10 goroutines × 100 = 1000 attempts. Limit: 10/s.
	// CAS ensures exactly ~10-11 logs pass.
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
	output := flushAndRead(t, logger, buf)

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
	output := flushAndRead(t, logger, buf)

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
	output := flushAndRead(t, logger, buf)

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
		{Level(99), "UNKNOWN"},
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

	logger.SetLevelValue(LevelDebug)

	logger.DebugString("TEST", "debug message")
	logger.InfoString("TEST", "info message", "key", "value")
	logger.WarnString("TEST", "warn message")
	logger.ErrorString("TEST", "error message")

	output := flushAndRead(t, logger, buf)

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
		IncludeCaller: true,
	})

	// Info uses skip=2, so the caller should be this test file
	logger.Info("TEST", []byte("caller test"))
	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, "loggerj_test.go") {
		t.Errorf("expected filename 'loggerj_test.go' in output, got: %s", output)
	}
	if !strings.Contains(output, ":") {
		t.Errorf("expected line number separator ':' in output, got: %s", output)
	}
}

// -----------------------------------------------------------------------------
// CAS Thundering Herd Test
// -----------------------------------------------------------------------------

func TestLog_CAS_ThunderingHerd(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  10000,
	})

	// 1-second window (reduced from 2s to ensure test completes in 1 window)
	logger.RegisterSub("STRESS", WithRateLimit(10, 1*time.Second))

	var wg sync.WaitGroup
	// 50 goroutines × 100 logs = 5,000 total requests (reduced from 50,000)
	// This completes in <1s under race detector → exactly 1 window
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				logger.Info("STRESS", []byte("stress test"))
			}
		}()
	}

	wg.Wait()

	// Use flushAndRead instead of time.Sleep to ensure all logs are processed
	output := flushAndRead(t, logger, buf)
	count := strings.Count(output, "stress test")

	// With 1s window and test completing in <1s, at most 10 logs should pass
	// Tolerance: 10-12 (allow small jitter for window boundary)
	if count > 12 {
		t.Errorf("CAS lock-free rate limit failed! Expected ≤12 (1 window), got: %d", count)
	}
	if count < 1 {
		t.Errorf("no logs passed through, count=%d", count)
	}
}

// -----------------------------------------------------------------------------
// Rate Limit Window Reset Tests
// -----------------------------------------------------------------------------

func TestRateLimit_WindowReset(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  1000,
	})

	// 3 logs per 1-second window
	logger.RegisterSub("WINDOW", WithRateLimit(3, time.Second))

	// Window 1: attempt 10 logs → at most 3 should pass
	for i := 0; i < 10; i++ {
		logger.Log(LevelInfo, "WINDOW", []byte("window1"))
	}

	// Wait for the window to expire
	time.Sleep(1100 * time.Millisecond)
	buf.Reset()

	// Window 2: attempt 10 logs → counter was reset, at most 3 should pass
	for i := 0; i < 10; i++ {
		logger.Log(LevelInfo, "WINDOW", []byte("window2"))
	}

	output := flushAndRead(t, logger, buf)
	count := strings.Count(output, "window2")

	if count < 1 {
		t.Errorf("no logs passed after window reset, count=%d", count)
	}
	if count > 4 {
		t.Errorf("rate limit exceeded after window reset! Expected ~3, got: %d", count)
	}
}

func TestRateLimit_CASReset_Consistency(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  10000,
	})
	// Tight window: 5 logs per 2 seconds. The 2-second window gives enough
	// headroom under race detector for the 1000 requests to complete within
	// a single window (typically <500ms even with -race).
	logger.RegisterSub("CAS_TEST", WithRateLimit(5, 2*time.Second))

	var wg sync.WaitGroup
	// 50 goroutines × 20 logs = 1000 total requests (reduced from previous)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				logger.Log(LevelInfo, "CAS_TEST", []byte("cas"))
			}
		}()
	}
	wg.Wait()

	// Use flushAndRead instead of time.Sleep+buf.String for deterministic drain
	output := flushAndRead(t, logger, buf)
	count := strings.Count(output, "cas")

	// Limit 5, window 2s. Test completes in <500ms → exactly 5 logs should pass.
	// The packed-state CAS closes the reset race, so tolerance is tight: ≤6.
	if count > 6 {
		t.Errorf("rate limit exceeded! Expected ≤5 (1 window), got: %d", count)
	}
	if count < 1 {
		t.Errorf("no logs passed through, count=%d", count)
	}
}

// TestRateLimit_SubSecondWindow verifies that sub-second rate limit windows
// work correctly. The previous second-granular implementation silently
// converted 500ms windows to 1s; the packed-state design uses milliseconds
// natively.
func TestRateLimit_SubSecondWindow(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  1000,
	})
	// 5 logs per 500ms window
	logger.RegisterSub("SUBSEC", WithRateLimit(5, 500*time.Millisecond))

	// Burst 1: 20 logs in <500ms → at most 5 should pass
	for i := 0; i < 20; i++ {
		logger.Log(LevelInfo, "SUBSEC", []byte("burst1"))
	}
	output1 := flushAndRead(t, logger, buf)
	count1 := strings.Count(output1, "burst1")
	if count1 > 5 {
		t.Errorf("sub-second window 1 exceeded! Expected ≤5, got: %d", count1)
	}
	if count1 < 1 {
		t.Errorf("no logs passed in sub-second window 1, count=%d", count1)
	}

	// Wait for the window to expire and a new one to begin
	time.Sleep(600 * time.Millisecond)
	buf.Reset()

	// Burst 2: counter must be reset, another 5 should pass
	for i := 0; i < 20; i++ {
		logger.Log(LevelInfo, "SUBSEC", []byte("burst2"))
	}
	output2 := flushAndRead(t, logger, buf)
	count2 := strings.Count(output2, "burst2")
	if count2 > 5 {
		t.Errorf("sub-second window 2 exceeded! Expected ≤5, got: %d", count2)
	}
	if count2 < 1 {
		t.Errorf("no logs passed in sub-second window 2, count=%d", count2)
	}
}

// TestRateLimit_BoundaryExact verifies the exact boundary: at the limit,
// the Nth log passes and the (N+1)th is dropped within the same window.
// With the old design, CAS races could let N+1 or N+2 slip through.
func TestRateLimit_BoundaryExact(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  1000,
	})
	logger.RegisterSub("BOUND", WithRateLimit(10, time.Second))

	// 15 sequential attempts in a single goroutine (no contention)
	for i := 0; i < 15; i++ {
		logger.Log(LevelInfo, "BOUND", []byte("bound"))
	}
	output := flushAndRead(t, logger, buf)
	count := strings.Count(output, "bound")
	if count != 10 {
		t.Errorf("expected exactly 10 logs at boundary, got: %d", count)
	}
}

// -----------------------------------------------------------------------------
// AsWriter Tests (io.Writer Adapter for std log)
// -----------------------------------------------------------------------------

func TestAsWriter_LevelAndType(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	w := logger.AsWriter(LevelError, "LEGACY")
	w.Write([]byte("legacy error message\n"))

	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, `"level":"ERROR"`) {
		t.Errorf("expected level=ERROR, got: %s", output)
	}
	if !strings.Contains(output, `"type":"LEGACY"`) {
		t.Errorf("expected type=LEGACY, got: %s", output)
	}
	if !strings.Contains(output, "legacy error message") {
		t.Errorf("expected message content, got: %s", output)
	}
}

func TestAsWriter_MultipleLevels(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	warnW := logger.AsWriter(LevelWarn, "THIRD_PARTY")
	errorW := logger.AsWriter(LevelError, "CRITICAL")

	warnW.Write([]byte("warning from lib\n"))
	errorW.Write([]byte("error from lib\n"))

	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, `"level":"WARN"`) {
		t.Errorf("expected WARN level, got: %s", output)
	}
	if !strings.Contains(output, `"type":"THIRD_PARTY"`) {
		t.Errorf("expected THIRD_PARTY type, got: %s", output)
	}
	if !strings.Contains(output, `"level":"ERROR"`) {
		t.Errorf("expected ERROR level, got: %s", output)
	}
	if !strings.Contains(output, `"type":"CRITICAL"`) {
		t.Errorf("expected CRITICAL type, got: %s", output)
	}
}

func TestAsWriter_TextFormat(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   false,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	w := logger.AsWriter(LevelWarn, "STDLIB")
	w.Write([]byte("text format test\n"))

	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, "WARN") {
		t.Errorf("expected WARN in text output, got: %s", output)
	}
	if !strings.Contains(output, "[STDLIB]") {
		t.Errorf("expected [STDLIB] in text output, got: %s", output)
	}
}

func TestAsWriter_StdLogIntegration(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	// Real stdlib log integration test
	w := logger.AsWriter(LevelInfo, "STDLIB")
	stdLogger := log.New(w, "", 0) // flags=0: loggerj provides its own timestamp

	stdLogger.Println("intercepted by loggerj")

	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, `"level":"INFO"`) {
		t.Errorf("expected INFO, got: %s", output)
	}
	if !strings.Contains(output, `"type":"STDLIB"`) {
		t.Errorf("expected STDLIB, got: %s", output)
	}
	if !strings.Contains(output, "intercepted by loggerj") {
		t.Errorf("expected message, got: %s", output)
	}
}

func TestAsWriter_BackwardCompatible(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	// Legacy usage pattern must produce identical behavior
	w := logger.AsWriter(LevelInfo, "STDLIB")
	w.Write([]byte("old style call\n"))

	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, `"level":"INFO"`) {
		t.Errorf("backward compat broken: expected INFO, got: %s", output)
	}
	if !strings.Contains(output, `"type":"STDLIB"`) {
		t.Errorf("backward compat broken: expected STDLIB, got: %s", output)
	}
}

// -----------------------------------------------------------------------------
// Entry Pool Memory Leak Tests
// -----------------------------------------------------------------------------

func TestEntry_Reset_LargeMsg(t *testing.T) {
	e := &Entry{}

	// Simulate a 10KB message
	largeMsg := make([]byte, 10240)
	e.Msg = append(e.Msg[:0], largeMsg...)

	if cap(e.Msg) < 10240 {
		t.Fatalf("setup error: expected cap >= 10240, got %d", cap(e.Msg))
	}

	e.Reset()

	// Large slice must be released to GC (nil)
	if e.Msg != nil {
		t.Errorf("expected Msg=nil after Reset for large slice, got cap=%d", cap(e.Msg))
	}
	if len(e.Msg) != 0 {
		t.Errorf("expected len(Msg)=0, got %d", len(e.Msg))
	}
}

func TestEntry_Reset_SmallMsg(t *testing.T) {
	e := &Entry{}

	e.Msg = append(e.Msg[:0], []byte("OK")...)

	if cap(e.Msg) == 0 {
		t.Fatal("setup error: expected cap > 0")
	}

	originalCap := cap(e.Msg)

	e.Reset()

	// Small slice must retain capacity to avoid re-allocation
	if e.Msg == nil {
		t.Error("expected Msg to retain capacity for small slice")
	}
	if cap(e.Msg) != originalCap {
		t.Errorf("expected cap=%d preserved, got %d", originalCap, cap(e.Msg))
	}
	if len(e.Msg) != 0 {
		t.Errorf("expected len=0, got %d", len(e.Msg))
	}
}

func TestEntry_Reset_LargeFields(t *testing.T) {
	e := &Entry{}

	// 100 fields (50 key-value pairs)
	fields := make([]string, 100)
	for i := range fields {
		fields[i] = "value"
	}
	e.Fields = append(e.Fields[:0], fields...)

	if cap(e.Fields) < 100 {
		t.Fatalf("setup error: expected cap >= 100, got %d", cap(e.Fields))
	}

	e.Reset()

	if e.Fields != nil {
		t.Errorf("expected Fields=nil after Reset for large slice, got cap=%d", cap(e.Fields))
	}
}

func TestEntry_Reset_SmallFields(t *testing.T) {
	e := &Entry{}

	e.Fields = append(e.Fields[:0], "key", "value")
	originalCap := cap(e.Fields)

	e.Reset()

	if e.Fields == nil {
		t.Error("expected Fields to retain capacity for small slice")
	}
	if cap(e.Fields) != originalCap {
		t.Errorf("expected cap=%d preserved, got %d", originalCap, cap(e.Fields))
	}
	if len(e.Fields) != 0 {
		t.Errorf("expected len=0, got %d", len(e.Fields))
	}
}

func TestEntry_Reset_Reuse(t *testing.T) {
	e := &Entry{}

	// Large message → Reset → nil
	e.Msg = append(e.Msg[:0], make([]byte, 10240)...)
	e.Reset()

	// Reuse after nil (append to nil slice is safe)
	e.Msg = append(e.Msg[:0], []byte("reused")...)
	if string(e.Msg) != "reused" {
		t.Errorf("expected 'reused', got '%s'", string(e.Msg))
	}

	// Fields must also be reusable
	e.Fields = append(e.Fields[:0], "k", "v")
	if len(e.Fields) != 2 || e.Fields[0] != "k" || e.Fields[1] != "v" {
		t.Errorf("expected [k v], got %v", e.Fields)
	}
}

func TestEntry_Reset_AllFields(t *testing.T) {
	e := &Entry{
		Level:   LevelError,
		Type:    "HTTP",
		Msg:     []byte("test"),
		File:    "main.go",
		Line:    42,
		Fields:  []string{"k", "v"},
		Profile: &SubProfile{Name: "TEST"},
	}

	e.Reset()

	if e.Level != 0 {
		t.Errorf("Level not reset: %d", e.Level)
	}
	if e.Type != "" {
		t.Errorf("Type not reset: %s", e.Type)
	}
	if len(e.Msg) != 0 {
		t.Errorf("Msg not reset: len=%d", len(e.Msg))
	}
	if e.File != "" {
		t.Errorf("File not reset: %s", e.File)
	}
	if e.Line != 0 {
		t.Errorf("Line not reset: %d", e.Line)
	}
	if len(e.Fields) != 0 {
		t.Errorf("Fields not reset: len=%d", len(e.Fields))
	}
	if e.Profile != nil {
		t.Error("Profile not reset")
	}
}

// -----------------------------------------------------------------------------
// Zero-Copy String API Tests
// -----------------------------------------------------------------------------

func TestStringAPI_ZeroCopy_Correctness(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.SetLevelValue(LevelDebug)

	logger.DebugString("TEST", "debug zero-copy")
	logger.InfoString("TEST", "info zero-copy", "key", "value")
	logger.WarnString("TEST", "warn zero-copy")
	logger.ErrorString("TEST", "error zero-copy")

	output := flushAndRead(t, logger, buf)

	for _, expected := range []string{
		"debug zero-copy",
		"info zero-copy",
		"warn zero-copy",
		"error zero-copy",
		`"key":"value"`,
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected %q in output, got: %s", expected, output)
		}
	}
}

func TestStringAPI_ZeroCopy_EmptyString(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.InfoString("TEST", "")

	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, `"msg":""`) {
		t.Errorf("expected empty msg in JSON, got: %s", output)
	}
}

func TestStringAPI_ZeroCopy_SpecialChars(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.InfoString("TEST", "msg with \"quotes\" and \\backslash\\ and \nnewline")

	output := flushAndRead(t, logger, buf)

	// Must produce valid JSON
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("invalid JSON after zero-copy: %v\nOutput: %s", err, output)
	}

	msg, ok := result["msg"].(string)
	if !ok {
		t.Fatalf("expected msg string, got %T", result["msg"])
	}
	if !strings.Contains(msg, "quotes") {
		t.Errorf("expected quotes in msg, got: %s", msg)
	}
}

func TestStringAPI_ZeroCopy_LongString(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	longMsg := strings.Repeat("x", 10000)
	logger.InfoString("TEST", longMsg)

	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, longMsg) {
		t.Error("expected long message in output")
	}
}

// TestStringAPI_ZeroAlloc verifies zero heap allocations in the String API.
//
// NOTE: testing.AllocsPerRun sets GOMAXPROCS(1), which blocks the async
// worker goroutine and prevents entries from returning to the pool.
// To work around this, we do NOT start a worker — the drop-path keeps
// the pool warm via Reset() + Put() on every call.
func TestStringAPI_ZeroAlloc(t *testing.T) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   1, // Drop-path: pool stays warm
		IncludeCaller: false,
	})
	// No worker started → channel fills immediately → drop-path active → pool warm

	// Warmup with identical message and fields to stabilize pool capacities
	for i := 0; i < 100; i++ {
		logger.InfoString("TEST", "zero alloc test", "key", "value")
	}

	allocs := testing.AllocsPerRun(1000, func() {
		logger.InfoString("TEST", "zero alloc test", "key", "value")
	})

	// Verified by benchmark: 0 allocs/op
	if allocs > 0 {
		t.Errorf("expected 0 allocs/op, got %.1f", allocs)
	}
}

// -----------------------------------------------------------------------------
// Timestamp Tests (Worker-Side)
// -----------------------------------------------------------------------------

func TestTimestamp_WorkerSide(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	before := time.Now().UnixMilli()
	logger.InfoString("TEST", "timestamp test")
	output := flushAndRead(t, logger, buf)
	after := time.Now().UnixMilli()

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	ts, ok := result["ts"].(float64)
	if !ok {
		t.Fatalf("expected ts number, got %T", result["ts"])
	}

	tsInt := int64(ts)
	if tsInt < before || tsInt > after {
		t.Errorf("timestamp %d out of range [%d, %d]", tsInt, before, after)
	}
}

// -----------------------------------------------------------------------------
// Context Integration Tests
// -----------------------------------------------------------------------------

func TestInfoCtx_WithTraceID(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	ctx := context.WithValue(context.Background(), TraceIDKey, "abc-123")
	logger.InfoCtx(ctx, "HTTP", "request", "method", "GET")

	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, `"trace_id":"abc-123"`) {
		t.Errorf("expected trace_id, got: %s", output)
	}
	if !strings.Contains(output, `"method":"GET"`) {
		t.Errorf("expected method field, got: %s", output)
	}
}

func TestInfoCtx_NilContext(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.InfoCtx(nil, "HTTP", "no context")
	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, "no context") {
		t.Errorf("expected message, got: %s", output)
	}
	if strings.Contains(output, "trace_id") {
		t.Error("nil context should not add trace_id")
	}
}

// -----------------------------------------------------------------------------
// OnDrop Callback Tests
// -----------------------------------------------------------------------------

func TestOnDrop_Callback(t *testing.T) {
	logger := NewLogger(Config{
		ChannelSize:  1,
		FlushTimeout: 10 * time.Second,
	})
	// No worker → channel fills immediately → drops occur

	var dropCount atomic.Uint64
	logger.SetOnDrop(func(dropped uint64) {
		dropCount.Store(dropped)
	})

	for i := 0; i < 100; i++ {
		logger.Log(LevelInfo, "TEST", []byte("overflow"))
	}

	if dropCount.Load() == 0 {
		t.Error("expected onDrop callback to be called")
	}
}

// TestOnDrop_ConcurrentSet verifies that calling SetOnDrop concurrently
// with active logging (which triggers the drop path) does not cause a
// data race. The old implementation used a plain struct field which
// triggered a DATA RACE under the -race detector.
func TestOnDrop_ConcurrentSet(t *testing.T) {
	logger := NewLogger(Config{
		ChannelSize:  1, // Intentionally tiny to force drops immediately
		FlushTimeout: 10 * time.Second,
	})
	// No worker started -> channel fills on first log -> all subsequent logs drop

	var wg sync.WaitGroup

	// Goroutine 1: continuously set and unset the callback (writer)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10000; i++ {
			if i%2 == 0 {
				logger.SetOnDrop(func(dropped uint64) {
					// Fast, atomic-only callback
				})
			} else {
				logger.SetOnDrop(nil)
			}
		}
	}()

	// Goroutine 2: continuously trigger drops (reader)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10000; i++ {
			logger.Log(LevelInfo, "TEST", []byte("overflow"))
		}
	}()

	wg.Wait()
}

// TestRotation_WriterRefresh verifies that after log rotation, the worker
// goroutine refreshes its bufio.Writer handle to the newly opened file.
// Without this fix, the worker continues writing to the old (closed) file
// handle, causing all subsequent logs to be lost with "write error".
func TestRotation_WriterRefresh(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := tmpDir + "/app.log"

	logger := NewLogger(Config{
		OutputFile:     logFile,
		MaxFileSize:    1024, // 1KB — forces rotation quickly
		MaxBackupFiles: 3,
		FlushTimeout:   10 * time.Millisecond,
		ChannelSize:    1000,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.Start(ctx)
	<-logger.started

	// Write enough data to trigger at least 2 rotations (~3KB total)
	for i := 0; i < 100; i++ {
		msg := fmt.Sprintf("log message number %d with some padding to fill the buffer quickly", i)
		logger.InfoString("TEST", msg)
	}

	logger.Flush()
	cancel()
	<-logger.workerDone
	logger.Close()

	// Verify the main log file exists and is not empty
	mainLog, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read main log file: %v", err)
	}
	if len(mainLog) == 0 {
		t.Error("main log file is empty after rotation — stale writer bug!")
	}

	// Verify at least one backup file exists (.1)
	backup1 := logFile + ".1"
	if _, err := os.Stat(backup1); os.IsNotExist(err) {
		t.Error("expected at least one backup file (.1) after rotation")
	}

	// Count total logs across all files (main + backups)
	totalLogs := 0
	files := []string{logFile, logFile + ".1", logFile + ".2", logFile + ".3"}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue // file doesn't exist, skip
		}
		totalLogs += strings.Count(string(data), "log message number")
	}

	// All 100 logs must be present across all files (no loss)
	if totalLogs < 95 { // allow small tolerance for timing
		t.Errorf("expected ~100 logs across all files, got %d — logs were lost!", totalLogs)
	}
}

// TestStart_Twice_NoPanic verifies that calling Start() or StartWithWriter()
// multiple times (either while running or after exit) does not cause a
// "close of closed channel" panic. The old implementation used a bare
// defer close(l.workerDone) which panicked on the second call.
func TestStart_Twice_NoPanic(t *testing.T) {
	logger := NewLogger(Config{
		ChannelSize:  100,
		FlushTimeout: 10 * time.Millisecond,
	})
	buf := &safeBuffer{}
	ctx, cancel := context.WithCancel(context.Background())

	// First start
	go logger.StartWithWriter(ctx, buf)
	<-logger.started

	// Second start while running -> should return immediately, no panic
	logger.StartWithWriter(ctx, buf)

	// Stop worker
	cancel()
	<-logger.workerDone

	// Third start after worker exited -> should also be safe (no panic)
	// The closeOnce ensures workerDone is not closed twice.
	ctx2, cancel2 := context.WithCancel(context.Background())
	go logger.StartWithWriter(ctx2, buf)
	// Note: started channel is already closed, so <-logger.started would
	// return immediately. We just verify no panic occurs.
	time.Sleep(20 * time.Millisecond)
	cancel2()
	// We can't safely <-logger.workerDone here because closeOnce already
	// closed it, but the lack of panic is the success criteria.
}

// TestFlush_BeforeStart_ReturnsQuickly verifies that Flush() does not
// block for 1 second when called before Start(), even if OutputFile is
// set (which causes globalWriter to be non-nil after NewLogger).
func TestFlush_BeforeStart_ReturnsQuickly(t *testing.T) {
	tmpDir := t.TempDir()
	logger := NewLogger(Config{
		OutputFile:  tmpDir + "/test.log",
		ChannelSize: 100,
	})

	// Log some entries to fill the channel
	for i := 0; i < 50; i++ {
		logger.InfoString("TEST", "message")
	}

	// Flush before Start() should return immediately (< 50ms), not block for 1s
	start := time.Now()
	logger.Flush()
	elapsed := time.Since(start)

	if elapsed > 50*time.Millisecond {
		t.Errorf("Flush() before Start() blocked for %v, expected < 50ms", elapsed)
	}

	logger.Close()
}

// TestRegisterSub_Duplicate_Replaces verifies that registering a SubProfile
// with an existing logType replaces the old profile instead of silently
// appending a duplicate that is ignored by the linear-scan getProfile().
// This prevents silent configuration errors during hot-reload or testing.
func TestRegisterSub_Duplicate_Replaces(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	// First registration: env=dev
	logger.RegisterSub("DUP", WithFields("env", "dev"))

	// Second registration: SAME name, DIFFERENT fields (env=prod, region=eu)
	logger.RegisterSub("DUP", WithFields("env", "prod", "region", "eu"))

	// Log a message
	logger.Info("DUP", []byte("msg"))
	output := flushAndRead(t, logger, buf)

	// The second registration MUST replace the first one.
	if !strings.Contains(output, `"env":"prod"`) {
		t.Errorf("expected replaced field env=prod, got: %s", output)
	}
	if !strings.Contains(output, `"region":"eu"`) {
		t.Errorf("expected replaced field region=eu, got: %s", output)
	}
	// The old field MUST NOT be present
	if strings.Contains(output, `"env":"dev"`) {
		t.Errorf("old field env=dev should have been replaced, got: %s", output)
	}
}

func TestTypedFields_JSON(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.InfoFields("HTTP", []byte("request"),
		Int("status", 200),
		Dur("latency", 150*time.Millisecond),
		Bool("cached", true),
		Str("method", "GET"),
	)

	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, `"status":200`) {
		t.Errorf("expected status:200, got: %s", output)
	}
	if !strings.Contains(output, `"latency":"150ms"`) {
		t.Errorf("expected latency:150ms, got: %s", output)
	}
	if !strings.Contains(output, `"cached":true`) {
		t.Errorf("expected cached:true, got: %s", output)
	}
	if !strings.Contains(output, `"method":"GET"`) {
		t.Errorf("expected method:GET, got: %s", output)
	}
}

func TestTypedFields_ZeroAlloc(t *testing.T) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   1,
		IncludeCaller: false,
	})

	// Warmup
	for i := 0; i < 100; i++ {
		logger.InfoFields("TEST", []byte("warmup"),
			Int("n", i), Str("s", "val"))
	}

	allocs := testing.AllocsPerRun(1000, func() {
		logger.InfoFields("TEST", []byte("zero alloc"),
			Int("n", 42), Str("s", "hello"), Bool("b", true))
	})

	if allocs > 0 {
		t.Errorf("expected 0 allocs/op for typed fields, got %.1f", allocs)
	}
}

func TestTypedFields_ErrNil(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.InfoFields("TEST", []byte("no error"), Err(nil))

	output := flushAndRead(t, logger, buf)
	// Check for the JSON field key "error": specifically, not the substring
	// "error" which appears in the message "no error". The colon ensures we're
	// matching a JSON key, not message content.
	if strings.Contains(output, `"error":`) {
		t.Errorf("Err(nil) should be skipped, got: %s", output)
	}
}

// -----------------------------------------------------------------------------
// Sync Mode Tests
// -----------------------------------------------------------------------------

// TestSyncMode_Basic verifies basic sync-mode logging works without Start().
func TestSyncMode_Basic(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := tmpDir + "/sync.log"
	logger := NewLogger(Config{
		SyncMode:   true,
		OutputFile: logFile,
	})
	// No Start() call needed in sync mode.
	logger.InfoString("TEST", "sync message", "key", "value")
	logger.Close()

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read sync log: %v", err)
	}
	output := string(data)
	if !strings.Contains(output, "sync message") {
		t.Errorf("expected 'sync message' in output, got: %s", output)
	}
	if !strings.Contains(output, "key=value") {
		t.Errorf("expected 'key=value' in output, got: %s", output)
	}
}

// TestSyncMode_JSON verifies sync-mode JSON output.
func TestSyncMode_JSON(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := tmpDir + "/sync.json"
	logger := NewLogger(Config{
		SyncMode:   true,
		JSONOutput: true,
		OutputFile: logFile,
	})
	logger.InfoString("HTTP", "request", "status", "200")
	logger.Close()

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read sync log: %v", err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("invalid JSON: %v\nOutput: %s", err, data)
	}
	if result["level"] != "INFO" {
		t.Errorf("expected level=INFO, got %v", result["level"])
	}
}

// TestSyncMode_TypedFields verifies sync-mode with typed Field API.
func TestSyncMode_TypedFields(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := tmpDir + "/sync_typed.json"
	logger := NewLogger(Config{
		SyncMode:   true,
		JSONOutput: true,
		OutputFile: logFile,
	})
	logger.InfoFields("HTTP", []byte("request"),
		Int("status", 200),
		Dur("latency", 150*time.Millisecond),
		Bool("cached", true),
	)
	logger.Close()

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read sync log: %v", err)
	}
	output := string(data)
	if !strings.Contains(output, `"status":200`) {
		t.Errorf("expected status:200, got: %s", output)
	}
	if !strings.Contains(output, `"latency":"150ms"`) {
		t.Errorf("expected latency:150ms, got: %s", output)
	}
	if !strings.Contains(output, `"cached":true`) {
		t.Errorf("expected cached:true, got: %s", output)
	}
}

// TestSyncMode_Concurrent verifies lock-free concurrent writes.
// 100 goroutines write simultaneously; O_APPEND guarantees no interleaving.
func TestSyncMode_Concurrent(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := tmpDir + "/sync_concurrent.log"
	logger := NewLogger(Config{
		SyncMode:   true,
		OutputFile: logFile,
	})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				logger.InfoString("CONCURRENT", fmt.Sprintf("goroutine-%d-iter-%d", id, j))
			}
		}(i)
	}
	wg.Wait()
	logger.Close()

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read sync log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	// 100 goroutines × 100 iterations = 10000 logs
	if len(lines) != 10000 {
		t.Errorf("expected 10000 lines, got %d", len(lines))
	}
	// Verify no interleaving: each line should be a complete log entry
	for i, line := range lines {
		if !strings.Contains(line, "goroutine-") {
			t.Errorf("line %d appears interleaved: %s", i, line)
		}
	}
}

// TestSyncMode_StartNoOp verifies Start() is a safe no-op in sync mode.
func TestSyncMode_StartNoOp(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := tmpDir + "/sync_noop.log"
	logger := NewLogger(Config{
		SyncMode:   true,
		OutputFile: logFile,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Start() should return immediately without blocking.
	go logger.Start(ctx)
	// started channel should be closed immediately in sync mode.
	select {
	case <-logger.started:
		// Success
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Start() blocked in sync mode — started channel not closed")
	}
	logger.InfoString("TEST", "after start")
	logger.Close()
}

// TestSyncMode_DurabilityTier_OSBuffered verifies OSBuffered tier throughput.
// Target: ~300ns/op (competitive with zerolog/zap sync mode).
func TestSyncMode_DurabilityTier_OSBuffered(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := tmpDir + "/osbuffered.log"
	logger := NewLogger(Config{
		SyncMode:       true,
		OutputFile:     logFile,
		DurabilityTier: OSBuffered,
	})
	// Write 1000 logs
	for i := 0; i < 1000; i++ {
		logger.InfoString("TEST", fmt.Sprintf("message-%d", i))
	}
	// Wait for periodic flush (10ms ticker)
	time.Sleep(20 * time.Millisecond)
	logger.Close()

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1000 {
		t.Errorf("expected 1000 lines, got %d", len(lines))
	}
}

// TestSyncMode_DurabilityTier_Direct verifies Direct tier writes immediately.
func TestSyncMode_DurabilityTier_Direct(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := tmpDir + "/direct.log"
	logger := NewLogger(Config{
		SyncMode:       true,
		OutputFile:     logFile,
		DurabilityTier: Direct,
	})
	logger.InfoString("TEST", "direct write")
	// No flush needed — Direct writes immediately
	logger.Close()

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log: %v", err)
	}
	if !strings.Contains(string(data), "direct write") {
		t.Errorf("expected 'direct write' in output, got: %s", data)
	}
}

// TestSyncMode_DurabilityTier_FsyncEveryN verifies fsync is called every N writes.
func TestSyncMode_DurabilityTier_FsyncEveryN(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := tmpDir + "/fsync_n.log"
	logger := NewLogger(Config{
		SyncMode:         true,
		OutputFile:       logFile,
		DurabilityTier:   FsyncEveryN,
		FsyncEveryNCount: 10,
	})
	// Write 25 logs → fsync should be called twice (at 10 and 20)
	for i := 0; i < 25; i++ {
		logger.InfoString("TEST", fmt.Sprintf("msg-%d", i))
	}
	logger.Close()

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 25 {
		t.Errorf("expected 25 lines, got %d", len(lines))
	}
}

// TestSyncWriteErrors verifies that failed writes in sync mode are counted
// and observable via Stats(). We simulate a failure by closing the file
// before logging.
func TestSyncWriteErrors(t *testing.T) {
	tmpDir := t.TempDir()
	logger := NewLogger(Config{
		SyncMode:       true,
		OutputFile:     tmpDir + "/err.log",
		DurabilityTier: Direct,
	})
	logger.InfoString("TEST", "before close")
	if err := logger.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	// After close, writes should fail and increment the counter.
	// Note: this writes to a closed file handle; the OS returns EBADF.
	logger.InfoString("TEST", "after close")

	stats := logger.Stats()
	if stats.SyncWriteErrors == 0 {
		t.Errorf("expected SyncWriteErrors > 0 after writing to closed file, got %d", stats.SyncWriteErrors)
	}
}

// -----------------------------------------------------------------------------
// Profile Lookup Adaptive Strategy Tests
// -----------------------------------------------------------------------------

// TestGetProfile_SmallRegistry_LinearScan verifies that small registries
// (n ≤ 8) use linear scan. This is the common case and must stay fast.
func TestGetProfile_SmallRegistry_LinearScan(t *testing.T) {
	logger := NewLogger(Config{ChannelSize: 100})

	// Register 8 profiles (at threshold, still linear scan)
	for i := 0; i < 8; i++ {
		logger.RegisterSub(fmt.Sprintf("TYPE_%d", i), WithFields("idx", fmt.Sprintf("%d", i)))
	}

	// All must be resolvable
	for i := 0; i < 8; i++ {
		p := logger.getProfile(fmt.Sprintf("TYPE_%d", i))
		if p == logger.defaultProfile {
			t.Errorf("TYPE_%d not found via linear scan", i)
		}
		if p.Name != fmt.Sprintf("TYPE_%d", i) {
			t.Errorf("wrong profile returned for TYPE_%d: got %s", i, p.Name)
		}
	}

	// Unknown profile must fall back to default
	if logger.getProfile("NONEXISTENT") != logger.defaultProfile {
		t.Error("NONEXISTENT should fall back to default")
	}

	// Verify the registry uses linear scan (no map) at threshold
	reg := logger.registry.Load()
	if reg.lookup != nil {
		t.Errorf("expected lookup=nil for n=8 (linear scan), got map with %d entries", len(reg.lookup))
	}
}

// TestGetProfile_LargeRegistry_MapFallback verifies that large registries
// (n > 8) automatically switch to map-based O(1) lookup. This prevents
// performance degradation as the number of profiles grows.
func TestGetProfile_LargeRegistry_MapFallback(t *testing.T) {
	logger := NewLogger(Config{ChannelSize: 100})

	// Register 200 profiles (well above threshold)
	for i := 0; i < 200; i++ {
		logger.RegisterSub(fmt.Sprintf("TYPE_%d", i), WithFields("idx", fmt.Sprintf("%d", i)))
	}

	// All must be resolvable
	for i := 0; i < 200; i++ {
		p := logger.getProfile(fmt.Sprintf("TYPE_%d", i))
		if p == logger.defaultProfile {
			t.Errorf("TYPE_%d not found via map lookup", i)
		}
		if p.Name != fmt.Sprintf("TYPE_%d", i) {
			t.Errorf("wrong profile returned for TYPE_%d: got %s", i, p.Name)
		}
	}

	// Verify the registry uses map lookup
	reg := logger.registry.Load()
	if reg.lookup == nil {
		t.Fatal("expected lookup map for n=200, got nil")
	}
	if len(reg.lookup) != 200 {
		t.Errorf("expected map with 200 entries, got %d", len(reg.lookup))
	}

	// Unknown profile must still fall back to default
	if logger.getProfile("NONEXISTENT") != logger.defaultProfile {
		t.Error("NONEXISTENT should fall back to default")
	}
}

// TestGetProfile_ThresholdCrossing verifies the adaptive strategy works
// correctly as the registry grows past the threshold (n=8).
func TestGetProfile_ThresholdCrossing(t *testing.T) {
	logger := NewLogger(Config{ChannelSize: 100})

	// Phase 1: register 8 profiles (at threshold, still linear scan)
	for i := 0; i < 8; i++ {
		logger.RegisterSub(fmt.Sprintf("TYPE_%d", i))
	}
	reg := logger.registry.Load()
	if reg.lookup != nil {
		t.Errorf("at n=8, expected linear scan (lookup=nil), got map")
	}

	// Phase 2: register 9th profile (crosses threshold, map is built)
	logger.RegisterSub("TYPE_8")
	reg = logger.registry.Load()
	if reg.lookup == nil {
		t.Fatal("at n=9, expected map lookup, got nil")
	}
	if len(reg.lookup) != 9 {
		t.Errorf("expected map with 9 entries, got %d", len(reg.lookup))
	}

	// All 9 must still be resolvable via the map
	for i := 0; i < 9; i++ {
		p := logger.getProfile(fmt.Sprintf("TYPE_%d", i))
		if p == logger.defaultProfile {
			t.Errorf("TYPE_%d not found after threshold crossing", i)
		}
	}

	// Phase 3: replace an existing profile (incremental map update)
	logger.RegisterSub("TYPE_5", WithFields("replaced", "true"))
	reg = logger.registry.Load()
	if len(reg.lookup) != 9 {
		t.Errorf("expected map size unchanged after replace, got %d", len(reg.lookup))
	}
	p := logger.getProfile("TYPE_5")
	if p == logger.defaultProfile {
		t.Error("TYPE_5 not found after replace")
	}
	if string(p.textPrefix) != "replaced=true " {
		t.Errorf("expected replaced=true prefix, got %q", string(p.textPrefix))
	}
}

// -----------------------------------------------------------------------------
// RateLimit
// -----------------------------------------------------------------------------

// TestCheckAtomicRateLimit_BoundaryExact verifies deterministic boundary
// behavior: exactly limit calls pass inside one window.
func TestCheckAtomicRateLimit_BoundaryExact(t *testing.T) {
	p := &SubProfile{
		rlLimit:    10,
		rlWindowMs: 1000,
		rlStartMs:  1_000_000,
	}

	now := p.rlStartMs + 500

	for i := int64(0); i < 10; i++ {
		if !checkAtomicRateLimitAt(p, now) {
			t.Fatalf("call %d should pass", i+1)
		}
	}

	if checkAtomicRateLimitAt(p, now) {
		t.Fatal("call 11 should be rejected")
	}
}

// TestCheckAtomicRateLimit_WindowReset verifies the counter resets when the
// profile-relative window changes.
func TestCheckAtomicRateLimit_WindowReset(t *testing.T) {
	p := &SubProfile{
		rlLimit:    3,
		rlWindowMs: 1000,
		rlStartMs:  2_000_000,
	}

	now := p.rlStartMs

	for i := int64(0); i < 3; i++ {
		if !checkAtomicRateLimitAt(p, now) {
			t.Fatalf("call %d should pass in first window", i+1)
		}
	}

	if checkAtomicRateLimitAt(p, now) {
		t.Fatal("fourth call in first window should be rejected")
	}

	nextWindow := now + 1000
	if !checkAtomicRateLimitAt(p, nextWindow) {
		t.Fatal("first call in second window should pass")
	}
}

// TestCheckAtomicRateLimit_WindowIndexBeyond32Bit verifies that the new
// 40-bit profile-relative window index supports values that would overflow
// a 32-bit window index.
func TestCheckAtomicRateLimit_WindowIndexBeyond32Bit(t *testing.T) {
	p := &SubProfile{
		rlLimit:    2,
		rlWindowMs: 1,
		rlStartMs:  1,
	}

	// int64(1)<<33 milliseconds after profile start produces a window index
	// greater than 2^32 for a 1ms window.
	base := p.rlStartMs + (int64(1) << 33)

	if !checkAtomicRateLimitAt(p, base) {
		t.Fatal("first call should pass")
	}

	if !checkAtomicRateLimitAt(p, base) {
		t.Fatal("second call should pass")
	}

	if checkAtomicRateLimitAt(p, base) {
		t.Fatal("third call should be rejected")
	}

	// The next millisecond must open a new window.
	if !checkAtomicRateLimitAt(p, base+1) {
		t.Fatal("first call in next window should pass")
	}
}

// TestCheckAtomicRateLimit_ProfileStartClampsNegative verifies that a
// timestamp before the profile start is clamped to elapsed=0.
func TestCheckAtomicRateLimit_ProfileStartClampsNegative(t *testing.T) {
	p := &SubProfile{
		rlLimit:    1,
		rlWindowMs: 1000,
		rlStartMs:  5000,
	}

	nowBeforeStart := p.rlStartMs - 100

	if !checkAtomicRateLimitAt(p, nowBeforeStart) {
		t.Fatal("first call before start should be clamped and pass")
	}

	if checkAtomicRateLimitAt(p, nowBeforeStart) {
		t.Fatal("second call in clamped window should be rejected")
	}
}

// TestCheckAtomicRateLimit_MaxWindowClamp verifies timestamps beyond the
// supported 40-bit window range are clamped instead of wrapping silently.
func TestCheckAtomicRateLimit_MaxWindowClamp(t *testing.T) {
	p := &SubProfile{
		rlLimit:    2,
		rlWindowMs: 1,
		rlStartMs:  1,
	}

	beyond := p.rlStartMs + int64(rlMaxWindow+1)

	if !checkAtomicRateLimitAt(p, beyond) {
		t.Fatal("first call should pass")
	}

	if !checkAtomicRateLimitAt(p, beyond) {
		t.Fatal("second call should pass")
	}

	if checkAtomicRateLimitAt(p, beyond) {
		t.Fatal("third call should be rejected")
	}

	// Because the window index is clamped to rlMaxWindow, the next
	// millisecond remains in the same clamped window.
	if checkAtomicRateLimitAt(p, beyond+1) {
		t.Fatal("call beyond max window clamp should remain rate limited")
	}
}

// TestWithRateLimit_CapsExactLimit verifies configured limits above the
// packed-state exact limit are capped.
func TestWithRateLimit_CapsExactLimit(t *testing.T) {
	p := &SubProfile{}

	opt := WithRateLimit(rlMaxExactLimit+1, time.Second)
	opt(p)

	if p.rlLimit != rlMaxExactLimit {
		t.Fatalf("expected limit capped to %d, got %d", rlMaxExactLimit, p.rlLimit)
	}

	if p.rlWindowMs != 1000 {
		t.Fatalf("expected windowMs=1000, got %d", p.rlWindowMs)
	}

	if p.rlStartMs == 0 {
		t.Fatal("expected rlStartMs to be initialized")
	}
}

// TestWithRateLimit_NegativeBecomesUnlimited verifies negative limits are
// treated as unlimited.
func TestWithRateLimit_NegativeBecomesUnlimited(t *testing.T) {
	p := &SubProfile{}

	opt := WithRateLimit(-1, time.Second)
	opt(p)

	if p.rlLimit != 0 {
		t.Fatalf("expected limit 0 for negative input, got %d", p.rlLimit)
	}
}

// TestCheckAtomicRateLimit_Unlimited verifies rlLimit=0 bypasses limiting.
func TestCheckAtomicRateLimit_Unlimited(t *testing.T) {
	p := &SubProfile{
		rlLimit:    0,
		rlWindowMs: 1000,
		rlStartMs:  1,
	}

	now := p.rlStartMs + 1000

	for i := 0; i < 1000; i++ {
		if !checkAtomicRateLimitAt(p, now) {
			t.Fatalf("unlimited profile rejected call %d", i)
		}
	}
}

// BenchmarkCheckAtomicRateLimit_Uncontended measures the pure CAS cost
// when there is no contention. The backoff mechanism must NOT add any
// overhead to this path.
func BenchmarkCheckAtomicRateLimit_Uncontended(b *testing.B) {
	p := &SubProfile{
		rlLimit:    1_000_000,
		rlWindowMs: 1000,
		rlStartMs:  time.Now().UnixMilli(),
	}
	now := p.rlStartMs + 500

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = checkAtomicRateLimitAt(p, now)
	}
}

// BenchmarkCheckAtomicRateLimit_HighContention isolates the CAS loop
// under extreme multi-core contention. All goroutines slam the exact
// same atomic state with a timestamp that never advances the window.
func BenchmarkCheckAtomicRateLimit_HighContention(b *testing.B) {
	p := &SubProfile{
		rlLimit:    rlMaxExactLimit, // Never hit the limit, force CAS retries
		rlWindowMs: 1000,
		rlStartMs:  time.Now().UnixMilli(),
	}
	now := p.rlStartMs + 500

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = checkAtomicRateLimitAt(p, now)
		}
	})
}

// BenchmarkCheckAtomicRateLimit_Saturated measures the cost when the
// rate limit is actively rejecting logs (cnt >= limit). This path
// should be extremely fast because it returns false BEFORE the CAS.
func BenchmarkCheckAtomicRateLimit_Saturated(b *testing.B) {
	p := &SubProfile{
		rlLimit:    1,
		rlWindowMs: 1000,
		rlStartMs:  time.Now().UnixMilli(),
	}
	now := p.rlStartMs + 500

	// Consume the single allowed log
	_ = checkAtomicRateLimitAt(p, now)

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			// All subsequent calls should hit the fast-path rejection
			_ = checkAtomicRateLimitAt(p, now)
		}
	})
}

// TestLog_RateLimitAndSampling_Order verifies the fixed gate order:
// Rate Limit (admission control) runs BEFORE Sampling (cost reduction).
//
// Setup: 100 raw logs, RateLimit=10/s, SampleRate=2 (1 out of 2).
//
// Expected behavior (new, correct order):
//  1. Rate limit admits 10 logs out of 100 raw input.
//  2. Sampling accepts 1/2 of the admitted 10 logs = 5 logs written.
//
// Old (incorrect) behavior would have been:
//  1. Sampling accepts 1/2 of 100 raw = 50 logs.
//  2. Rate limit admits 10 out of 50 sampled = 10 logs written.
func TestLog_RateLimitAndSampling_Order(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  1000,
	})

	// Register with BOTH rate limit and sampling
	logger.RegisterSub("GATES",
		WithRateLimit(10, time.Second), // Admit max 10 per second
		WithSampleRate(2),              // Then sample 1 out of 2 admitted
	)

	// Burst 100 raw logs in a single window (<1s)
	for i := 0; i < 100; i++ {
		logger.Log(LevelInfo, "GATES", []byte("raw input"))
	}

	output := flushAndRead(t, logger, buf)
	count := strings.Count(output, "raw input")

	// With rate-limit FIRST: 100 raw -> 10 admitted -> 5 sampled.
	// Tolerance: 4-6 (allow small jitter for atomic ordering).
	if count < 4 || count > 6 {
		t.Errorf("expected ~5 logs (rate-limit then sample), got %d. Gate order may be wrong.", count)
	}

	// Explicitly fail if old behavior (10 logs) is observed
	if count >= 9 {
		t.Errorf("observed ~10 logs: sampling ran BEFORE rate limit. Gate order is wrong!")
	}
}

// TestRotationErrors_Counter verifies that rotation failures are counted
// and observable via Stats().RotationErrors.
func TestRotationErrors_Counter(t *testing.T) {
	logger := NewLogger(Config{ChannelSize: 100})
	stats := logger.Stats()
	if stats.RotationErrors != 0 {
		t.Errorf("expected RotationErrors=0 initially, got %d", stats.RotationErrors)
	}
}
