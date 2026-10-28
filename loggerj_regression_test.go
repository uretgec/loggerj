package loggerj

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// -----------------------------------------------------------------------------
// Formatting regressions (v1.4.4)
// -----------------------------------------------------------------------------

func TestAppendDuration_Table(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{999, "999ns"},
		{250 * time.Microsecond, "250\u00b5s"},
		{1500 * time.Microsecond, "1ms"},
		{150 * time.Millisecond, "150ms"},
		{2005 * time.Millisecond, "2.005s"}, // was "2.5s"
		{2500 * time.Millisecond, "2.500s"},
		{5 * time.Second, "5s"},
		{59*time.Second + 999*time.Millisecond, "59.999s"},
		{2*time.Minute + 30*time.Second, "2m30s"},
		{2 * time.Minute, "2m"},
		{90 * time.Minute, "1h30m"},
		{100 * time.Hour, "100h"},
		{time.Hour + 5*time.Second, "1h5s"},
		{-1500 * time.Millisecond, "-1.500s"},
		{-5 * time.Second, "-5s"},
	}
	for _, tt := range tests {
		if got := string(appendDuration(nil, tt.d)); got != tt.want {
			t.Errorf("appendDuration(%d) = %q, want %q", int64(tt.d), got, tt.want)
		}
	}
}

func TestAppendJSONString_InvalidUTF8(t *testing.T) {
	out := appendJSONString(nil, "a\xffb\xc3")

	var got string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON: %v\noutput=%q", err, out)
	}
	if want := "a\ufffdb\ufffd"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTypedFields_JSON_NonFiniteFloat(t *testing.T) {
	fields := []Field{
		Float64("nan", math.NaN()),
		Float64("pinf", math.Inf(1)),
		Float64("ninf", math.Inf(-1)),
		Float64("ok", 1.5),
	}
	out := append([]byte{'{'}, appendTypedFieldsJSON(nil, fields)...)
	out = append(out, '}')

	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON: %v\noutput=%s", err, out)
	}
	if got["nan"] != "NaN" || got["pinf"] != "+Inf" || got["ninf"] != "-Inf" {
		t.Errorf("unexpected non-finite encoding: %v", got)
	}
	if got["ok"] != 1.5 {
		t.Errorf("finite float changed: %v", got["ok"])
	}
}

func TestTypedFields_Text_SkippedFieldSpacing(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	// Err(nil) is a zero-value Field and is skipped by the encoder.
	logger.InfoFields("SPACING", []byte("msg"), Err(nil), Str("a", "b"))
	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, "msg a=b") {
		t.Errorf("expected single space before the first field, got: %q", output)
	}
	if strings.Contains(output, "msg  a=b") {
		t.Errorf("double space before the first field, got: %q", output)
	}
}

func TestEntry_Reset_ClearsReferences(t *testing.T) {
	e := &Entry{}
	e.Fields = append(e.Fields[:0], "key", "value")
	e.FieldsV = append(e.FieldsV[:0], Str("k", "v"))

	e.Reset()

	if s := e.Fields[:2]; s[0] != "" || s[1] != "" {
		t.Errorf("Fields backing array still references strings: %q", s)
	}
	if v := e.FieldsV[:1]; v[0] != (Field{}) {
		t.Errorf("FieldsV backing array still references strings: %+v", v[0])
	}
}

// -----------------------------------------------------------------------------
// Context methods: the level filter must run before any ctx.Value lookup
// -----------------------------------------------------------------------------

type countingCtx struct {
	context.Context
	calls *int
}

func (c countingCtx) Value(key any) any {
	*c.calls++
	return c.Context.Value(key)
}

func TestInfoCtx_FilteredSkipsContextLookups(t *testing.T) {
	logger := NewLogger(Config{ChannelSize: 100}) // default level: Info

	calls := 0
	ctx := countingCtx{Context: context.Background(), calls: &calls}

	logger.DebugCtx(ctx, "T", "filtered")
	if calls != 0 {
		t.Errorf("filtered DebugCtx performed %d ctx.Value lookups, want 0", calls)
	}

	logger.InfoCtx(ctx, "T", "passes")
	if calls == 0 {
		t.Error("InfoCtx should look up context keys")
	}
}

// -----------------------------------------------------------------------------
// Rate limit: a stale timestamp must not reset a newer window
// -----------------------------------------------------------------------------

func TestCheckAtomicRateLimit_StaleTimestampDoesNotResetWindow(t *testing.T) {
	p := &SubProfile{
		rlLimit:    2,
		rlWindowMs: 1000,
		rlStartMs:  1_000_000,
	}
	newer := p.rlStartMs + 1000 // window 1
	older := p.rlStartMs        // window 0

	for i := 0; i < 2; i++ {
		if !checkAtomicRateLimitAt(p, newer) {
			t.Fatalf("call %d in the newer window should pass", i+1)
		}
	}
	if checkAtomicRateLimitAt(p, newer) {
		t.Fatal("third call in the newer window should be rejected")
	}
	if checkAtomicRateLimitAt(p, older) {
		t.Fatal("a stale timestamp must not reset the newer window")
	}
}

// -----------------------------------------------------------------------------
// Rotation
// -----------------------------------------------------------------------------

func TestRotation_BackupCount(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "app.log")
	logger := NewLogger(Config{
		OutputFile:     logFile,
		MaxFileSize:    10,
		MaxBackupFiles: 3,
	})

	// Drive rotateLogFile directly so the number of rotations is
	// deterministic (the worker only checks rotation on its own flushes).
	for i := 1; i <= 6; i++ {
		_, _ = logger.currentWriter.WriteString(fmt.Sprintf("gen-%d\n", i))
		logger.currentSize = 1 << 20
		rotated, err := logger.rotateLogFile()
		if err != nil || !rotated {
			t.Fatalf("rotation %d: rotated=%v err=%v", i, rotated, err)
		}
	}
	logger.Close()

	// MaxBackupFiles=3 keeps exactly .1, .2 and .3 (it used to keep 2).
	// .1 is the newest rotated generation.
	for i, want := range map[int]string{1: "gen-6", 2: "gen-5", 3: "gen-4"} {
		name := fmt.Sprintf("%s.%d", logFile, i)
		data, err := os.ReadFile(name)
		if err != nil {
			t.Errorf("expected backup %s: %v", name, err)
			continue
		}
		if !strings.Contains(string(data), want) {
			t.Errorf("%s = %q, want it to contain %q", name, data, want)
		}
	}
	if _, err := os.Stat(logFile + ".4"); !os.IsNotExist(err) {
		t.Errorf("backup .4 must not exist (err=%v)", err)
	}
	if _, err := os.Stat(logFile + ".rotating"); !os.IsNotExist(err) {
		t.Errorf("temporary .rotating file must not be left behind (err=%v)", err)
	}
}

func TestRotation_RenameFailureKeepsLog(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("rename-over-directory semantics differ on Windows")
	}

	logFile := filepath.Join(t.TempDir(), "app.log")
	logger := NewLogger(Config{
		OutputFile:     logFile,
		MaxFileSize:    100,
		MaxBackupFiles: 2,
	})

	// A non-empty directory at the temporary name makes the first rename fail.
	blocker := logFile + ".rotating"
	if err := os.MkdirAll(filepath.Join(blocker, "x"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, _ = logger.currentWriter.WriteString("keep me\n")
	logger.currentSize = 1 << 20

	rotated, err := logger.rotateLogFile()
	if err == nil {
		t.Fatal("expected a rename error")
	}
	if !rotated {
		t.Error("rotated must be true so the worker refreshes its writer")
	}
	if got := logger.rotationErrors.Load(); got != 1 {
		t.Errorf("rotationErrors = %d, want 1", got)
	}
	logger.Close()

	data, rerr := os.ReadFile(logFile)
	if rerr != nil {
		t.Fatalf("live log is gone: %v", rerr)
	}
	if !strings.Contains(string(data), "keep me") {
		t.Errorf("live log was truncated, content: %q", data)
	}
}

// -----------------------------------------------------------------------------
// Sync mode
// -----------------------------------------------------------------------------

func TestSyncMode_NoBufferedFileHandle(t *testing.T) {
	logger := NewLogger(Config{
		SyncMode:   true,
		OutputFile: filepath.Join(t.TempDir(), "s.log"),
	})
	defer logger.Close()

	if logger.currentFile != nil || logger.currentWriter != nil {
		t.Error("sync mode must not open the async log file handle")
	}
	if logger.logCh != nil {
		t.Error("sync mode must not allocate the log channel")
	}
}

func TestSyncMode_FlushWritesBufferedTiers(t *testing.T) {
	for _, tier := range []DurabilityTier{OSBuffered, FsyncEveryN} {
		t.Run(tier.String(), func(t *testing.T) {
			logFile := filepath.Join(t.TempDir(), "flush.log")
			logger := NewLogger(Config{
				SyncMode:         true,
				OutputFile:       logFile,
				DurabilityTier:   tier,
				FsyncEveryNCount: 1000,
			})
			defer logger.Close()

			for i := 0; i < 5; i++ {
				logger.InfoString("FLUSH", fmt.Sprintf("entry-%d", i))
			}
			logger.Flush()

			data, err := os.ReadFile(logFile)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(string(data), "\n"); got != 5 {
				t.Errorf("after Flush the file has %d lines, want 5", got)
			}
		})
	}
}

func TestSyncMode_BufferedFlushErrorCounted(t *testing.T) {
	logger := NewLogger(Config{
		SyncMode:       true,
		OutputFile:     filepath.Join(t.TempDir(), "e.log"),
		DurabilityTier: OSBuffered,
	})

	// Disable the 10ms time-based flush so the entry stays buffered.
	logger.syncLastFlushMs.Store(time.Now().Add(time.Hour).UnixMilli())
	logger.InfoString("ERR", "buffered")

	_ = logger.syncFile.Close() // make the next flush fail
	logger.Flush()

	if logger.Stats().SyncWriteErrors == 0 {
		t.Error("a failed flush of the buffered tier must be counted in SyncWriteErrors")
	}
}

func TestConfig_FsyncEveryNCountDefault(t *testing.T) {
	logger := NewLogger(Config{SyncMode: true, DurabilityTier: FsyncEveryN})
	defer logger.Close()

	if logger.cfg.FsyncEveryNCount != 100 {
		t.Errorf("FsyncEveryNCount = %d, want the documented default 100", logger.cfg.FsyncEveryNCount)
	}
}

// -----------------------------------------------------------------------------
// slog adapter
// -----------------------------------------------------------------------------

// TestSlogHandler_LongMessageNoExtraAllocs guards against a []byte(r.Message)
// conversion, which allocates for messages longer than 32 bytes.
func TestSlogHandler_LongMessageNoExtraAllocs(t *testing.T) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   1, // drop-path keeps the entry pool warm
		IncludeCaller: false,
	})
	handler := NewSlogHandler(logger, "TEST")

	measure := func(msg string) float64 {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, msg, 0)
		r.AddAttrs(slog.String("key", "value"))
		for i := 0; i < 100; i++ {
			_ = handler.Handle(context.Background(), r)
		}
		return testing.AllocsPerRun(1000, func() {
			_ = handler.Handle(context.Background(), r)
		})
	}

	short := measure("short message")
	long := measure(strings.Repeat("x", 200))
	if long > short {
		t.Errorf("a 200-byte message allocates more (%.1f) than a short one (%.1f)", long, short)
	}
}

// TestSlogHandler_CallerFromRecordPC verifies the caller comes from
// slog.Record.PC, i.e. the code that called slogger.Info.
func TestSlogHandler_CallerFromRecordPC(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:    true,
		FlushTimeout:  10 * time.Millisecond,
		ChannelSize:   100,
		IncludeCaller: true,
	})
	slogger := slog.New(NewSlogHandler(logger, "SLOG"))

	slogger.Info("with caller")
	output := flushAndRead(t, logger, buf)

	if !strings.Contains(output, `"file":"loggerj_regression_test.go"`) {
		t.Errorf("expected the caller file from Record.PC, got: %s", output)
	}
}
