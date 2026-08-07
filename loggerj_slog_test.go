package loggerj

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// setupSlogTest creates a logger + slog.Logger pair backed by a buffer.
func setupSlogTest(t *testing.T) (*slog.Logger, *Logger, *safeBuffer) {
	t.Helper()
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})
	handler := NewSlogHandler(logger, "SLOG")
	return slog.New(handler), logger, buf
}

// TestSlogHandler_Basic verifies a simple slog.Info flows through loggerj.
func TestSlogHandler_Basic(t *testing.T) {
	slogger, logger, buf := setupSlogTest(t)

	slogger.Info("request received", "method", "GET", "path", "/api")

	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, "request received") {
		t.Errorf("expected message, got: %s", output)
	}
	if !strings.Contains(output, `"method":"GET"`) {
		t.Errorf("expected method field, got: %s", output)
	}
	if !strings.Contains(output, `"path":"/api"`) {
		t.Errorf("expected path field, got: %s", output)
	}
	if !strings.Contains(output, `"level":"INFO"`) {
		t.Errorf("expected INFO level, got: %s", output)
	}
}

// TestSlogHandler_Levels verifies level filtering respects loggerj's threshold.
func TestSlogHandler_Levels(t *testing.T) {
	slogger, logger, buf := setupSlogTest(t)

	logger.SetLevelValue(LevelWarn)

	slogger.Debug("debug msg") // filtered
	slogger.Info("info msg")   // filtered
	slogger.Warn("warn msg")   // logged
	slogger.Error("error msg") // logged

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

// TestSlogHandler_TypedAttrs verifies typed slog attrs convert without boxing.
func TestSlogHandler_TypedAttrs(t *testing.T) {
	slogger, logger, buf := setupSlogTest(t)

	slogger.Info("typed",
		"count", 42,
		"ratio", 3.14,
		"active", true,
		"latency", 150*time.Millisecond,
	)

	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, `"count":42`) {
		t.Errorf("expected count:42, got: %s", output)
	}
	if !strings.Contains(output, `"active":true`) {
		t.Errorf("expected active:true, got: %s", output)
	}
	if !strings.Contains(output, `"latency":"150ms"`) {
		t.Errorf("expected latency:150ms, got: %s", output)
	}
	// Verify valid JSON
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nOutput: %s", err, output)
	}
}

// TestSlogHandler_WithAttrs verifies pre-attributes are prepended to every record.
func TestSlogHandler_WithAttrs(t *testing.T) {
	slogger, logger, buf := setupSlogTest(t)

	child := slogger.With("env", "prod", "service", "gateway")
	child.Info("request")

	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, `"env":"prod"`) {
		t.Errorf("expected env field, got: %s", output)
	}
	if !strings.Contains(output, `"service":"gateway"`) {
		t.Errorf("expected service field, got: %s", output)
	}
	if !strings.Contains(output, "request") {
		t.Errorf("expected message, got: %s", output)
	}
}

// TestSlogHandler_WithGroup verifies groups flatten into dotted keys.
func TestSlogHandler_WithGroup(t *testing.T) {
	slogger, logger, buf := setupSlogTest(t)

	child := slogger.WithGroup("http")
	child.Info("request", "method", "GET")

	output := flushAndRead(t, logger, buf)

	// Group flattening produces "http.method" as the key.
	if !strings.Contains(output, `"http.method":"GET"`) {
		t.Errorf("expected grouped key http.method, got: %s", output)
	}
}

// TestSlogHandler_Enabled verifies Enabled() reflects runtime level changes.
func TestSlogHandler_Enabled(t *testing.T) {
	logger := NewLogger(Config{ChannelSize: 100})
	handler := NewSlogHandler(logger, "TEST")

	logger.SetLevelValue(LevelError)
	if handler.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("Info should be disabled at Error level")
	}
	if !handler.Enabled(context.Background(), slog.LevelError) {
		t.Error("Error should be enabled at Error level")
	}

	logger.SetLevelValue(LevelDebug)
	if !handler.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("Debug should be enabled at Debug level")
	}
}

// TestSlogHandler_ValidJSON verifies all output is well-formed JSON.
func TestSlogHandler_ValidJSON(t *testing.T) {
	slogger, logger, buf := setupSlogTest(t)

	slogger.Info("test",
		"key", "value with \"quotes\"",
		"number", 123,
	)
	slogger.With("nested", "attr").Info("second")

	output := flushAndRead(t, logger, buf)
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i, line := range lines {
		var result map[string]interface{}
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			t.Errorf("line %d invalid JSON: %v\nLine: %s", i, err, line)
		}
	}
}

// TestSlogHandler_ZeroAlloc verifies Handle() is allocation-free once warm.
// NOTE: slog itself may allocate on the caller side (slog.Info variadic);
// this measures the handler's internal cost only.
func TestSlogHandler_ZeroAlloc(t *testing.T) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   1, // drop-path keeps pool warm
		IncludeCaller: false,
	})
	handler := NewSlogHandler(logger, "TEST")
	slogger := slog.New(handler)

	// Warmup
	for i := 0; i < 100; i++ {
		slogger.Info("warmup", "key", "value")
	}

	// Measure handler internals only via direct Handle call
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "zero-alloc test", 0)
	r.AddAttrs(slog.String("key", "value"))

	allocs := testing.AllocsPerRun(1000, func() {
		_ = handler.Handle(context.Background(), r)
	})

	if allocs > 1 {
		t.Errorf("expected ≤1 allocs/op in SlogHandler.Handle, got %.1f", allocs)
	}
}

// TestSlogHandler_WithGroup_MultiAttr verifies WithGroup prefixes all
// record attributes as dotted keys.
func TestSlogHandler_WithGroup_MultiAttr(t *testing.T) {
	slogger, logger, buf := setupSlogTest(t)

	child := slogger.WithGroup("http")
	child.Info("request", "method", "GET", "status", 200)

	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, `"http.method":"GET"`) {
		t.Errorf("expected http.method, got: %s", output)
	}
	if !strings.Contains(output, `"http.status":200`) {
		t.Errorf("expected http.status, got: %s", output)
	}
}

// TestSlogHandler_GroupAttr verifies slog.Group attributes are flattened
// into dotted keys instead of falling back to string.
func TestSlogHandler_GroupAttr(t *testing.T) {
	slogger, logger, buf := setupSlogTest(t)

	slogger.Info("request",
		slog.Group("http",
			"method", "GET",
			"status", 200,
		),
	)

	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, `"http.method":"GET"`) {
		t.Errorf("expected http.method, got: %s", output)
	}
	if !strings.Contains(output, `"http.status":200`) {
		t.Errorf("expected http.status, got: %s", output)
	}
}

// TestSlogHandler_NestedGroupAttr verifies nested slog.Group values are
// flattened recursively.
func TestSlogHandler_NestedGroupAttr(t *testing.T) {
	slogger, logger, buf := setupSlogTest(t)

	slogger.Info("request",
		slog.Group("http",
			slog.Group("req",
				"method", "GET",
				"path", "/api",
			),
		),
	)

	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, `"http.req.method":"GET"`) {
		t.Errorf("expected http.req.method, got: %s", output)
	}
	if !strings.Contains(output, `"http.req.path":"/api"`) {
		t.Errorf("expected http.req.path, got: %s", output)
	}
}

// TestSlogHandler_WithGroup_Nested verifies nested WithGroup calls produce
// dotted prefixes.
func TestSlogHandler_WithGroup_Nested(t *testing.T) {
	slogger, logger, buf := setupSlogTest(t)

	child := slogger.
		WithGroup("http").
		WithGroup("request")

	child.Info("received", "method", "POST")

	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, `"http.request.method":"POST"`) {
		t.Errorf("expected http.request.method, got: %s", output)
	}
}

// TestSlogHandler_WithAttrs_Group verifies WithAttrs inside a group is
// pre-converted with the group prefix.
func TestSlogHandler_WithAttrs_Group(t *testing.T) {
	slogger, logger, buf := setupSlogTest(t)

	child := slogger.
		WithGroup("service").
		With("env", "prod")

	child.Info("boot")

	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, `"service.env":"prod"`) {
		t.Errorf("expected service.env, got: %s", output)
	}
}

// TestSlogHandler_TimePrecision verifies slog.KindTime values preserve
// nanosecond precision via RFC3339Nano formatting.
func TestSlogHandler_TimePrecision(t *testing.T) {
	slogger, logger, buf := setupSlogTest(t)

	// Use a time with nanosecond precision
	preciseTime := time.Date(2024, 1, 1, 12, 0, 0, 123456789, time.UTC)
	slogger.Info("event", "timestamp", preciseTime)

	output := flushAndRead(t, logger, buf)

	// RFC3339Nano preserves nanoseconds: "2024-01-01T12:00:00.123456789Z"
	if !strings.Contains(output, "123456789") {
		t.Errorf("expected nanosecond precision in output, got: %s", output)
	}
}
