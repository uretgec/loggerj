package loggerj

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// -----------------------------------------------------------------------------
// Worker batch drain
// -----------------------------------------------------------------------------

// TestFormatBatch_StopsAtBatchMax verifies one batch never takes more than
// workerBatchMax entries, so a saturating producer cannot starve the worker's
// ctx, ticker and flush cases.
func TestFormatBatch_StopsAtBatchMax(t *testing.T) {
	logger := NewLogger(Config{
		ChannelSize:      1000,
		WorkerBufferSize: 1 << 20,
		FlushThreshold:   1 << 20,
	})
	// No worker is started: the entries stay queued.
	for i := 0; i < 300; i++ {
		logger.Log(LevelInfo, "B", []byte("m"))
	}

	first := <-logger.logCh
	buf := logger.formatBatch(nil, first)

	if got := strings.Count(string(buf), "\n"); got != workerBatchMax {
		t.Errorf("batch formatted %d entries, want %d", got, workerBatchMax)
	}
	if left := len(logger.logCh); left != 300-workerBatchMax {
		t.Errorf("%d entries left in the channel, want %d", left, 300-workerBatchMax)
	}
}

// TestFormatBatch_StopsAtFlushThreshold verifies the batch stops once the
// buffer reaches FlushThreshold, so the buffer does not grow by a whole batch
// of large entries before it is written out.
func TestFormatBatch_StopsAtFlushThreshold(t *testing.T) {
	logger := NewLogger(Config{
		ChannelSize:      1000,
		WorkerBufferSize: 256,
		FlushThreshold:   256,
	})
	for i := 0; i < 50; i++ {
		logger.Log(LevelInfo, "B", []byte("message"))
	}

	first := <-logger.logCh
	buf := logger.formatBatch(nil, first)

	if len(buf) < 256 {
		t.Errorf("batch stopped at %d bytes, before the threshold", len(buf))
	}
	if len(buf) > 256+128 {
		t.Errorf("batch overshot the threshold by too much: %d bytes", len(buf))
	}
	if len(logger.logCh) == 0 {
		t.Error("expected entries to be left in the channel")
	}
}

// TestWorker_BatchDrainKeepsOrderAndTimestamps verifies that entries queued
// before the worker starts are delivered once, in order, with non-decreasing
// timestamps.
func TestWorker_BatchDrainKeepsOrderAndTimestamps(t *testing.T) {
	logger := NewLogger(Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  1000,
	})

	const n = 500
	for i := 0; i < n; i++ {
		logger.InfoString("ORD", fmt.Sprintf("m-%d", i))
	}

	buf := &safeBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.StartWithWriter(ctx, buf)
	<-logger.started
	logger.Flush()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != n {
		t.Fatalf("got %d lines, want %d (drops=%d)", len(lines), n, logger.Drops())
	}

	var prev float64
	for i, line := range lines {
		var rec struct {
			TS  float64 `json:"ts"`
			Msg string  `json:"msg"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %d: invalid JSON: %v", i, err)
		}
		if want := fmt.Sprintf("m-%d", i); rec.Msg != want {
			t.Fatalf("line %d: msg = %q, want %q", i, rec.Msg, want)
		}
		if rec.TS < prev {
			t.Fatalf("line %d: ts %v went backwards (previous %v)", i, rec.TS, prev)
		}
		prev = rec.TS
	}

	cancel()
	<-logger.workerDone
	logger.Close()
}
