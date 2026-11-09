package loggerj

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// -----------------------------------------------------------------------------
// Fault injection: blocked and failing writers
// -----------------------------------------------------------------------------

// blockingWriter blocks every Write until release is closed.
type blockingWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}

// TestFlush_DoesNotHangOnBlockedWriter: a worker stuck in its writer must not
// make Flush hang. Before v1.5.2 the first Flush timed out and left its
// request in flushCh, and the second Flush blocked on the send forever.
func TestFlush_DoesNotHangOnBlockedWriter(t *testing.T) {
	logger := NewLogger(Config{FlushTimeout: 10 * time.Millisecond, ChannelSize: 100})
	w := newBlockingWriter()
	logger.StartBackgroundWithWriter(context.Background(), w)

	logger.InfoString("BLOCK", "stuck in the writer")
	select {
	case <-w.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the worker never reached the writer")
	}

	done := make(chan struct{})
	go func() {
		logger.Flush()
		logger.Flush()
		logger.Flush()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Flush hangs while the worker is blocked in its writer")
	}

	close(w.release)
	logger.Close()
}

// flakyWriter fails every Write while fail is set.
type flakyWriter struct {
	mu   sync.Mutex
	fail bool
	buf  strings.Builder
}

func (w *flakyWriter) setFail(v bool) {
	w.mu.Lock()
	w.fail = v
	w.mu.Unlock()
}

func (w *flakyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fail {
		return 0, errors.New("no space left on device")
	}
	return w.buf.Write(p)
}

func (w *flakyWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestWriteErrors_CountedAndWriterRecovers: a failed write is counted, and the
// worker writes again once the writer works. A bufio.Writer refuses all writes
// after its first error, so before v1.5.2 one transient error stopped the
// output for good.
func TestWriteErrors_CountedAndWriterRecovers(t *testing.T) {
	logger := NewLogger(Config{FlushTimeout: 10 * time.Millisecond, ChannelSize: 100})
	w := &flakyWriter{fail: true}
	logger.StartBackgroundWithWriter(context.Background(), w)

	logger.InfoString("FLAKY", "lost while the writer fails")
	logger.Flush()
	if got := logger.Stats().WriteErrors; got == 0 {
		t.Fatal("WriteErrors = 0, want the failed write to be counted")
	}

	w.setFail(false)
	logger.InfoString("FLAKY", "written after recovery")
	logger.Flush()
	logger.Close()

	if out := w.String(); !strings.Contains(out, "written after recovery") {
		t.Errorf("the worker did not recover from the write error, output: %q", out)
	}
}

func TestShouldWarn_OncePerInterval(t *testing.T) {
	var last atomic.Int64
	if !shouldWarn(&last) {
		t.Error("the first message must be allowed")
	}
	if shouldWarn(&last) {
		t.Error("a second message right after the first must be suppressed")
	}
	last.Store(monoMillis() - warnInterval.Milliseconds() - 1)
	if !shouldWarn(&last) {
		t.Error("a message after the interval must be allowed")
	}
}

// -----------------------------------------------------------------------------
// Concurrent load (meant to be run with -race)
// -----------------------------------------------------------------------------

// TestConcurrentLoad logs from many goroutines through all APIs while Flush,
// level changes and Stats run concurrently. Every attempt must end up written
// or counted as a drop.
func TestConcurrentLoad(t *testing.T) {
	logger := NewLogger(Config{
		JSONOutput:   true,
		FlushTimeout: 5 * time.Millisecond,
		ChannelSize:  1024,
	})
	buf := &safeBuffer{}
	logger.StartBackgroundWithWriter(context.Background(), buf)
	sub := logger.Sub("LOAD", WithFields("env", "test"))

	const goroutines, perGoroutine = 16, 2000

	stop := make(chan struct{})
	bg := make(chan struct{})
	go func() {
		defer close(bg)
		for {
			select {
			case <-stop:
				return
			default:
			}
			logger.Flush()
			logger.SetLevelValue(LevelDebug)
			logger.SetLevelValue(LevelInfo)
			_ = logger.Stats()
			time.Sleep(time.Millisecond)
		}
	}()

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				switch i % 3 {
				case 0:
					logger.InfoString("LOAD", "plain", "k", "v")
				case 1:
					logger.InfoFields("LOAD", []byte("typed"), Int("i", i))
				default:
					sub.InfoString("handle")
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	<-bg
	logger.Close()

	written := uint64(strings.Count(buf.String(), "\n"))
	total := uint64(goroutines * perGoroutine)
	if written+logger.Drops() != total {
		t.Errorf("written %d + dropped %d != attempted %d", written, logger.Drops(), total)
	}
}
