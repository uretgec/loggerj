package loggerj

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
	output := flushAndRead(t, logger, buf)
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

	logger.RegisterSub("STRESS", WithRateLimit(10, time.Second))

	var wg sync.WaitGroup
	// 1000 goroutines × 100 logs = 100,000 total requests
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

	// Unix() second-granularity may cause up to 2 window transitions:
	// 2 × 10 = 20 + 2 jitter tolerance
	if count > 22 {
		t.Errorf("CAS lock-free rate limit failed! Expected ≤22 (2 windows), got: %d", count)
	}
	if count < 1 {
		t.Errorf("no logs passed through, count=%d", count)
	}
}

func BenchmarkLog_RateLimited_HighContention(b *testing.B) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   65536,
		IncludeCaller: false,
	})

	// Single profile with a very high limit (no drops, measure pure CAS contention)
	logger.RegisterSub("HOT", WithRateLimit(10000000, time.Second))

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

	// Tight window: 5 logs per second
	logger.RegisterSub("CAS_TEST", WithRateLimit(5, time.Second))

	var wg sync.WaitGroup

	// 50 goroutines × 20 logs = 1000 total requests
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
	time.Sleep(100 * time.Millisecond)

	output := buf.String()
	count := strings.Count(output, "cas")

	// Limit 5, window 1s. Test completes in <1s → max 5 logs (+1 tolerance)
	if count > 6 {
		t.Errorf("rate limit exceeded after CAS reset! Expected ≤5, got: %d", count)
	}
	if count < 1 {
		t.Errorf("no logs passed through, count=%d", count)
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
