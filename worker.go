package loggerj

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// -----------------------------------------------------------------------------
// Worker & Lifecycle
// -----------------------------------------------------------------------------

// defaultWriter returns the writer Start uses: the log file when OutputFile
// is configured, stderr otherwise.
func (l *Logger) defaultWriter() io.Writer {
	if l.cfg.OutputFile != "" && l.currentWriter != nil {
		return l.currentWriter
	}
	return os.Stderr
}

// Start runs the worker goroutine that processes log entries. It blocks until
// ctx is cancelled or Close is called, so it is normally started with
// `go logger.Start(ctx)`. It writes to the configured OutputFile or stderr.
//
// `go` returns before the worker goroutine has run, so a Flush() issued right
// afterwards can still find no running worker. Prefer StartBackground, which
// returns only once the worker is running.
func (l *Logger) Start(ctx context.Context) {
	l.StartWithWriter(ctx, l.defaultWriter())
}

// StartBackground starts the worker in its own goroutine and returns once the
// worker is running, so Flush() can be called right away. It writes to the
// configured OutputFile or stderr. Close stops the worker, so this is enough:
//
//	logger.StartBackground(context.Background())
//	defer logger.Close()
//
// Cancelling ctx also stops the worker. In SyncMode it does nothing.
func (l *Logger) StartBackground(ctx context.Context) {
	l.StartBackgroundWithWriter(ctx, l.defaultWriter())
}

// StartBackgroundWithWriter is StartBackground with a custom io.Writer.
func (l *Logger) StartBackgroundWithWriter(ctx context.Context, w io.Writer) {
	ready := make(chan struct{})
	go l.runWorker(ctx, w, ready)
	<-ready
}

// StartWithWriter runs the worker goroutine with a custom io.Writer. Like
// Start it blocks until ctx is cancelled or Close is called.
//
// Lifecycle synchronization:
//   - The started channel is closed (via sync.Once) when the worker begins,
//     allowing callers to wait for readiness without polling or sleeping.
//   - The workerDone channel is closed when the worker exits (after draining
//     remaining entries on context cancellation).
//
// The Flush() method drains all pending channel entries before writing,
// guaranteeing no log loss on explicit flush.
func (l *Logger) StartWithWriter(ctx context.Context, w io.Writer) {
	l.runWorker(ctx, w, nil)
}

// runWorker is the worker. If ready is not nil it is closed as soon as the
// worker is running, or when there is nothing to start (sync mode, a worker
// that is already running).
func (l *Logger) runWorker(ctx context.Context, w io.Writer, ready chan<- struct{}) {
	var readyOnce sync.Once
	signalReady := func() {
		if ready != nil {
			readyOnce.Do(func() { close(ready) })
		}
	}
	defer signalReady() // covers the early returns

	// Sync mode: no worker needed. Close started/workerDone immediately
	// and return. This allows code that unconditionally calls Start()
	// to work correctly in both async and sync modes.
	if l.cfg.SyncMode {
		l.startOnce.Do(func() { close(l.started) })
		l.closeOnce.Do(func() { close(l.workerDone) })
		return
	}

	// Prevent double-start panics and concurrent starts.
	// If the worker is already running, this call becomes a safe no-op.
	// The worker gets its own cancel so that Close can stop it.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopped := make(chan struct{})

	l.workerMu.Lock()
	if !l.running.CompareAndSwap(false, true) {
		l.workerMu.Unlock()
		return
	}
	l.workerCancel = cancel
	l.workerStopped = stopped
	l.workerMu.Unlock()

	l.globalWriterMu.Lock()
	l.globalWriter = w
	l.globalWriterMu.Unlock()

	// Signal that the worker has started (first call only, thread-safe)
	l.startOnce.Do(func() { close(l.started) })

	// Signal that the worker has exited when this function returns.
	// closeOnce ensures that if Start() is called again after the worker
	// exits, we don't panic on "close of closed channel".
	defer func() {
		l.running.Store(false)
		l.closeOnce.Do(func() { close(l.workerDone) })
		close(stopped)
	}()

	// From here on Flush() sees a running worker.
	signalReady()

	bw, ok := w.(*bufio.Writer)
	if !ok {
		bw = bufio.NewWriterSize(w, l.cfg.WriterBufferSize)
	}

	ticker := time.NewTicker(l.cfg.FlushTimeout)
	defer ticker.Stop()

	buf := make([]byte, 0, l.cfg.WorkerBufferSize)
	flushThreshold := l.cfg.FlushThreshold

workerLoop:
	for {
		select {

		// ── Graceful shutdown: drain remaining entries and flush ──
		case <-ctx.Done():
			l.drainAndFlush(bw, buf)
			return

		// ── Normal log processing ──
		case e := <-l.logCh:
			buf = l.formatBatch(buf, e)
			if len(buf) >= flushThreshold {
				l.writeOut(bw, buf)
				buf = buf[:0]
				// rotateIfNeeded returns the writer to use from now on:
				// after a rotation the old bw points to a closed file.
				bw = l.rotateIfNeeded(bw)
			}

		// ── Periodic flush ──
		case <-ticker.C:
			if len(buf) > 0 {
				l.writeOut(bw, buf)
				buf = buf[:0]
				// Check rotation on the ticker as well (safety net).
				bw = l.rotateIfNeeded(bw)
			}

		// ── Explicit Flush() call ──
		// Drain ALL pending channel entries first, then write the buffer.
		// This guarantees that Flush() never loses in-flight log entries.
		case done := <-l.flushCh:
			for {
				select {
				case e := <-l.logCh:
					buf = l.formatBatch(buf, e)
					if len(buf) >= flushThreshold {
						l.writeOut(bw, buf)
						buf = buf[:0]
					}
				default:
					// Channel empty — write and flush the buffer
					if len(buf) > 0 {
						l.writeOut(bw, buf)
						buf = buf[:0]
					}
					close(done)
					continue workerLoop
				}
			}
		}
	}
}

// writeOut writes the buffer to the underlying writer, flushes it, and
// updates currentSize. Centralizes all I/O paths (threshold, ticker,
// explicit-flush, drain) to ensure currentSize is always accurate.
// Returns the number of bytes written.
func (l *Logger) writeOut(bw *bufio.Writer, buf []byte) int {
	if len(buf) == 0 {
		return 0
	}
	written := len(buf)
	if _, err := bw.Write(buf); err != nil {
		fmt.Fprintf(os.Stderr, "loggerj: write error: %v\n", err)
	}
	if err := bw.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "loggerj: flush error: %v\n", err)
	}
	if l.cfg.OutputFile != "" {
		l.currentSize += int64(written)
	}
	return written
}

// -----------------------------------------------------------------------------
// Flush & Drain
// -----------------------------------------------------------------------------

// drainAndFlush drains all remaining entries from the channel, formats them,
// and writes the buffer. Called during graceful shutdown (ctx.Done()).
func (l *Logger) drainAndFlush(bw *bufio.Writer, buf []byte) {
	for {
		select {
		case e := <-l.logCh:
			buf = l.formatBatch(buf, e)
			if len(buf) >= l.cfg.FlushThreshold {
				l.writeOut(bw, buf)
				buf = buf[:0]
			}
		default:
			if len(buf) > 0 {
				l.writeOut(bw, buf)
				// No need to refresh bw here; worker is exiting.
			}
			return
		}
	}
}

// Flush forces an immediate flush of all pending log entries. It signals
// the worker to drain the channel and write the buffer, then blocks until
// the worker confirms completion (or times out after 1 second).
//
// If no worker is running, Flush returns immediately and the pending entries
// stay queued: they are written when a worker starts, or counted in Drops()
// if the channel fills up first. (Before v1.5.0 they were discarded.)
// `go logger.Start(ctx)` returns before the worker goroutine has run, so a
// Flush() issued right afterwards finds no worker and does not wait; use
// StartBackground, which returns once the worker is running.
//
// In SyncMode, Flush flushes the shared buffer of the OSBuffered and
// FsyncEveryN tiers (and fsyncs for FsyncEveryN); it does nothing for Direct
// and FsyncEveryWrite.
func (l *Logger) Flush() {
	// Sync mode: flush the shared buffer of the buffered tiers (no-op for
	// Direct and FsyncEveryWrite).
	if l.cfg.SyncMode {
		l.syncFlush()
		return
	}

	l.flushMu.Lock()
	defer l.flushMu.Unlock()

	// No worker: do not wait for one (that would block for the full timeout)
	// and do not discard the queued entries.
	if !l.running.Load() {
		return
	}

	done := make(chan struct{})
	// flushCh has capacity 1. Since we hold flushMu, it is guaranteed
	// to be empty, so this send will never block.
	l.flushCh <- done

	// Use time.NewTimer instead of time.After to prevent timer leaks
	// when the worker responds quickly.
	timer := time.NewTimer(1 * time.Second)
	defer timer.Stop()

	select {
	case <-done:
	case <-timer.C:
	}
}

// closeWaitTimeout is how long Close waits for the worker to drain the
// channel and exit before it closes the file anyway.
const closeWaitTimeout = 5 * time.Second

// stopWorker asks the worker, if one was started, to stop and waits until it
// has drained the channel and exited. It returns at once when no worker was
// started or the worker has already exited.
func (l *Logger) stopWorker() {
	l.workerMu.Lock()
	cancel, stopped := l.workerCancel, l.workerStopped
	l.workerMu.Unlock()
	if cancel == nil {
		return
	}

	cancel()

	timer := time.NewTimer(closeWaitTimeout)
	defer timer.Stop()
	select {
	case <-stopped:
	case <-timer.C:
		fmt.Fprintf(os.Stderr, "loggerj: worker did not stop within %v; closing anyway\n", closeWaitTimeout)
	}
}

// Close stops the worker, if one is running, waits for it to drain the channel
// and exit, and then flushes and closes the log file. Cancelling the context
// given to Start first is fine. Close is safe to call more than once; later
// calls return nil. If the worker does not exit within a few seconds (for
// example because a custom writer blocks), Close stops waiting and closes the
// file anyway.
//
// In sync mode Close flushes the shared buffered writer (and fsyncs for
// FsyncEveryN) and closes the sync file.
func (l *Logger) Close() error {
	if !l.closed.CompareAndSwap(false, true) {
		return nil
	}
	if l.cfg.SyncMode {
		// Final flush for OSBuffered and FsyncEveryN (fsync for the latter),
		// so no buffered entry is left in process memory.
		l.syncFlush()
		if l.syncFile != nil && l.syncFile != os.Stderr {
			return l.syncFile.Close()
		}
		return nil
	}

	l.stopWorker()

	l.rotationMu.Lock()
	defer l.rotationMu.Unlock()
	if l.currentWriter != nil {
		l.currentWriter.Flush()
	}
	if l.currentFile != nil {
		return l.currentFile.Close()
	}
	return nil
}
