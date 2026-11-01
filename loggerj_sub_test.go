package loggerj

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSub_RegistersAndLogs(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	sub := logger.Sub("HTTP", WithFields("env", "prod"))
	if sub.Name() != "HTTP" {
		t.Errorf("Name() = %q", sub.Name())
	}
	sub.InfoString("request", "k", "v")
	sub.InfoFields([]byte("typed"), Int("status", 200))
	output := flushAndRead(t, logger, buf)

	for _, want := range []string{`"type":"HTTP"`, `"env":"prod"`, `"k":"v"`, `"status":200`} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %s in %s", want, output)
		}
	}
}

func TestSub_WithoutOptionsKeepsExistingProfile(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})

	logger.RegisterSub("LIM", WithRateLimit(2, time.Second))
	sub := logger.Sub("LIM") // must not reset the rate limit
	for i := 0; i < 5; i++ {
		sub.InfoString("limited")
	}

	output := flushAndRead(t, logger, buf)
	if got := strings.Count(output, "limited"); got != 2 {
		t.Errorf("got %d entries, want 2 (the existing rate limit)", got)
	}
}

// TestSub_GatesMatchLogType guards passGates against drifting from
// checkGatesAfterLevel: the same profile must admit the same entries through a
// SubLogger and through the log-type API.
func TestSub_GatesMatchLogType(t *testing.T) {
	opts := []SubOption{WithSampleRate(10), WithRateLimit(5, time.Second)}

	l1, b1, _ := setupTestLogger(t, Config{FlushTimeout: 10 * time.Millisecond, ChannelSize: 1000})
	l1.RegisterSub("G", opts...)
	for i := 0; i < 100; i++ {
		l1.InfoString("G", "gate")
	}
	n1 := strings.Count(flushAndRead(t, l1, b1), "gate")

	l2, b2, _ := setupTestLogger(t, Config{FlushTimeout: 10 * time.Millisecond, ChannelSize: 1000})
	sub := l2.Sub("G", opts...)
	for i := 0; i < 100; i++ {
		sub.InfoString("gate")
	}
	n2 := strings.Count(flushAndRead(t, l2, b2), "gate")

	if n1 != n2 || n1 != 5 {
		t.Errorf("log-type API admitted %d, SubLogger admitted %d, want 5 for both", n1, n2)
	}
}

func TestSub_LevelFilter(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})
	logger.SetLevelValue(LevelWarn)

	sub := logger.Sub("LV")
	sub.InfoString("filtered info")
	sub.InfoFields([]byte("filtered typed info"))
	sub.WarnString("kept warn")
	sub.ErrorFields([]byte("kept typed error"))

	output := flushAndRead(t, logger, buf)
	if strings.Contains(output, "filtered") {
		t.Errorf("filtered entries were written: %s", output)
	}
	if !strings.Contains(output, "kept warn") || !strings.Contains(output, "kept typed error") {
		t.Errorf("expected entries missing: %s", output)
	}
}

func TestSub_CallerFile(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:    true,
		FlushTimeout:  10 * time.Millisecond,
		ChannelSize:   100,
		IncludeCaller: true,
	})
	sub := logger.Sub("CALL")

	sub.Info([]byte("bytes"))
	sub.InfoString("string")
	sub.InfoFields([]byte("typed"))

	output := flushAndRead(t, logger, buf)
	if got := strings.Count(output, `"file":"loggerj_sub_test.go"`); got != 3 {
		t.Errorf("caller file reported %d times, want 3: %s", got, output)
	}
}

func TestSub_SyncMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub.log")
	logger := NewLogger(Config{SyncMode: true, OutputFile: path, DurabilityTier: Direct})

	sub := logger.Sub("SYNC", WithFields("env", "prod"))
	sub.InfoString("sync message")
	sub.ErrorFields([]byte("sync typed"), Int("n", 1))
	logger.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[SYNC]", "env=prod", "sync message", "sync typed", "n=1"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %q in %s", want, data)
		}
	}
}

func TestSub_ZeroAlloc(t *testing.T) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   1, // drop-path keeps the entry pool warm
		IncludeCaller: false,
	})
	sub := logger.Sub("ALLOC")

	for i := 0; i < 100; i++ {
		sub.InfoString("zero alloc test", "key", "value")
	}
	allocs := testing.AllocsPerRun(1000, func() {
		sub.InfoString("zero alloc test", "key", "value")
	})
	if allocs > 0 {
		t.Errorf("expected 0 allocs/op, got %.1f", allocs)
	}
}
