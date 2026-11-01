package loggerj

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// -----------------------------------------------------------------------------
// Worker lifecycle: StartBackground, Close, Flush without a worker
// -----------------------------------------------------------------------------

// TestStartBackground_FlushRightAfterStart is the case `go logger.Start(ctx)`
// cannot handle: Flush right after starting must see a running worker.
func TestStartBackground_FlushRightAfterStart(t *testing.T) {
	for i := 0; i < 200; i++ {
		logger := NewLogger(Config{
			FlushTimeout: 10 * time.Millisecond,
			ChannelSize:  100,
		})
		buf := &safeBuffer{}
		ctx, cancel := context.WithCancel(context.Background())

		logger.StartBackgroundWithWriter(ctx, buf)
		logger.InfoString("LIFE", "right after start")
		logger.Flush()

		got := buf.String()
		cancel()
		logger.Close()
		if !strings.Contains(got, "right after start") {
			t.Fatalf("iteration %d: entry lost, output: %q", i, got)
		}
	}
}

func TestStartBackground_Twice(t *testing.T) {
	logger := NewLogger(Config{ChannelSize: 100})
	buf := &safeBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger.StartBackgroundWithWriter(ctx, buf)
	logger.StartBackgroundWithWriter(ctx, buf) // already running: returns at once
	logger.Close()
}

func TestStartBackground_SyncModeReturns(t *testing.T) {
	logger := NewLogger(Config{SyncMode: true})
	done := make(chan struct{})
	go func() {
		logger.StartBackground(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("StartBackground blocked in sync mode")
	}
	logger.Close()
}

// TestClose_StopsWorkerAndDrainsQueue verifies Close alone is enough: it
// writes the queued entries and the worker has exited when it returns.
func TestClose_StopsWorkerAndDrainsQueue(t *testing.T) {
	logger := NewLogger(Config{
		FlushTimeout: time.Hour, // only Close can drain the queue
		ChannelSize:  1000,
	})
	buf := &safeBuffer{}
	logger.StartBackgroundWithWriter(context.Background(), buf)

	for i := 0; i < 100; i++ {
		logger.InfoString("LIFE", fmt.Sprintf("entry-%d", i))
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close returned %v", err)
	}

	select {
	case <-logger.workerDone:
	default:
		t.Error("worker still running after Close")
	}
	if got := strings.Count(buf.String(), "entry-"); got != 100 {
		t.Errorf("got %d entries after Close, want 100 (drops=%d)", got, logger.Drops())
	}
}

func TestClose_Idempotent(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "idem.log")
	logger := NewLogger(Config{OutputFile: logFile, ChannelSize: 100})
	logger.StartBackground(context.Background())
	logger.InfoString("LIFE", "written once")

	if err := logger.Close(); err != nil {
		t.Fatalf("first Close returned %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Errorf("second Close returned %v, want nil", err)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "written once") {
		t.Errorf("entry missing from the log file: %q", data)
	}
}

// TestClose_AfterCancel covers the previously recommended order: cancelling
// the context first, then Close, must not race with the worker's drain.
func TestClose_AfterCancel(t *testing.T) {
	logger := NewLogger(Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})
	buf := &safeBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	logger.StartBackgroundWithWriter(ctx, buf)

	logger.InfoString("LIFE", "before cancel")
	cancel()
	logger.Close()

	if !strings.Contains(buf.String(), "before cancel") {
		t.Errorf("entry lost: %q", buf.String())
	}
}

// TestFlush_WithoutWorkerKeepsEntries verifies Flush no longer discards the
// queued entries when no worker is running.
func TestFlush_WithoutWorkerKeepsEntries(t *testing.T) {
	logger := NewLogger(Config{
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})
	for i := 0; i < 5; i++ {
		logger.InfoString("LIFE", "queued")
	}

	start := time.Now()
	logger.Flush()
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("Flush without a worker blocked for %v", elapsed)
	}
	if got := len(logger.logCh); got != 5 {
		t.Fatalf("%d entries queued after Flush, want 5", got)
	}

	buf := &safeBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger.StartBackgroundWithWriter(ctx, buf)
	logger.Flush()

	if got := strings.Count(buf.String(), "queued"); got != 5 {
		t.Errorf("got %d entries after starting the worker, want 5", got)
	}
	logger.Close()
}
