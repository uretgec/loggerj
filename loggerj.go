// Package loggerj provides an ultra-high-performance, asynchronous, and lock-free
// logging facility designed for high-throughput Go services. It offers zero heap
// allocations in the hot path, atomic rate limiting, log rotation, and structured
// fields in both text and JSON formats.
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
//  2. Hot Path (Log-time): The Log() method performs ZERO map lookups, ZERO
//     mutex locks, and ZERO heap allocations. It uses atomic.CompareAndSwap
//     (CAS) for rate limiting and atomic.Add for sampling. String-to-byte
//     conversion uses unsafe zero-copy (Go 1.21+). Timestamps are deferred
//     to the worker goroutine, removing a vDSO syscall from the hot path.
//  3. Worker: A dedicated goroutine formats entries, injects pre-baked
//     []byte prefixes, and writes to the underlying io.Writer via bufio.
//     Flush() drains the channel before writing, guaranteeing no log loss
//     on explicit flush.
//
// This design ensures that logging never blocks the caller, eliminates GC
// pressure in the hot path, and scales linearly with CPU cores without
// lock contention.
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
//	go logger.Start(ctx)
//	defer logger.Close()
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
//
// Benchmarks on Apple M1 Pro (10 cores), Go 1.21+:
//
//	Filtered:    ~2.0 ns/op   (484M logs/s)   0 allocs/op
//	RateLimited: ~44 ns/op    (23M logs/s)    0 allocs/op
//	Parallel:    ~86 ns/op    (11.6M logs/s)  0 allocs/op
//	JSON:        ~65 ns/op    (15.3M logs/s)  0 allocs/op
//	StringAPI:   ~66 ns/op    (15.1M logs/s)  0 allocs/op
//	WithCaller:  ~461 ns/op   (2.2M logs/s)   2 allocs/op
//
// See BENCH.md for detailed benchmark results and methodology.
package loggerj

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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
//
//go:nosplit
func unsafeStringToBytes(s string) []byte {
	if len(s) == 0 {
		return nil
	}
	return unsafe.Slice(unsafe.StringData(s), len(s))
}

// -----------------------------------------------------------------------------
// Level
// -----------------------------------------------------------------------------

// Level represents the severity of a log entry. Lower values indicate more
// verbose logging. The logger filters entries below the configured threshold.
type Level uint8

const (
	// LevelDebug is the most verbose level, used for detailed debugging information.
	LevelDebug Level = 0
	// LevelInfo is the default level, used for general operational messages.
	LevelInfo Level = 1
	// LevelWarn indicates potential issues that should be monitored.
	LevelWarn Level = 2
	// LevelError indicates serious problems that require immediate attention.
	LevelError Level = 3
)

// levelNames is a lookup table for level names. Using an array instead of
// switch-case provides O(1) lookup with better branch prediction.
var levelNames = [4]string{"DEBUG", "INFO", "WARN", "ERROR"}

// String returns the human-readable name of the log level.
// Unknown levels return "UNKNOWN" without allocation.
func (l Level) String() string {
	if l < Level(len(levelNames)) {
		return levelNames[l]
	}
	return "UNKNOWN"
}

// -----------------------------------------------------------------------------
// Config
// -----------------------------------------------------------------------------

// Config holds the configuration for a Logger instance. All fields have sensible
// defaults and can be left at their zero values.
type Config struct {
	// JSONOutput controls the output format. If true, logs are formatted as JSON.
	// If false, logs are formatted as human-readable text. Default: false
	JSONOutput bool

	// FlushTimeout is the interval at which the worker flushes buffered logs.
	// Shorter timeouts reduce latency but increase I/O operations. Default: 50ms
	FlushTimeout time.Duration

	// ChannelSize is the capacity of the internal log channel. Larger values
	// provide more buffering for burst traffic. If full, entries are dropped.
	// Default: 4096
	ChannelSize int

	// WorkerBufferSize is the initial capacity of the worker's format buffer.
	// Minimum: 256. Default: 4096
	WorkerBufferSize int

	// FlushThreshold is the byte count at which the worker flushes the
	// format buffer to the underlying writer. Should be <= WorkerBufferSize.
	// Minimum: 256. Default: 4096
	FlushThreshold int

	// WriterBufferSize is the size of the bufio.Writer buffer used for I/O.
	// Minimum: 512. Default: 8192
	WriterBufferSize int

	// RateLimitWindow is the default time window for rate limiting, in seconds.
	// Used if a SubProfile doesn't specify its own window via WithRateLimit.
	// Minimum: 1. Default: 1
	RateLimitWindow int64

	// IncludeCaller adds file:line information to each log entry.
	// WARNING: Adds ~460ns overhead and 2 allocations per log entry.
	// Should be disabled in production for maximum performance. Default: false
	IncludeCaller bool

	// OutputFile is the path to the log file. If empty, logs are written to stderr.
	OutputFile string

	// MaxFileSize is the maximum size of the log file before rotation.
	// If 0, rotation is disabled. Default: 0
	MaxFileSize int64

	// MaxBackupFiles is the maximum number of rotated log files to keep.
	// Only effective if MaxFileSize > 0. Default: 0
	MaxBackupFiles int
}

// DefaultConfig returns a Config with sensible defaults for production use.
func DefaultConfig() Config {
	return Config{
		JSONOutput:       false,
		FlushTimeout:     50 * time.Millisecond,
		ChannelSize:      4096,
		WorkerBufferSize: 4096,
		FlushThreshold:   4096,
		WriterBufferSize: 8192,
		RateLimitWindow:  1,
		IncludeCaller:    false,
		OutputFile:       "",
		MaxFileSize:      0,
		MaxBackupFiles:   0,
	}
}

// -----------------------------------------------------------------------------
// SubProfile (Pre-Compiled Execution Profile)
// -----------------------------------------------------------------------------

// SubProfile represents a pre-compiled execution profile for a specific logType.
// Unlike traditional loggers that use mutexes and maps in the hot path,
// SubProfile holds lock-free atomic counters for rate limiting/sampling and
// pre-baked []byte prefixes for zero-CPU formatting.
type SubProfile struct {
	Name string

	// Pre-baked prefixes for zero-CPU formatting in the worker.
	textPrefix []byte // e.g., "module=HTTP env=prod "
	jsonPrefix []byte // e.g., ,"module":"HTTP","env":"prod"

	// Lock-Free Rate Limiting (CAS)
	rlLimit   int64        // Max logs per window (0 = unlimited)
	rlWindow  int64        // Window size in seconds
	rlCount   atomic.Int64 // Current count in window
	rlResetAt atomic.Int64 // Unix timestamp when window resets

	// Lock-Free Sampling
	sampleRate  int64        // Log 1 out of N (0 = no sampling)
	sampleCount atomic.Int64 // Atomic counter

	// tempFields is used only during initialization to hold raw fields
	// before they are baked into textPrefix/jsonPrefix. It is set to nil
	// after registration to allow GC to reclaim the memory.
	tempFields []string
}

// SubOption configures a SubProfile during RegisterSub.
type SubOption func(*SubProfile)

// WithRateLimit sets a lock-free rate limit for this specific logType.
// limit is the max logs per window. window is the duration (e.g., time.Second).
func WithRateLimit(limit int64, window time.Duration) SubOption {
	return func(p *SubProfile) {
		p.rlLimit = limit
		p.rlWindow = int64(window.Seconds())
	}
}

// WithSampleRate sets a lock-free sampling rate for this logType.
// rate means 1 out of `rate` logs will be written (0 disables sampling).
func WithSampleRate(rate int64) SubOption {
	return func(p *SubProfile) {
		p.sampleRate = rate
	}
}

// WithFields adds static key-value pairs that will be pre-baked into the
// JSON/Text prefixes. This avoids formatting these fields in the hot path.
func WithFields(fields ...string) SubOption {
	return func(p *SubProfile) {
		p.tempFields = fields
	}
}

// -----------------------------------------------------------------------------
// Entry
// -----------------------------------------------------------------------------

// Entry represents a single log record. Entries are pooled using sync.Pool
// to minimize allocations. The Reset method clears all fields for reuse.
//
// Timestamps are not stored in the Entry; they are captured by the worker
// goroutine at format time, removing a vDSO syscall from the hot path.
type Entry struct {
	Level   Level
	Type    string
	Msg     []byte
	File    string
	Line    int
	Fields  []string
	Profile *SubProfile
}

// Reset clears all fields of the Entry for reuse. Large slices (Msg > 4096
// bytes, Fields > 64 elements) are released to the garbage collector to
// prevent permanent memory retention in the pool.
func (e *Entry) Reset() {
	e.Level = 0
	e.Type = ""
	e.File = ""
	e.Line = 0
	e.Profile = nil

	if cap(e.Msg) > 4096 {
		e.Msg = nil
	} else {
		e.Msg = e.Msg[:0]
	}

	if cap(e.Fields) > 64 {
		e.Fields = nil
	} else {
		e.Fields = e.Fields[:0]
	}
}

// -----------------------------------------------------------------------------
// Profile Registry (Copy-on-Write, Lock-Free Reads)
// -----------------------------------------------------------------------------

// profileRegistry holds an immutable snapshot of all registered profiles.
// Hot-path reads are lock-free via atomic.Pointer. Cold-path writes
// (RegisterSub) create a new copy and swap atomically.
type profileRegistry struct {
	names    []string
	profiles []*SubProfile
}

// -----------------------------------------------------------------------------
// Logger
// -----------------------------------------------------------------------------

// Logger is the main logging instance. It provides asynchronous, high-throughput,
// lock-free logging with zero heap allocations in the hot path.
type Logger struct {
	cfg          Config
	logCh        chan *Entry
	flushCh      chan chan struct{}
	drops        atomic.Uint64
	currentLevel atomic.Uint32

	// Copy-on-write profile registry: lock-free reads via atomic.Pointer,
	// mutex-protected writes via registerMu (cold path only).
	registry       atomic.Pointer[profileRegistry]
	defaultProfile *SubProfile
	registerMu     sync.Mutex

	// File rotation & I/O
	rotationMu     sync.Mutex
	currentFile    *os.File
	currentWriter  *bufio.Writer
	currentSize    int64
	outputFilePath string
	globalWriterMu sync.Mutex
	globalWriter   io.Writer

	pool sync.Pool

	// Deterministic lifecycle synchronization.
	// started is closed when the worker goroutine begins processing.
	// workerDone is closed when the worker goroutine exits.
	startOnce  sync.Once
	started    chan struct{}
	workerDone chan struct{}

	// onDrop is an optional callback invoked when logs are dropped
	// due to a full channel. Called from the hot path — keep it fast.
	onDrop func(dropped uint64)
}

// NewLogger creates a new Logger instance. The logger is not started;
// call Start or StartWithWriter to begin processing.
func NewLogger(c Config) *Logger {
	l := &Logger{
		cfg:        c,
		pool:       sync.Pool{New: func() any { return &Entry{} }},
		flushCh:    make(chan chan struct{}, 1),
		started:    make(chan struct{}),
		workerDone: make(chan struct{}),
	}

	if l.cfg.ChannelSize <= 0 {
		l.cfg.ChannelSize = 4096
	}
	if l.cfg.FlushTimeout <= 0 {
		l.cfg.FlushTimeout = 50 * time.Millisecond
	}
	if l.cfg.WorkerBufferSize < 256 {
		l.cfg.WorkerBufferSize = 4096
	}
	if l.cfg.FlushThreshold < 256 {
		l.cfg.FlushThreshold = 4096
	}
	if l.cfg.FlushThreshold > l.cfg.WorkerBufferSize {
		l.cfg.FlushThreshold = l.cfg.WorkerBufferSize
	}
	if l.cfg.WriterBufferSize < 512 {
		l.cfg.WriterBufferSize = 8192
	}
	if l.cfg.RateLimitWindow < 1 {
		l.cfg.RateLimitWindow = 1
	}

	l.logCh = make(chan *Entry, l.cfg.ChannelSize)
	l.currentLevel.Store(uint32(LevelInfo))
	l.defaultProfile = &SubProfile{Name: "DEFAULT"}

	if l.cfg.OutputFile != "" {
		l.outputFilePath = l.cfg.OutputFile
		if err := l.openLogFile(); err != nil {
			fmt.Fprintf(os.Stderr, "loggerj: failed to open log file: %v\n", err)
			l.cfg.OutputFile = ""
		}
	}
	return l
}

// RegisterSub registers a SubProfile for a specific logType.
//
// COLD PATH: Call this during application initialization, NOT inside
// HTTP handlers or hot loops. The registry uses a copy-on-write strategy:
// a new immutable snapshot is created and swapped atomically, so concurrent
// hot-path reads are never blocked.
func (l *Logger) RegisterSub(logType string, opts ...SubOption) {
	p := &SubProfile{Name: logType}
	for _, opt := range opts {
		opt(p)
	}

	// Pre-bake prefixes (zero CPU cost in hot path)
	if len(p.tempFields) > 0 {
		p.textPrefix = l.buildTextPrefix(p.tempFields)
		p.jsonPrefix = l.buildJSONPrefix(p.tempFields)
	}
	// Release raw fields immediately after baking
	p.tempFields = nil

	// Initialize rate limit window
	if p.rlLimit > 0 {
		if p.rlWindow == 0 {
			p.rlWindow = l.cfg.RateLimitWindow
		}
		p.rlResetAt.Store(time.Now().Unix() + p.rlWindow)
	}

	// Copy-on-write: clone the current registry, append the new profile,
	// and swap atomically. Hot-path readers see either the old or the new
	// snapshot — never a partially updated state.
	l.registerMu.Lock()
	defer l.registerMu.Unlock()

	old := l.registry.Load()
	var newReg *profileRegistry
	if old == nil {
		newReg = &profileRegistry{
			names:    []string{logType},
			profiles: []*SubProfile{p},
		}
	} else {
		n := len(old.names)
		newNames := make([]string, n+1)
		newProfiles := make([]*SubProfile, n+1)
		copy(newNames, old.names)
		copy(newProfiles, old.profiles)
		newNames[n] = logType
		newProfiles[n] = p
		newReg = &profileRegistry{
			names:    newNames,
			profiles: newProfiles,
		}
	}
	l.registry.Store(newReg)
}

// buildTextPrefix pre-formats static fields into a text prefix
// (e.g., "env=prod region=eu ") for zero-CPU injection in the worker.
func (l *Logger) buildTextPrefix(fields []string) []byte {
	var buf []byte
	for i := 0; i < len(fields); i += 2 {
		buf = append(buf, fields[i]...)
		buf = append(buf, '=')
		if i+1 < len(fields) {
			buf = append(buf, fields[i+1]...)
		}
		buf = append(buf, ' ')
	}
	return buf
}

// buildJSONPrefix pre-formats static fields into a JSON prefix
// (e.g., ,"env":"prod","region":"eu") for zero-CPU injection in the worker.
func (l *Logger) buildJSONPrefix(fields []string) []byte {
	var buf []byte
	for i := 0; i < len(fields); i += 2 {
		buf = append(buf, ',')
		buf = appendJSONString(buf, fields[i])
		buf = append(buf, ':')
		if i+1 < len(fields) {
			buf = appendJSONString(buf, fields[i+1])
		} else {
			buf = append(buf, `""`...)
		}
	}
	return buf
}

// getProfile performs a lock-free profile lookup on the hot path.
// It reads the immutable registry snapshot via atomic.Pointer and
// performs a linear scan (cache-friendly, branch-predictor friendly).
// Returns defaultProfile if no match is found.
//
//go:nosplit
func (l *Logger) getProfile(logType string) *SubProfile {
	reg := l.registry.Load()
	if reg == nil {
		return l.defaultProfile
	}
	for i := range reg.names {
		if reg.names[i] == logType {
			return reg.profiles[i]
		}
	}
	return l.defaultProfile
}

// -----------------------------------------------------------------------------
// Level Control
// -----------------------------------------------------------------------------

// SetLevelValue sets the current log level threshold atomically.
// Entries below this level are discarded in ~2ns with zero allocations.
func (l *Logger) SetLevelValue(level Level) {
	l.currentLevel.Store(uint32(level))
}

// GetLevel returns the current log level threshold.
func (l *Logger) GetLevel() Level {
	return Level(l.currentLevel.Load())
}

// -----------------------------------------------------------------------------
// Public API: []byte Methods
// -----------------------------------------------------------------------------

// Log is the public core logging method. Caller skip is 1.
func (l *Logger) Log(level Level, logType string, msg []byte, fields ...string) {
	l.log(level, logType, msg, 1, fields...)
}

// Debug logs a message at LevelDebug. Caller skip is 2.
func (l *Logger) Debug(logType string, msg []byte, fields ...string) {
	l.log(LevelDebug, logType, msg, 2, fields...)
}

// Info logs a message at LevelInfo. Caller skip is 2.
func (l *Logger) Info(logType string, msg []byte, fields ...string) {
	l.log(LevelInfo, logType, msg, 2, fields...)
}

// Warn logs a message at LevelWarn. Caller skip is 2.
func (l *Logger) Warn(logType string, msg []byte, fields ...string) {
	l.log(LevelWarn, logType, msg, 2, fields...)
}

// Error logs a message at LevelError. Caller skip is 2.
func (l *Logger) Error(logType string, msg []byte, fields ...string) {
	l.log(LevelError, logType, msg, 2, fields...)
}

// -----------------------------------------------------------------------------
// Public API: String Methods (Zero-Copy via unsafe)
// -----------------------------------------------------------------------------

// DebugString logs a string message at LevelDebug with zero-copy
// string-to-byte conversion. Caller skip is 2.
func (l *Logger) DebugString(logType string, msg string, fields ...string) {
	l.log(LevelDebug, logType, unsafeStringToBytes(msg), 2, fields...)
}

// InfoString logs a string message at LevelInfo with zero-copy
// string-to-byte conversion. Caller skip is 2.
func (l *Logger) InfoString(logType string, msg string, fields ...string) {
	l.log(LevelInfo, logType, unsafeStringToBytes(msg), 2, fields...)
}

// WarnString logs a string message at LevelWarn with zero-copy
// string-to-byte conversion. Caller skip is 2.
func (l *Logger) WarnString(logType string, msg string, fields ...string) {
	l.log(LevelWarn, logType, unsafeStringToBytes(msg), 2, fields...)
}

// ErrorString logs a string message at LevelError with zero-copy
// string-to-byte conversion. Caller skip is 2.
func (l *Logger) ErrorString(logType string, msg string, fields ...string) {
	l.log(LevelError, logType, unsafeStringToBytes(msg), 2, fields...)
}

// -----------------------------------------------------------------------------
// Public API: Context-Aware Methods (Opt-in)
// -----------------------------------------------------------------------------

// contextKey is an unexported type for context keys defined in this package,
// preventing collisions with keys from other packages.
type contextKey int

const (
	// TraceIDKey is the context key for distributed trace identifiers.
	TraceIDKey contextKey = iota
	// RequestIDKey is the context key for request identifiers.
	RequestIDKey
	// SpanIDKey is the context key for span identifiers.
	SpanIDKey
)

// InfoCtx logs a message at LevelInfo with optional context extraction.
// If ctx is nil or contains no known keys, it behaves exactly like
// InfoString with zero additional cost.
func (l *Logger) InfoCtx(ctx context.Context, logType string, msg string, fields ...string) {
	fields = appendCtxFields(ctx, fields)
	l.log(LevelInfo, logType, unsafeStringToBytes(msg), 2, fields...)
}

// DebugCtx logs a message at LevelDebug with optional context extraction.
// If ctx is nil or contains no known keys, it behaves exactly like
// DebugString with zero additional cost.
func (l *Logger) DebugCtx(ctx context.Context, logType string, msg string, fields ...string) {
	fields = appendCtxFields(ctx, fields)
	l.log(LevelDebug, logType, unsafeStringToBytes(msg), 2, fields...)
}

// WarnCtx logs a message at LevelWarn with optional context extraction.
// If ctx is nil or contains no known keys, it behaves exactly like
// WarnString with zero additional cost.
func (l *Logger) WarnCtx(ctx context.Context, logType string, msg string, fields ...string) {
	fields = appendCtxFields(ctx, fields)
	l.log(LevelWarn, logType, unsafeStringToBytes(msg), 2, fields...)
}

// ErrorCtx logs a message at LevelError with optional context extraction.
// If ctx is nil or contains no known keys, it behaves exactly like
// ErrorString with zero additional cost.
func (l *Logger) ErrorCtx(ctx context.Context, logType string, msg string, fields ...string) {
	fields = appendCtxFields(ctx, fields)
	l.log(LevelError, logType, unsafeStringToBytes(msg), 2, fields...)
}

// appendCtxFields extracts known keys (TraceIDKey, RequestIDKey, SpanIDKey)
// from the context and appends them as key-value field pairs.
// Returns fields unchanged if ctx is nil or contains no known keys.
func appendCtxFields(ctx context.Context, fields []string) []string {
	if ctx == nil {
		return fields
	}
	if v, ok := ctx.Value(TraceIDKey).(string); ok && v != "" {
		fields = append(fields, "trace_id", v)
	}
	if v, ok := ctx.Value(RequestIDKey).(string); ok && v != "" {
		fields = append(fields, "request_id", v)
	}
	if v, ok := ctx.Value(SpanIDKey).(string); ok && v != "" {
		fields = append(fields, "span_id", v)
	}
	return fields
}

// -----------------------------------------------------------------------------
// Core Logging (Internal)
// -----------------------------------------------------------------------------

// log is the internal core logging method with configurable caller skip.
// This is the hot path: zero allocations, zero mutex locks, zero map lookups.
func (l *Logger) log(level Level, logType string, msg []byte, skip int, fields ...string) {
	// 1. Atomic level check (~2ns, zero alloc)
	if level < Level(l.currentLevel.Load()) {
		return
	}

	// 2. Lock-free profile lookup (atomic.Pointer + linear scan)
	p := l.getProfile(logType)

	// 3. Lock-free sampling (atomic.Add)
	if p.sampleRate > 0 {
		if p.sampleCount.Add(1)%p.sampleRate != 0 {
			return
		}
	}

	// 4. Lock-free rate limiting (atomic CAS)
	if p.rlLimit > 0 {
		if !l.checkAtomicRateLimit(p) {
			return
		}
	}

	// 5. Caller info (opt-in, ~460ns + 2 allocs when enabled)
	var file string
	var line int
	if l.cfg.IncludeCaller {
		_, file, line, _ = runtime.Caller(skip)
		for i := len(file) - 1; i > 0; i-- {
			if file[i] == '/' {
				file = file[i+1:]
				break
			}
		}
	}

	// 6. Pool get, populate, and channel send (non-blocking)
	e := l.pool.Get().(*Entry)
	e.Level = level
	e.Type = logType
	e.Msg = append(e.Msg[:0], msg...)
	e.File = file
	e.Line = line
	e.Fields = append(e.Fields[:0], fields...)
	e.Profile = p

	select {
	case l.logCh <- e:
	default:
		// Channel full: drop the entry and increment the counter
		l.drops.Add(1)
		if l.onDrop != nil {
			l.onDrop(l.drops.Load())
		}
		e.Reset()
		l.pool.Put(e)
	}
}

// checkAtomicRateLimit performs lock-free rate limiting using Compare-And-Swap.
// On window expiry, the counter is reset to 0 via CAS, then every goroutine
// (including the one that won the CAS) increments via Add(1). This ensures
// a single atomic increment point, eliminating the race window that existed
// when Store(1) and Add(1) were used as separate paths.
func (l *Logger) checkAtomicRateLimit(p *SubProfile) bool {
	now := time.Now().Unix()
	resetTime := p.rlResetAt.Load()
	if now >= resetTime {
		if p.rlResetAt.CompareAndSwap(resetTime, now+p.rlWindow) {
			p.rlCount.Store(0)
		}
		// CAS failure means another goroutine already reset the window;
		// fall through to the shared increment path.
	}
	return p.rlCount.Add(1) <= p.rlLimit
}

// -----------------------------------------------------------------------------
// Worker & Lifecycle
// -----------------------------------------------------------------------------

// Start begins the worker goroutine that processes log entries.
// It writes to the configured OutputFile or stderr.
func (l *Logger) Start(ctx context.Context) {
	var w io.Writer
	if l.cfg.OutputFile != "" && l.currentWriter != nil {
		w = l.currentWriter
	} else {
		w = os.Stderr
	}
	l.StartWithWriter(ctx, w)
}

// StartWithWriter begins the worker goroutine with a custom io.Writer.
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
	l.globalWriterMu.Lock()
	l.globalWriter = w
	l.globalWriterMu.Unlock()

	// Signal that the worker has started (first call only, thread-safe)
	l.startOnce.Do(func() { close(l.started) })
	// Signal that the worker has exited when this function returns
	defer close(l.workerDone)

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
			buf = l.formatEntry(buf, e)
			e.Reset()
			l.pool.Put(e)

			if len(buf) >= flushThreshold {
				written := len(buf)
				if _, err := bw.Write(buf); err != nil {
					fmt.Fprintf(os.Stderr, "loggerj: write error: %v\n", err)
				}
				if err := bw.Flush(); err != nil {
					fmt.Fprintf(os.Stderr, "loggerj: flush error: %v\n", err)
				}
				buf = buf[:0]
				if l.cfg.OutputFile != "" {
					l.currentSize += int64(written)
					if err := l.rotateLogFile(); err != nil {
						fmt.Fprintf(os.Stderr, "loggerj: rotation error: %v\n", err)
					}
				}
			}

		// ── Periodic flush ──
		case <-ticker.C:
			if len(buf) > 0 {
				if _, err := bw.Write(buf); err != nil {
					fmt.Fprintf(os.Stderr, "loggerj: write error: %v\n", err)
				}
				if err := bw.Flush(); err != nil {
					fmt.Fprintf(os.Stderr, "loggerj: flush error: %v\n", err)
				}
				buf = buf[:0]
			}

		// ── Explicit Flush() call ──
		// Drain ALL pending channel entries first, then write the buffer.
		// This guarantees that Flush() never loses in-flight log entries.
		case done := <-l.flushCh:
			for {
				select {
				case e := <-l.logCh:
					buf = l.formatEntry(buf, e)
					e.Reset()
					l.pool.Put(e)
				default:
					// Channel empty — write and flush the buffer
					if len(buf) > 0 {
						if _, err := bw.Write(buf); err != nil {
							fmt.Fprintf(os.Stderr, "loggerj: write error: %v\n", err)
						}
						if err := bw.Flush(); err != nil {
							fmt.Fprintf(os.Stderr, "loggerj: flush error: %v\n", err)
						}
						buf = buf[:0]
					}
					close(done)
					continue workerLoop
				}
			}
		}
	}
}

// -----------------------------------------------------------------------------
// Formatting
// -----------------------------------------------------------------------------

// formatEntry formats a log entry into the buffer. The timestamp is captured
// here (worker-side) rather than in the hot path, removing a vDSO syscall
// from the caller's critical path.
func (l *Logger) formatEntry(buf []byte, e *Entry) []byte {
	ts := time.Now().UnixMilli()
	if l.cfg.JSONOutput {
		return l.formatJSON(buf, e, ts)
	}
	return l.formatText(buf, e, ts)
}

// formatText formats a log entry as human-readable text:
//
//	[1704067200123] INFO [HTTP] request received method=GET path=/api
func (l *Logger) formatText(buf []byte, e *Entry, ts int64) []byte {
	buf = append(buf, '[')
	buf = strconv.AppendInt(buf, ts, 10)
	buf = append(buf, "] "...)
	buf = append(buf, e.Level.String()...)
	buf = append(buf, " ["...)
	buf = append(buf, e.Type...)
	buf = append(buf, "] "...)

	if e.File != "" {
		buf = append(buf, e.File...)
		buf = append(buf, ':')
		buf = strconv.AppendInt(buf, int64(e.Line), 10)
		buf = append(buf, ' ')
	}

	// Inject pre-baked text prefix (zero CPU cost)
	if e.Profile != nil && len(e.Profile.textPrefix) > 0 {
		buf = append(buf, e.Profile.textPrefix...)
	}

	buf = append(buf, e.Msg...)

	if len(e.Fields) > 0 {
		buf = append(buf, ' ')
		buf = appendFieldsText(buf, e.Fields)
	}
	buf = append(buf, '\n')
	return buf
}

// formatJSON formats a log entry as a single-line JSON object:
//
//	{"ts":1704067200123,"level":"INFO","type":"HTTP","msg":"request received","fields":{"method":"GET"}}
func (l *Logger) formatJSON(buf []byte, e *Entry, ts int64) []byte {
	buf = append(buf, `{"ts":`...)
	buf = strconv.AppendInt(buf, ts, 10)
	buf = append(buf, `,"level":"`...)
	buf = append(buf, e.Level.String()...)
	buf = append(buf, '"')
	buf = append(buf, `,"type":`...)
	buf = appendJSONString(buf, e.Type)

	if e.File != "" {
		buf = append(buf, `,"file":`...)
		buf = appendJSONString(buf, e.File)
		buf = append(buf, `,"line":`...)
		buf = strconv.AppendInt(buf, int64(e.Line), 10)
	}

	// Inject pre-baked JSON prefix (starts with ',', zero CPU cost)
	if e.Profile != nil && len(e.Profile.jsonPrefix) > 0 {
		buf = append(buf, e.Profile.jsonPrefix...)
	}

	buf = append(buf, `,"msg":`...)
	buf = appendJSONStringBytes(buf, e.Msg)

	if len(e.Fields) > 0 {
		buf = append(buf, `,"fields":{`...)
		buf = appendFieldsJSON(buf, e.Fields)
		buf = append(buf, '}')
	}

	buf = append(buf, "}\n"...)
	return buf
}

// appendFieldsText appends key-value fields in text format: key1=val1 key2=val2
func appendFieldsText(buf []byte, fields []string) []byte {
	for i := 0; i < len(fields); i += 2 {
		if i > 0 {
			buf = append(buf, ' ')
		}
		buf = append(buf, fields[i]...)
		buf = append(buf, '=')
		if i+1 < len(fields) {
			buf = append(buf, fields[i+1]...)
		}
	}
	return buf
}

// appendFieldsJSON appends key-value fields in JSON format: "key1":"val1","key2":"val2"
func appendFieldsJSON(buf []byte, fields []string) []byte {
	for i := 0; i < len(fields); i += 2 {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = appendJSONString(buf, fields[i])
		buf = append(buf, ':')
		if i+1 < len(fields) {
			buf = appendJSONString(buf, fields[i+1])
		} else {
			buf = append(buf, `""`...)
		}
	}
	return buf
}

// appendJSONString appends a JSON-escaped string (with surrounding quotes) to buf.
// Control characters (< 0x20), DEL (0x7F), double quotes, and backslashes
// are properly escaped per RFC 8259.
func appendJSONString(buf []byte, s string) []byte {
	buf = append(buf, '"')
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7F || c == '"' || c == '\\' {
			if start < i {
				buf = append(buf, s[start:i]...)
			}
			buf = append(buf, '\\')
			switch c {
			case '"', '\\':
				buf = append(buf, c)
			case '\n':
				buf = append(buf, 'n')
			case '\r':
				buf = append(buf, 'r')
			case '\t':
				buf = append(buf, 't')
			default:
				buf = append(buf, 'u', '0', '0',
					"0123456789abcdef"[c>>4],
					"0123456789abcdef"[c&0xf])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		buf = append(buf, s[start:]...)
	}
	buf = append(buf, '"')
	return buf
}

// appendJSONStringBytes appends a JSON-escaped byte slice (with surrounding
// quotes) to buf. Identical escaping rules as appendJSONString.
func appendJSONStringBytes(buf []byte, s []byte) []byte {
	buf = append(buf, '"')
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7F || c == '"' || c == '\\' {
			if start < i {
				buf = append(buf, s[start:i]...)
			}
			buf = append(buf, '\\')
			switch c {
			case '"', '\\':
				buf = append(buf, c)
			case '\n':
				buf = append(buf, 'n')
			case '\r':
				buf = append(buf, 'r')
			case '\t':
				buf = append(buf, 't')
			default:
				buf = append(buf, 'u', '0', '0',
					"0123456789abcdef"[c>>4],
					"0123456789abcdef"[c&0xf])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		buf = append(buf, s[start:]...)
	}
	buf = append(buf, '"')
	return buf
}

// -----------------------------------------------------------------------------
// File Rotation & I/O
// -----------------------------------------------------------------------------

// openLogFile opens (or reopens) the configured log file and initializes
// the buffered writer. Called during NewLogger and after rotation.
func (l *Logger) openLogFile() error {
	l.rotationMu.Lock()
	defer l.rotationMu.Unlock()

	if l.currentFile != nil {
		if err := l.currentWriter.Flush(); err != nil {
			fmt.Fprintf(os.Stderr, "loggerj: flush error on reopen: %v\n", err)
		}
		l.currentFile.Close()
	}

	f, err := os.OpenFile(l.outputFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}

	l.currentFile = f
	l.currentWriter = bufio.NewWriterSize(f, l.cfg.WriterBufferSize)
	l.currentSize = stat.Size()

	l.globalWriterMu.Lock()
	l.globalWriter = l.currentWriter
	l.globalWriterMu.Unlock()
	return nil
}

// rotateLogFile performs size-based log rotation. When the current file
// exceeds MaxFileSize, it is renamed with a numeric suffix (.1, .2, ...)
// and a new file is created. Old backups beyond MaxBackupFiles are removed.
func (l *Logger) rotateLogFile() error {
	if l.cfg.MaxFileSize <= 0 || l.currentSize < l.cfg.MaxFileSize {
		return nil
	}

	l.rotationMu.Lock()
	defer l.rotationMu.Unlock()

	if l.currentWriter != nil {
		if err := l.currentWriter.Flush(); err != nil {
			fmt.Fprintf(os.Stderr, "loggerj: flush before rotation failed: %v\n", err)
		}
	}
	if l.currentFile != nil {
		if err := l.currentFile.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "loggerj: close before rotation failed: %v\n", err)
		}
	}

	// Shift existing backups: .4 → .5, .3 → .4, .2 → .3, .1 → .2
	for i := l.cfg.MaxBackupFiles - 1; i > 0; i-- {
		src := fmt.Sprintf("%s.%d", l.outputFilePath, i)
		dst := fmt.Sprintf("%s.%d", l.outputFilePath, i+1)
		if err := os.Rename(src, dst); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "loggerj: failed to rotate backup %d -> %d: %v\n", i, i+1, err)
		}
	}

	// Rename current log to .1
	if err := os.Rename(l.outputFilePath, l.outputFilePath+".1"); err != nil {
		fmt.Fprintf(os.Stderr, "loggerj: failed to rename current log: %v\n", err)
	}

	// Remove the oldest backup if it exceeds MaxBackupFiles
	if l.cfg.MaxBackupFiles > 0 {
		oldest := fmt.Sprintf("%s.%d", l.outputFilePath, l.cfg.MaxBackupFiles)
		if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "loggerj: failed to remove oldest backup: %v\n", err)
		}
	}

	// Create a fresh log file
	f, err := os.OpenFile(l.outputFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open new log file: %w", err)
	}
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("failed to stat new log file: %w", err)
	}

	l.currentFile = f
	l.currentWriter = bufio.NewWriterSize(f, l.cfg.WriterBufferSize)
	l.currentSize = stat.Size()

	l.globalWriterMu.Lock()
	l.globalWriter = l.currentWriter
	l.globalWriterMu.Unlock()
	return nil
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
			buf = l.formatEntry(buf, e)
			e.Reset()
			l.pool.Put(e)
		default:
			if len(buf) > 0 {
				if _, err := bw.Write(buf); err != nil {
					fmt.Fprintf(os.Stderr, "loggerj: write error during drain: %v\n", err)
				}
				if err := bw.Flush(); err != nil {
					fmt.Fprintf(os.Stderr, "loggerj: flush error during drain: %v\n", err)
				}
			}
			return
		}
	}
}

// Flush forces an immediate flush of all pending log entries. It signals
// the worker to drain the channel and write the buffer, then blocks until
// the worker confirms completion (or times out after 1 second).
//
// If no worker is running (globalWriter is nil), Flush drains and discards
// all pending entries to prevent channel blockage.
func (l *Logger) Flush() {
	l.globalWriterMu.Lock()
	w := l.globalWriter
	l.globalWriterMu.Unlock()

	if w == nil {
		// No worker running: drain and discard to prevent blockage
		for {
			select {
			case e := <-l.logCh:
				e.Reset()
				l.pool.Put(e)
			default:
				return
			}
		}
	}

	done := make(chan struct{})
	select {
	case l.flushCh <- done:
		select {
		case <-done:
		case <-time.After(1 * time.Second):
		}
	default:
		// flushCh full (another Flush in progress): brief retry
		time.Sleep(time.Millisecond)
		select {
		case l.flushCh <- done:
			select {
			case <-done:
			case <-time.After(1 * time.Second):
			}
		default:
			return
		}
	}
}

// Close releases resources held by the logger, including flushing the
// buffered writer and closing the log file handle.
func (l *Logger) Close() error {
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

// -----------------------------------------------------------------------------
// Observability
// -----------------------------------------------------------------------------

// Drops returns the total number of log entries dropped because the
// internal channel was full.
func (l *Logger) Drops() uint64 {
	return l.drops.Load()
}

// ResetDrops resets the drop counter to zero.
func (l *Logger) ResetDrops() {
	l.drops.Store(0)
}

// SetOnDrop registers a callback invoked whenever a log entry is dropped
// due to a full channel. The callback receives the current total drop count.
//
// WARNING: This callback is invoked from the hot path. Keep it fast
// (e.g., atomic counter increment, metrics gauge update). Never perform
// I/O or acquire locks inside this callback.
func (l *Logger) SetOnDrop(fn func(dropped uint64)) {
	l.onDrop = fn
}

// Stats returns a snapshot of logger statistics.
func (l *Logger) Stats() map[string]uint64 {
	return map[string]uint64{
		"drops":        l.drops.Load(),
		"channel_size": uint64(len(l.logCh)),
		"channel_cap":  uint64(cap(l.logCh)),
	}
}

// -----------------------------------------------------------------------------
// Standard Library Integration (io.Writer Adapter)
// -----------------------------------------------------------------------------

// StdLogWriter wraps a Logger to implement the io.Writer interface.
// This allows the standard library "log" package (and third-party libraries
// that rely on it) to route their output through loggerj's async pipeline.
type StdLogWriter struct {
	logger  *Logger
	level   Level
	logType string
}

// AsWriter returns an io.Writer that routes all writes to the logger
// at the specified level and logType.
//
// Usage with the standard library log package:
//
//	log.SetFlags(0) // Disable std log timestamps; loggerj adds its own
//	log.SetOutput(logger.AsWriter(loggerj.LevelInfo, "STDLIB"))
//
// Note: This adapter incurs a minor allocation (string(p)) per write.
// This is acceptable for intercepting legacy or third-party logs but
// should not be used for the application's primary high-throughput path.
func (l *Logger) AsWriter(level Level, logType string) io.Writer {
	return &StdLogWriter{
		logger:  l,
		level:   level,
		logType: logType,
	}
}

// Write implements the io.Writer interface. Trailing newlines from
// std log are trimmed for cleaner loggerj output.
func (w *StdLogWriter) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\n")
	w.logger.Log(w.level, w.logType, unsafeStringToBytes(msg))
	return len(p), nil
}
