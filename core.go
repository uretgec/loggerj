package loggerj

import (
	"fmt"
	"os"
	"runtime"
	"time"
)

// -----------------------------------------------------------------------------
// Core Logging (Internal)
// -----------------------------------------------------------------------------

// checkGatesAfterLevel runs the lock-free rate-limit and sampling checks AFTER
// the level check has already been performed by the caller (the level check
// is inlined into log()/logTyped() for the ~2ns filtered fast-path).
//
// Order of operations (v1.5.0+; documented in WithRateLimit/WithSampleRate):
//  1. Sampling: one atomic add; keeps 1 out of N entries.
//  2. Rate limit (admission control): rejects sampled entries over the
//     quota. It costs one monotonic clock read, paid only by entries that
//     survived sampling.
//
// The rate limit therefore caps the output. Before v1.5.0 the rate limiter
// ran first, which made the limit count entries before sampling.
func (l *Logger) checkGatesAfterLevel(logType string) (*SubProfile, bool) {
	// 1. Lock-free profile lookup (atomic.Pointer + linear scan / map)
	p := l.getProfile(logType)

	// 2. Lock-free sampling (atomic.Add)
	// Runs first: one atomic add, and no clock read for discarded entries.
	if p.sampleRate > 0 {
		if p.sampleCount.Add(1)%p.sampleRate != 0 {
			return nil, false
		}
	}

	// 3. Lock-free rate limiting (atomic CAS)
	// Applies to the sampled entries, so the limit caps the output. Under
	// saturation the CAS is skipped, but the clock read performed by
	// checkAtomicRateLimit is still paid by every sampled entry.
	if p.rlLimit > 0 {
		if !l.checkAtomicRateLimit(p) {
			return nil, false
		}
	}

	return p, true
}

// captureCaller resolves file:line via runtime.Caller. Marked noinline
// so the frame count is stable regardless of compiler inlining decisions;
// skip is incremented by 1 to account for this function's own frame.
//
//go:noinline
func (l *Logger) captureCaller(skip int) (file string, line int) {
	if !l.cfg.IncludeCaller {
		return "", 0
	}
	_, file, line, _ = runtime.Caller(skip + 1)
	for i := len(file) - 1; i > 0; i-- {
		if file[i] == '/' {
			return file[i+1:], line
		}
	}
	return file, line
}

// callerFromPC resolves file:line from a program counter recorded by the
// caller (for example slog.Record.PC). It returns empty values when
// IncludeCaller is disabled.
func (l *Logger) callerFromPC(pc uintptr) (file string, line int) {
	if !l.cfg.IncludeCaller || pc == 0 {
		return "", 0
	}
	frame, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	file = frame.File
	for i := len(file) - 1; i > 0; i-- {
		if file[i] == '/' {
			return file[i+1:], frame.Line
		}
	}
	return file, frame.Line
}

// dispatch sends a populated Entry to the worker via the non-blocking
// channel send, or drops it (incrementing the drop counter and invoking
// onDrop) if the channel is full. Shared by both the string-field and
// typed-field hot paths so there is exactly one drop-handling code path.
func (l *Logger) dispatch(e *Entry) {
	select {
	case l.logCh <- e:
		return // the worker owns e from here on
	default:
	}

	// Channel full. ERROR entries may wait a bounded time for space.
	if d := l.cfg.ErrorBlockTimeout; d > 0 && e.Level >= LevelError {
		timer := time.NewTimer(d)
		select {
		case l.logCh <- e:
			timer.Stop()
			return
		case <-timer.C:
		}
	}

	// Drop the entry and count it.
	l.drops.Add(1)
	if int(e.Level) < len(l.dropsByLevel) {
		l.dropsByLevel[e.Level].Add(1)
	}
	if fn := l.onDrop.Load(); fn != nil {
		(*fn)(l.drops.Load())
	}
	e.Reset()
	l.pool.Put(e)
}

// log is the internal core logging method for the legacy ...string field
// API, with configurable caller skip. This is a hot path: zero
// allocations, zero mutex locks, zero map lookups — provided the caller
// passes string literals. Dynamic values still require the caller to
// format them first (fmt.Sprintf, strconv), which allocates; logTyped
// exists to eliminate exactly that cost.
func (l *Logger) log(level Level, logType string, msg []byte, skip int, fields ...string) {
	// Fast-path: level filter (~2ns, zero alloc) — INLINED for hot path.
	// This is the ~2ns filtered path that must stay alloc-free and inline.
	if level < Level(l.currentLevel.Load()) {
		return
	}

	p, ok := l.checkGatesAfterLevel(logType)
	if !ok {
		return
	}

	// Sync mode: bypass channel/worker, write directly.
	if l.cfg.SyncMode {
		file, line := l.captureCaller(skip)
		l.syncWrite(level, logType, msg, p, nil, fields, file, line)
		return
	}

	// Caller info must be captured with the *original* skip value the
	// public method passed in — see captureCaller's doc comment.
	file, line := l.captureCaller(skip)

	e := l.pool.Get().(*Entry)
	e.Level = level
	e.Type = logType
	e.Msg = append(e.Msg[:0], msg...)
	e.File = file
	e.Line = line
	e.Fields = append(e.Fields[:0], fields...)
	e.Profile = p

	l.dispatch(e)
}

// logTyped is the internal core logging method for the typed Field API.
// If pc is non-zero (slog.Record.PC) it is used to resolve the caller and
// skip is ignored; otherwise the caller is resolved with skip.
// Identical gating and dispatch to log(), but populates Entry.FieldsV
// instead of Entry.Fields — Field values are copied by value (no
// interface{} boxing), so this path is zero-allocation even when the
// caller logs integers, booleans, floats, durations, or errors.
func (l *Logger) logTyped(level Level, logType string, msg []byte, skip int, pc uintptr, fields ...Field) {
	// Fast-path: level filter (~2ns, zero alloc) — INLINED for hot path.
	if level < Level(l.currentLevel.Load()) {
		return
	}

	p, ok := l.checkGatesAfterLevel(logType)
	if !ok {
		return
	}

	// Resolve the caller once. A non-zero pc (slog.Record.PC) is exact and
	// independent of call depth; otherwise fall back to the skip count.
	var file string
	var line int
	if pc != 0 {
		file, line = l.callerFromPC(pc)
	} else {
		file, line = l.captureCaller(skip)
	}

	// Sync mode: bypass channel/worker, write directly.
	if l.cfg.SyncMode {
		l.syncWrite(level, logType, msg, p, fields, nil, file, line)
		return
	}

	e := l.pool.Get().(*Entry)
	e.Level = level
	e.Type = logType
	e.Msg = append(e.Msg[:0], msg...)
	e.File = file
	e.Line = line
	e.FieldsV = append(e.FieldsV[:0], fields...)
	e.Profile = p

	l.dispatch(e)
}

// syncWrite formats and writes a log entry synchronously using a pooled
// buffer. The write strategy depends on Config.DurabilityTier:
//
//   - OSBuffered: buffer copy to bufio.Writer; flushed when the buffer is
//     nearly full or when a later call sees >=10ms since the last flush
//     (no background timer). Throughput: ~111ns/op single-thread,
//     ~238ns/op parallel on the reference machine (no syscall per entry).
//   - Direct: single write() syscall per entry. Throughput: ~1566ns/op.
//   - FsyncEveryN: bufio buffer, flush + fsync() every N entries.
//   - FsyncEveryWrite: write() + fsync() on every entry (~4.4ms/op on the
//     reference machine).
//
// Locking per tier:
//   - Direct / FsyncEveryWrite: no loggerj mutex. os.File.Write still
//     serializes concurrent writers on the same file internally.
//   - OSBuffered / FsyncEveryN: syncMu is held during BOTH the buffer copy
//     AND the flush/fsync syscall because bufio.Writer is not thread-safe.
//     This is the root cause of the ~2x parallel slowdown (111ns -> 238ns).
//     A per-goroutine writer pool would avoid it but would lose buffered
//     data on Close().
func (l *Logger) syncWrite(level Level, logType string, msg []byte, p *SubProfile, fields []Field, stringFields []string, file string, line int) {
	bp := l.syncBufPool.Get().(*[]byte)
	buf := (*bp)[:0]

	var e Entry
	e.Level = level
	e.Type = logType
	e.Msg = msg
	e.File = file
	e.Line = line
	e.Profile = p
	if len(fields) > 0 {
		e.FieldsV = fields
	} else {
		e.Fields = stringFields
	}

	// formatEntry handles both JSON and Text, and captures the timestamp.
	buf = l.formatEntry(buf, &e)

	switch l.cfg.DurabilityTier {
	case OSBuffered:
		l.syncMu.Lock()
		if l.syncBw != nil {
			if _, err := l.syncBw.Write(buf); err != nil {
				l.noteSyncError("write", err)
			}
			// Flush trigger 1: buffer nearly full
			// Flush trigger 2: 10ms elapsed since last flush
			shouldFlush := l.syncBw.Available() < len(buf)
			if !shouldFlush {
				nowMs := time.Now().UnixMilli()
				if nowMs-l.syncLastFlushMs.Load() >= 10 {
					shouldFlush = true
				}
			}
			if shouldFlush {
				if err := l.syncBw.Flush(); err != nil {
					l.noteSyncError("flush", err)
				}
				l.syncLastFlushMs.Store(time.Now().UnixMilli())
			}
		} else {
			l.syncFileWrite(buf)
		}
		l.syncMu.Unlock()

	case Direct:
		l.syncFileWrite(buf)

	case FsyncEveryN:
		l.syncMu.Lock()
		if l.syncBw != nil {
			if _, err := l.syncBw.Write(buf); err != nil {
				l.noteSyncError("write", err)
			}
		} else {
			l.syncFileWrite(buf)
		}
		count := l.syncWriteCount.Add(1)
		if count >= int64(l.cfg.FsyncEveryNCount) {
			if l.syncBw != nil {
				if err := l.syncBw.Flush(); err != nil {
					l.noteSyncError("flush", err)
				}
			}
			l.syncFileSync()
			l.syncWriteCount.Store(0)
		}
		l.syncMu.Unlock()

	case FsyncEveryWrite:
		l.syncFileWrite(buf)
		l.syncFileSync()
	}

	if cap(buf) > 4096 {
		*bp = make([]byte, 0, 4096)
	} else {
		*bp = buf
	}
	l.syncBufPool.Put(bp)
}

// syncFileWrite wraps l.syncFile.Write with error counting. Every failed
// write is counted and reported to stderr so audit users can detect log
// loss. Errors are not returned because syncWrite has no error channel;
// the counter is the observable signal.
func (l *Logger) syncFileWrite(buf []byte) {
	if _, err := l.syncFile.Write(buf); err != nil {
		l.noteSyncError("write", err)
	}
}

// syncFileSync wraps l.syncFile.Sync with error reporting. fsync failures
// are rare but indicate serious durability problems.
func (l *Logger) syncFileSync() {
	if err := l.syncFile.Sync(); err != nil {
		l.noteSyncError("fsync", err)
	}
}

// noteSyncError counts a failed sync-mode I/O operation and reports it to
// stderr. op is one of "write", "flush" or "fsync". The counter is exposed
// as Stats().SyncWriteErrors.
func (l *Logger) noteSyncError(op string, err error) {
	l.syncWriteErrors.Add(1)
	fmt.Fprintf(os.Stderr, "loggerj: sync %s error (tier=%s): %v\n",
		op, l.cfg.DurabilityTier, err)
}

// syncFlush flushes the shared bufio.Writer used by the OSBuffered and
// FsyncEveryN tiers and, for FsyncEveryN, fsyncs the file. It is a no-op for
// Direct and FsyncEveryWrite, which have no user-space buffer.
func (l *Logger) syncFlush() {
	if l.syncBw == nil {
		return
	}
	l.syncMu.Lock()
	defer l.syncMu.Unlock()
	if err := l.syncBw.Flush(); err != nil {
		l.noteSyncError("flush", err)
	}
	if l.cfg.DurabilityTier == FsyncEveryN && l.syncFile != nil && l.syncFile != os.Stderr {
		l.syncFileSync()
		l.syncWriteCount.Store(0)
	}
}
