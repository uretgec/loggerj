package loggerj

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// -----------------------------------------------------------------------------
// Text output: control characters must not start new log lines
// -----------------------------------------------------------------------------

func TestAppendTextBytes_Table(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"plain text", "plain text"},
		{"tab\tkept", "tab\tkept"},
		{"line1\nline2", `line1\nline2`},
		{"cr\rlf", `cr\rlf`},
		{"\x01\x1f", `\x01\x1f`},
		{"del\x7f", `del\x7f`},
		{"", ""},
		{"ünïcode çğ", "ünïcode çğ"},
	}
	for _, tt := range tests {
		if got := string(appendTextString(nil, tt.in)); got != tt.want {
			t.Errorf("appendTextString(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTextOutput_EscapesControlCharacters(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.InfoString("FORGE", "first\nINFO [FAKE] forged line", "k", "v\r\nforged")
	logger.InfoFields("FORGE", []byte("typed"), Str("s", "a\nb"), Err(os.ErrNotExist))
	output := flushAndRead(t, logger, buf)

	if got := strings.Count(output, "\n"); got != 2 {
		t.Errorf("expected exactly 2 log lines, got %d newlines: %q", got, output)
	}
	if !strings.Contains(output, `first\nINFO [FAKE] forged line`) {
		t.Errorf("message was not escaped: %q", output)
	}
	if !strings.Contains(output, `k=v\r\nforged`) {
		t.Errorf("field value was not escaped: %q", output)
	}
	if !strings.Contains(output, `s=a\nb`) {
		t.Errorf("typed field value was not escaped: %q", output)
	}
}

// -----------------------------------------------------------------------------
// Reserved keys
// -----------------------------------------------------------------------------

func TestRegisterSub_ReservedKeysIgnored(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.RegisterSub("RES", WithFields("msg", "evil", "env", "prod", "ts", "1", "region", "eu"))
	logger.Info("RES", []byte("hello"))
	output := flushAndRead(t, logger, buf)

	var got map[string]interface{}
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, output)
	}
	if got["msg"] != "hello" {
		t.Errorf("msg = %v, want hello", got["msg"])
	}
	if got["env"] != "prod" || got["region"] != "eu" {
		t.Errorf("non-reserved fields missing: %v", got)
	}
	if strings.Count(output, `"msg":`) != 1 || strings.Count(output, `"ts":`) != 1 {
		t.Errorf("duplicate reserved keys in output: %s", output)
	}
}

func TestFilterReservedFields_NoChangeReturnsInput(t *testing.T) {
	in := []string{"a", "1", "b", "2"}
	out := filterReservedFields("X", in)
	if len(out) != 4 || &out[0] != &in[0] {
		t.Errorf("expected the input slice back, got %v", out)
	}
}

// -----------------------------------------------------------------------------
// FileMode
// -----------------------------------------------------------------------------

func TestFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not meaningful on Windows")
	}

	check := func(t *testing.T, path string, want os.FileMode) {
		t.Helper()
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Mode().Perm(); got != want {
			t.Errorf("%s has mode %v, want %v", filepath.Base(path), got, want)
		}
	}

	t.Run("async", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "async.log")
		logger := NewLogger(Config{OutputFile: path, FileMode: 0o600, MaxFileSize: 10})
		check(t, path, 0o600)

		// A rotated replacement file gets the same mode.
		_, _ = logger.currentWriter.WriteString("rotate me\n")
		logger.currentSize = 1 << 20
		if rotated, err := logger.rotateLogFile(); err != nil || !rotated {
			t.Fatalf("rotated=%v err=%v", rotated, err)
		}
		check(t, path, 0o600)
		logger.Close()
	})

	t.Run("sync", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sync.log")
		logger := NewLogger(Config{SyncMode: true, OutputFile: path, FileMode: 0o600})
		defer logger.Close()
		check(t, path, 0o600)
	})
}

// -----------------------------------------------------------------------------
// Drops by level, ERROR wait
// -----------------------------------------------------------------------------

func TestStats_DropsByLevel(t *testing.T) {
	logger := NewLogger(Config{ChannelSize: 1}) // no worker: the channel fills up

	logger.InfoString("D", "fills the channel")
	logger.WarnString("D", "dropped")
	logger.WarnString("D", "dropped")
	logger.ErrorString("D", "dropped")

	st := logger.Stats()
	if st.Drops != 3 {
		t.Errorf("Drops = %d, want 3", st.Drops)
	}
	if st.DropsByLevel[LevelWarn] != 2 || st.DropsByLevel[LevelError] != 1 ||
		st.DropsByLevel[LevelInfo] != 0 || st.DropsByLevel[LevelDebug] != 0 {
		t.Errorf("DropsByLevel = %v, want warn=2 error=1", st.DropsByLevel)
	}

	logger.ResetDrops()
	if st := logger.Stats(); st.Drops != 0 || st.DropsByLevel[LevelWarn] != 0 {
		t.Errorf("ResetDrops left counters behind: %+v", st)
	}
}

func TestErrorBlockTimeout_DropsAfterTimeout(t *testing.T) {
	logger := NewLogger(Config{ChannelSize: 1, ErrorBlockTimeout: 40 * time.Millisecond})
	logger.InfoString("E", "fills the channel")

	start := time.Now()
	logger.ErrorString("E", "no worker, must wait and then drop")
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Errorf("ERROR call returned after %v, expected to wait ~40ms", elapsed)
	}
	if got := logger.Stats().DropsByLevel[LevelError]; got != 1 {
		t.Errorf("ERROR drops = %d, want 1", got)
	}

	// Other levels never wait.
	start = time.Now()
	logger.WarnString("E", "dropped at once")
	if elapsed := time.Since(start); elapsed > 30*time.Millisecond {
		t.Errorf("WARN call waited %v", elapsed)
	}
}

func TestErrorBlockTimeout_WaitsForSpace(t *testing.T) {
	logger := NewLogger(Config{
		ChannelSize:       1,
		FlushTimeout:      10 * time.Millisecond,
		ErrorBlockTimeout: 5 * time.Second,
	})
	logger.InfoString("E", "fills the channel")

	buf := &safeBuffer{}
	go func() {
		time.Sleep(30 * time.Millisecond)
		logger.StartBackgroundWithWriter(context.Background(), buf)
	}()

	logger.ErrorString("E", "must not be dropped") // blocks until the worker drains
	<-logger.started
	logger.Close()

	out := buf.String()
	if !strings.Contains(out, "fills the channel") || !strings.Contains(out, "must not be dropped") {
		t.Errorf("entries missing: %q", out)
	}
	if logger.Drops() != 0 {
		t.Errorf("Drops = %d, want 0", logger.Drops())
	}
}

// -----------------------------------------------------------------------------
// slog: context fields
// -----------------------------------------------------------------------------

func TestSlogHandler_ContextFields(t *testing.T) {
	slogger, logger, buf := setupSlogTest(t)

	ctx := context.WithValue(context.Background(), TraceIDKey, "abc-123")
	ctx = context.WithValue(ctx, RequestIDKey, "req-9")
	slogger.InfoContext(ctx, "with ids", "k", "v")
	slogger.Info("without ids")

	output := flushAndRead(t, logger, buf)
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines: %q", len(lines), output)
	}
	if !strings.Contains(lines[0], `"trace_id":"abc-123"`) || !strings.Contains(lines[0], `"request_id":"req-9"`) {
		t.Errorf("context ids missing: %s", lines[0])
	}
	if strings.Contains(lines[1], "trace_id") {
		t.Errorf("unexpected trace_id: %s", lines[1])
	}
}
