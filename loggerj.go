// Package loggerj provides a high-throughput, asynchronous logging facility for
// Go services. The documented logging APIs avoid heap allocations on the
// caller's goroutine, rate limiting uses a lock-free atomic state, and the
// package ships size-based log rotation and structured fields in both text
// and JSON formats.
//
// # Architecture: Pre-Compiled Execution Profiles
//
// Unlike traditional loggers that use mutexes and maps for rate limiting and
// sampling in the hot path, loggerj uses a "Pre-compiled Execution Profile"
// architecture.
//
//  1. Cold Path (Init-time): You register log types using RegisterSub(). This
//     pre-bakes JSON/Text prefixes into []byte and initializes lock-free atomic
//     counters for rate limiting and sampling. Profiles are stored in an
//     immutable copy-on-write registry accessed via atomic.Pointer, ensuring
//     zero interface boxing and zero map lookups in the hot path.
//  2. Hot Path (Log-time): The Log() method takes no loggerj-level mutex and
//     does not allocate on the heap. Profile lookup is a linear scan for up
//     to 8 profiles and a map lookup above that. It uses
//     atomic.CompareAndSwap (CAS) for rate limiting and atomic.Add for
//     sampling. The channel send that hands the entry to the worker still
//     takes the Go runtime's internal channel lock. String-to-byte conversion
//     uses unsafe zero-copy (Go 1.20+). Timestamps are taken by the worker
//     when it formats the entry, not when Log is called: this removes a clock
//     read from the hot path, but under backpressure ts reflects dequeue
//     time. The worker reads the clock once per batch of up to 128 queued
//     entries, so entries of one batch share the same ts.
//  3. Worker: A dedicated goroutine formats entries, injects pre-baked
//     []byte prefixes, and writes to the underlying io.Writer via bufio.
//     Flush() drains the channel before writing, guaranteeing no log loss
//     on explicit flush.
//
// This design keeps the caller's cost small and bounded: when the channel is
// full the entry is dropped (and counted in Stats) instead of blocking the
// caller. Throughput under heavy parallel load is limited by the shared
// channel and the single worker goroutine.
//
// # Quick Start
//
//	logger := loggerj.NewLogger(loggerj.Config{
//	    JSONOutput:   true,
//	    FlushTimeout: 50 * time.Millisecond,
//	})
//
//	// COLD PATH: Register profiles once at startup
//	logger.RegisterSub("HTTP",
//	    loggerj.WithRateLimit(1000, time.Second),
//	    loggerj.WithFields("env", "prod", "service", "gateway"),
//	)
//
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//	logger.StartBackground(ctx)
//	defer logger.Close() // drains queued entries, stops the worker, closes the file
//
//	// HOT PATH: Ultra-fast, zero-allocation logging
//	logger.InfoString("HTTP", "request received", "method", "GET", "path", "/api")
//	logger.ErrorString("DB", "connection failed", "host", "localhost", "err", "timeout")
//
//	// Context-aware logging (opt-in, zero cost when unused)
//	ctx = context.WithValue(ctx, loggerj.TraceIDKey, "abc-123")
//	logger.InfoCtx(ctx, "HTTP", "traced request", "method", "POST")
//
// # Performance
// See BENCH.md for detailed benchmark results and methodology.
package loggerj

import (
	"unsafe"
)

// -----------------------------------------------------------------------------
// Zero-Copy String→[]byte Conversion (Go 1.21+)
// -----------------------------------------------------------------------------

// unsafeStringToBytes converts a string to []byte without allocation.
//
// SAFETY: The returned slice MUST NOT be mutated and MUST NOT outlive
// the original string. In loggerj, this is safe because:
//  1. Go strings are immutable — the backing memory never changes.
//  2. The caller (log method) immediately copies via append(e.Msg[:0], msg...),
//     so the unsafe slice is only used within the same synchronous call.
//  3. The worker goroutine operates on the copied data, not the original.
func unsafeStringToBytes(s string) []byte {
	if len(s) == 0 {
		return nil
	}
	return unsafe.Slice(unsafe.StringData(s), len(s))
}
