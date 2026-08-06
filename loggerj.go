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
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
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
// Durability Tiers (Sync Mode)
// -----------------------------------------------------------------------------

// DurabilityTier controls what guarantee SyncMode provides beyond the
// write(2) syscall itself. Higher tiers trade latency for a stronger
// promise about what survives a crash.
type DurabilityTier uint8

const (
	// OSBuffered (default): uses bufio.Writer with periodic flush (every
	// 10ms or 100 logs, whichever comes first). Each write is a buffer
	// copy (~5ns), not a syscall. Survives process crash. Does NOT survive
	// OS crash or power loss — data may still be in the page cache.
	//
	// This is the guarantee zap and zerolog provide, though neither states
	// it explicitly; loggerj states it here on purpose.
	//
	// Throughput: ~300ns/op (competitive with zerolog/zap sync mode).
	OSBuffered DurabilityTier = iota

	// Direct: one write(2) syscall per log entry, no buffering. Relies on
	// O_APPEND atomicity for concurrent safety. Survives process crash.
	// Does NOT survive OS crash or power loss.
	//
	// Throughput: ~1550ns/op (syscall overhead dominates).
	Direct

	// FsyncEveryN: calls fsync(2) after every N writes (configured via
	// Config.FsyncEveryNCount). Survives OS crash / power loss for
	// committed entries, at the cost of fsync latency (typically 1-10ms
	// on spinning disk, less on SSD/NVMe).
	//
	// Throughput: ~5000ns/op (fsync latency amortized over N logs).
	FsyncEveryN

	// FsyncEveryWrite: calls fsync(2) after every single write. Maximum
	// durability, minimum throughput. Intended for audit trails, not
	// high-volume application logs.
	//
	// Throughput: ~5000-10000ns/op (fsync on every log).
	FsyncEveryWrite
)

// String returns the human-readable name of the durability tier.
func (d DurabilityTier) String() string {
	switch d {
	case OSBuffered:
		return "OSBuffered"
	case Direct:
		return "Direct"
	case FsyncEveryN:
		return "FsyncEveryN"
	case FsyncEveryWrite:
		return "FsyncEveryWrite"
	default:
		return "Unknown"
	}
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

	// SyncMode bypasses the async channel/worker pipeline and writes each log
	// entry directly to the underlying writer with a single atomic write()
	// syscall. When SyncMode is true:
	//
	//   - The log channel and worker goroutine are NOT created.
	//   - Start()/StartWithWriter() become no-ops.
	//   - Flush() becomes a no-op (writes are already synchronous).
	//   - Each log call performs: format → write() → return.
	//
	// The file is opened with O_APPEND, which the POSIX kernel guarantees
	// to be atomic for writes up to PIPE_BUF (typically 4096-65536 bytes).
	// This means concurrent goroutines writing to the same file will NOT
	// interleave their log lines — no mutex is needed.
	//
	// This mode is FIXED at creation time and cannot be toggled at runtime.
	// Use for audit trails, financial logs, or any scenario requiring
	// per-log write guarantees. Target: <400ns/op, ≤1 alloc/op.
	//
	// Default: false (async mode)
	SyncMode bool

	// DurabilityTier controls the sync-mode durability guarantee.
	// Only effective when SyncMode is true. Default: OSBuffered
	DurabilityTier DurabilityTier

	// FsyncEveryNCount is the number of writes before calling fsync(2).
	// Only effective when DurabilityTier is FsyncEveryN. Default: 100
	FsyncEveryNCount int
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
		DurabilityTier:   OSBuffered,
		FsyncEveryNCount: 100,
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

	// Lock-Free Rate Limiting (single-word CAS)
	// rlState packs the window index (upper 32 bits) and the in-window count
	// (lower 32 bits) into one atomic.Uint64. This makes the "reset-then-increment"
	// operation linearizable in a single Compare-And-Swap, closing the race window
	// that existed between rlResetAt.CompareAndSwap and rlCount.Store.
	rlLimit    int64         // Max logs per window (0 = unlimited)
	rlWindowMs int64         // Window size in milliseconds (supports sub-second)
	rlState    atomic.Uint64 // packed: (windowIdx << 32) | count

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
// limit is the max logs per window. window is the duration (e.g., time.Second,
// 500 * time.Millisecond). Sub-second windows are fully supported; unlike the
// previous second-granular implementation, a 500ms window no longer silently
// becomes 1s.
func WithRateLimit(limit int64, window time.Duration) SubOption {
	return func(p *SubProfile) {
		p.rlLimit = limit
		p.rlWindowMs = window.Milliseconds()
		if p.rlWindowMs < 1 {
			p.rlWindowMs = 1
		}
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
// Typed Field API (Zero-Allocation Structured Fields)
// -----------------------------------------------------------------------------

// FieldType identifies which union member of Field is populated,
// avoiding interface{} boxing on the hot path.
type FieldType uint8

const (
	StringType   FieldType = iota // Str field: value in Str
	Int64Type                     // Int/Int64: value in Num (as int64 bits)
	Uint64Type                    // Uint64: value in Num
	Float64Type                   // Float64: value in Num (as float64 bits)
	BoolType                      // Bool: value in Num (0 or 1)
	DurationType                  // Dur: value in Num (nanoseconds)
	ErrorType                     // Err: value in Str (err.Error() text)
)

// Field is a single structured log attribute. It carries its value in one
// of the untyped union members below instead of interface{}, so building a
// Field never allocates — the same guarantee zap.Field provides.
//
// Size: 48 bytes (string key + uint8 type + uint64 num + string val).
// Passed by value — no pointer indirection, no heap escape.
type Field struct {
	Key  string
	Type FieldType
	Num  uint64 // holds Int64/Uint64/Float64(bits)/Duration(ns)/Bool(0-1)
	Str  string // holds String value, or Error.Error() text
}

// --- Constructors (all zero-allocation, return by value) ---

// Str constructs a string field. Zero allocation.
func Str(key, val string) Field {
	return Field{Key: key, Type: StringType, Str: val}
}

// Int constructs an int field. Zero allocation — the value is stored
// directly in the Num union member, no boxing.
func Int(key string, val int) Field {
	return Field{Key: key, Type: Int64Type, Num: uint64(val)}
}

// Int64 constructs an int64 field. Zero allocation.
func Int64(key string, val int64) Field {
	return Field{Key: key, Type: Int64Type, Num: uint64(val)}
}

// Uint64 constructs a uint64 field. Zero allocation.
func Uint64(key string, val uint64) Field {
	return Field{Key: key, Type: Uint64Type, Num: val}
}

// Float64 constructs a float64 field. Zero allocation — bits are stored
// in Num via math.Float64bits.
func Float64(key string, val float64) Field {
	return Field{Key: key, Type: Float64Type, Num: uint64(floatToBits(val))}
}

// Bool constructs a boolean field. Zero allocation.
func Bool(key string, val bool) Field {
	var n uint64
	if val {
		n = 1
	}
	return Field{Key: key, Type: BoolType, Num: n}
}

// Dur constructs a duration field. Zero allocation — stored as nanoseconds.
func Dur(key string, val time.Duration) Field {
	return Field{Key: key, Type: DurationType, Num: uint64(val)}
}

// Err constructs an error field with key "error". Returns a zero-value
// Field (skipped by the encoder) if err is nil — mirrors zap.Error's
// nil-safety. Zero allocation when err is nil.
func Err(err error) Field {
	if err == nil {
		return Field{} // zero-value: skipped by encoder
	}
	return Field{Key: "error", Type: ErrorType, Str: err.Error()}
}

// ErrWithKey constructs an error field with a custom key.
// Returns a zero-value Field (skipped by the encoder) if err is nil.
func ErrWithKey(key string, err error) Field {
	if err == nil {
		return Field{}
	}
	return Field{Key: key, Type: ErrorType, Str: err.Error()}
}

// floatToBits converts float64 to uint64 bits without importing math
// in the hot path. Inlined by the compiler.
//
//go:nosplit
func floatToBits(f float64) uint64 {
	return *(*uint64)(unsafe.Pointer(&f))
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
	Level  Level
	Type   string
	Msg    []byte
	File   string
	Line   int
	Fields []string // legacy string-field API (Log, InfoString, ...)
	// FieldsV holds typed fields from the Field API (LogFields,
	// InfoFields, ...). An Entry uses exactly one of Fields or FieldsV
	// per log call, never both — the formatter checks FieldsV first.
	FieldsV []Field
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

	if cap(e.FieldsV) > 64 {
		e.FieldsV = nil
	} else {
		e.FieldsV = e.FieldsV[:0]
	}
}

// -----------------------------------------------------------------------------
// Profile Registry (Copy-on-Write, Lock-Free Reads)
// -----------------------------------------------------------------------------

// profileRegistry holds an immutable snapshot of all registered profiles.
// Hot-path reads are lock-free via atomic.Pointer. Cold-path writes
// (RegisterSub) create a new copy and swap atomically.
//
// Lookup strategy (adaptive, benchmark-driven):
//   - n ≤ 8: linear scan over names[] (cache-friendly, ~2-3ns per compare).
//     For very small registries the map overhead (~5ns) exceeds the scan cost.
//   - n > 8: map[string]*SubProfile for O(1) lookup (~7.7ns vs ~20.5ns
//     for a 16-profile linear scan — measured on Apple M1 Pro).
//
// Threshold 8 is conservative; benchmark on your registry size if you
// register hundreds of profiles.
type profileRegistry struct {
	names    []string
	profiles []*SubProfile
	lookup   map[string]*SubProfile // nil when n ≤ 8; populated when n > 8
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

	// Sync mode state (only used when cfg.SyncMode is true)
	syncFile    *os.File      // Direct file handle for sync writes (O_APPEND)
	syncBufPool sync.Pool     // Pooled []byte buffers for sync formatting
	syncBw      *bufio.Writer // Shared buffered writer for OSBuffered/FsyncEveryN
	// syncWriters     sync.Pool     // Pool of bufio.Writer for OSBuffered tier (lock-free)
	syncMu          sync.Mutex   // Protects syncBw memory copy (not the syscall)
	syncLastFlushMs atomic.Int64 // UnixMilli timestamp of last OSBuffered flush
	syncWriteCount  atomic.Int64 // Tracks writes for FsyncEveryN tier

	// syncWriteErrors counts failed write(2) calls in sync mode.
	// Audit-oriented users must monitor this; a non-zero value means
	// at least one log entry was lost despite the durability tier.
	syncWriteErrors atomic.Uint64

	// Deterministic lifecycle synchronization.
	// started is closed when the worker goroutine begins processing.
	// workerDone is closed when the worker goroutine exits.
	startOnce  sync.Once
	closeOnce  sync.Once // Protects workerDone from double-close panics
	started    chan struct{}
	workerDone chan struct{}

	// running tracks whether the worker goroutine is currently active.
	// Prevents double-start panics and allows Flush() to bypass the worker
	// channel synchronization when no worker is running.
	running atomic.Bool

	// flushMu serializes Flush() calls. Flush is a cold-path operation,
	// so a mutex is acceptable and eliminates the need for complex
	// non-blocking channel selects and retry sleeps.
	flushMu sync.Mutex

	// onDrop is an optional callback invoked when logs are dropped
	// due to a full channel. Called from the drop-path (not the happy path).
	// Uses atomic.Pointer to allow concurrent SetOnDrop calls without data races.
	onDrop atomic.Pointer[func(dropped uint64)]
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

	// Sync mode: skip channel/worker setup, open file with O_APPEND.
	// The file handle is used directly by syncWrite() for atomic writes.
	if l.cfg.SyncMode {
		if l.cfg.OutputFile != "" {
			// CRITICAL: os.O_APPEND ensures concurrent writes are atomic
			// and append to the end of the file instead of overwriting offset 0.
			f, err := os.OpenFile(l.cfg.OutputFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil {
				fmt.Fprintf(os.Stderr, "loggerj: failed to open sync log file: %v\n", err)
				l.syncFile = os.Stderr
			} else {
				l.syncFile = f
			}
		} else {
			l.syncFile = os.Stderr
		}

		l.syncBufPool = sync.Pool{
			New: func() any {
				buf := make([]byte, 0, 4096)
				return &buf
			},
		}

		// Initialize the shared bufio.Writer for buffered tiers.
		// 64KB buffer provides excellent amortization for small log lines.
		if l.cfg.DurabilityTier == OSBuffered || l.cfg.DurabilityTier == FsyncEveryN {
			l.syncBw = bufio.NewWriterSize(l.syncFile, 64*1024)

			// For OSBuffered tier: pool of bufio.Writer instances.
			// Each goroutine gets its own writer (no lock contention).
			// Writers are Reset() to syncFile before each use.
			// O_APPEND guarantees concurrent writes are atomic at the kernel level.
			// l.syncWriters = sync.Pool{
			// 	New: func() any {
			// 		return bufio.NewWriterSize(l.syncFile, 8192)
			// 	},
			// }
		}

		// For OSBuffered tier: initialize last flush timestamp.
		if l.cfg.DurabilityTier == OSBuffered {
			l.syncLastFlushMs.Store(time.Now().UnixMilli())
		}

		l.startOnce.Do(func() { close(l.started) })
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

	// Initialize rate limit window. WithRateLimit sets rlWindowMs directly;
	// if the caller did not use WithRateLimit but rlLimit is somehow > 0,
	// fall back to Config.RateLimitWindow (seconds → ms). rlState starts at
	// zero; the first checkAtomicRateLimit call naturally seeds the window.
	if p.rlLimit > 0 {
		if p.rlWindowMs <= 0 {
			p.rlWindowMs = l.cfg.RateLimitWindow * 1000
		}
		if p.rlWindowMs < 1 {
			p.rlWindowMs = 1000
		}
	}

	// Copy-on-write: clone the current registry, append/replace the profile,
	// and swap atomically. Hot-path readers see either the old or the new
	// snapshot — never a partially updated state.
	l.registerMu.Lock()
	defer l.registerMu.Unlock()
	old := l.registry.Load()
	var newReg *profileRegistry

	// profileLookupThreshold is the registry size above which a map is
	// faster than a linear scan. Measured: map ~7.7ns vs linear ~20.5ns
	// at n=16 on Apple M1 Pro. Below 8, linear wins on cache locality.
	const profileLookupThreshold = 8

	buildFullMap := func(names []string, profiles []*SubProfile) map[string]*SubProfile {
		m := make(map[string]*SubProfile, len(names))
		for i := range names {
			m[names[i]] = profiles[i]
		}
		return m
	}

	if old == nil {
		newReg = &profileRegistry{
			names:    []string{logType},
			profiles: []*SubProfile{p},
			lookup:   nil, // n=1, linear is optimal
		}
	} else {
		n := len(old.names)
		replaceIdx := -1
		for i := 0; i < n; i++ {
			if old.names[i] == logType {
				replaceIdx = i
				break
			}
		}

		var newNames []string
		var newProfiles []*SubProfile

		if replaceIdx >= 0 {
			// Replace existing profile (same size, swap the pointer)
			newNames = make([]string, n)
			newProfiles = make([]*SubProfile, n)
			copy(newNames, old.names)
			copy(newProfiles, old.profiles)
			newProfiles[replaceIdx] = p
		} else {
			// Append new profile
			newNames = make([]string, n+1)
			newProfiles = make([]*SubProfile, n+1)
			copy(newNames, old.names)
			copy(newProfiles, old.profiles)
			newNames[n] = logType
			newProfiles[n] = p
		}

		newReg = &profileRegistry{
			names:    newNames,
			profiles: newProfiles,
		}

		// Adaptive map maintenance: only pay map cost when it's faster.
		if len(newNames) > profileLookupThreshold {
			if old.lookup != nil {
				// Incremental O(1) update: clone + apply single change.
				newReg.lookup = make(map[string]*SubProfile, len(newNames))
				for k, v := range old.lookup {
					newReg.lookup[k] = v
				}
				newReg.lookup[logType] = p
			} else {
				// First crossing of the threshold: full O(n) rebuild.
				newReg.lookup = buildFullMap(newNames, newProfiles)
			}
		}
		// else: stay on linear scan (lookup stays nil)
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
// Reads the immutable registry snapshot via atomic.Pointer and uses an
// adaptive strategy benchmarked on Apple M1 Pro:
//
//   - n ≤ 8: linear scan (cache-friendly, ~2-3ns per compare, no map probe).
//   - n > 8: map[string]*SubProfile (~7.7ns, O(1), scales to thousands).
//
// Returns defaultProfile if no match is found.
//
//go:nosplit
func (l *Logger) getProfile(logType string) *SubProfile {
	reg := l.registry.Load()
	if reg == nil {
		return l.defaultProfile
	}
	// Fast path: map lookup when registry exceeds the threshold.
	if reg.lookup != nil {
		if p, ok := reg.lookup[logType]; ok {
			return p
		}
		return l.defaultProfile
	}
	// Small registry: linear scan wins on cache locality.
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

// -----------------------------------------------------------------------------
// Public API: Typed Field Methods (Zero-Allocation)
// -----------------------------------------------------------------------------

// LogFields logs a message with typed fields at the given level.
// This is the zero-allocation alternative to Log() with string fields.
// Caller skip is 1.
func (l *Logger) LogFields(level Level, logType string, msg []byte, fields ...Field) {
	l.logTyped(level, logType, msg, 1, fields...)
}

// DebugFields logs a message at LevelDebug with typed fields. Caller skip is 2.
func (l *Logger) DebugFields(logType string, msg []byte, fields ...Field) {
	l.logTyped(LevelDebug, logType, msg, 2, fields...)
}

// InfoFields logs a message at LevelInfo with typed fields. Caller skip is 2.
func (l *Logger) InfoFields(logType string, msg []byte, fields ...Field) {
	l.logTyped(LevelInfo, logType, msg, 2, fields...)
}

// WarnFields logs a message at LevelWarn with typed fields. Caller skip is 2.
func (l *Logger) WarnFields(logType string, msg []byte, fields ...Field) {
	l.logTyped(LevelWarn, logType, msg, 2, fields...)
}

// ErrorFields logs a message at LevelError with typed fields. Caller skip is 2.
func (l *Logger) ErrorFields(logType string, msg []byte, fields ...Field) {
	l.logTyped(LevelError, logType, msg, 2, fields...)
}

// --- String variants (zero-copy message conversion) ---

// DebugFieldsString logs a string message at LevelDebug with typed fields.
func (l *Logger) DebugFieldsString(logType string, msg string, fields ...Field) {
	l.logTyped(LevelDebug, logType, unsafeStringToBytes(msg), 2, fields...)
}

// InfoFieldsString logs a string message at LevelInfo with typed fields.
func (l *Logger) InfoFieldsString(logType string, msg string, fields ...Field) {
	l.logTyped(LevelInfo, logType, unsafeStringToBytes(msg), 2, fields...)
}

// WarnFieldsString logs a string message at LevelWarn with typed fields.
func (l *Logger) WarnFieldsString(logType string, msg string, fields ...Field) {
	l.logTyped(LevelWarn, logType, unsafeStringToBytes(msg), 2, fields...)
}

// ErrorFieldsString logs a string message at LevelError with typed fields.
func (l *Logger) ErrorFieldsString(logType string, msg string, fields ...Field) {
	l.logTyped(LevelError, logType, unsafeStringToBytes(msg), 2, fields...)
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

// checkGatesAfterLevel runs the lock-free sampling/rate-limit checks AFTER
// the level check has already been performed by the caller (the level check
// is inlined into log()/logTyped() for the ~2ns filtered fast-path).
//
//go:nosplit
func (l *Logger) checkGatesAfterLevel(logType string) (*SubProfile, bool) {
	// 1. Lock-free profile lookup (atomic.Pointer + linear scan)
	p := l.getProfile(logType)

	// 2. Lock-free sampling (atomic.Add)
	if p.sampleRate > 0 {
		if p.sampleCount.Add(1)%p.sampleRate != 0 {
			return nil, false
		}
	}

	// 3. Lock-free rate limiting (atomic CAS)
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

// dispatch sends a populated Entry to the worker via the non-blocking
// channel send, or drops it (incrementing the drop counter and invoking
// onDrop) if the channel is full. Shared by both the string-field and
// typed-field hot paths so there is exactly one drop-handling code path.
func (l *Logger) dispatch(e *Entry) {
	select {
	case l.logCh <- e:
	default:
		// Channel full: drop the entry and increment the counter
		l.drops.Add(1)
		if fn := l.onDrop.Load(); fn != nil {
			(*fn)(l.drops.Load())
		}
		e.Reset()
		l.pool.Put(e)
	}
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
// Identical gating and dispatch to log(), but populates Entry.FieldsV
// instead of Entry.Fields — Field values are copied by value (no
// interface{} boxing), so this path is zero-allocation even when the
// caller logs integers, booleans, floats, durations, or errors.
func (l *Logger) logTyped(level Level, logType string, msg []byte, skip int, fields ...Field) {
	// Fast-path: level filter (~2ns, zero alloc) — INLINED for hot path.
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
		l.syncWrite(level, logType, msg, p, fields, nil, file, line)
		return
	}

	file, line := l.captureCaller(skip)

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

// checkAtomicRateLimit performs lock-free rate limiting with a single
// linearizable Compare-And-Swap. The window index and in-window count are
// packed into one atomic.Uint64, so a window transition and the corresponding
// counter reset happen atomically. Under contention, losing goroutines simply
// retry with the freshly-published state — no separate Store(0) step exists,
// eliminating the race where a goroutine could observe an old counter value
// between the winner's CAS and its Store(0).
//
//go:nosplit
func (l *Logger) checkAtomicRateLimit(p *SubProfile) bool {
	nowMs := time.Now().UnixMilli()
	windowMs := p.rlWindowMs
	if windowMs <= 0 {
		windowMs = 1000 // Fallback to 1 second if somehow unset
	}
	nowWin := uint64(nowMs / windowMs)
	limit := uint64(p.rlLimit)
	for {
		state := p.rlState.Load()
		win := state >> 32
		cnt := state & 0xFFFFFFFF
		var newCnt, next uint64
		if win != nowWin {
			newCnt = 1
			next = (nowWin << 32) | 1
		} else {
			newCnt = cnt + 1
			next = state + 1
		}
		if p.rlState.CompareAndSwap(state, next) {
			return newCnt <= limit
		}
	}
}

// syncWrite formats and writes a log entry synchronously using a pooled
// buffer. The write strategy depends on Config.DurabilityTier:
//
//   - OSBuffered: buffer copy to bufio.Writer (~5ns), periodic flush (~10ms).
//     Throughput: ~300ns/op. Competitive with zerolog/zap sync mode.
//   - Direct: single write() syscall, O_APPEND atomic. Throughput: ~1550ns/op.
//   - FsyncEveryN: write() + fsync() every N logs. Throughput: ~5000ns/op.
//   - FsyncEveryWrite: write() + fsync() on every log. Throughput: ~5000-10000ns/op.
//
// Lock-free status per tier:
//   - Direct / FsyncEveryWrite: lock-free write path (O_APPEND atomic).
//   - OSBuffered / FsyncEveryN: syncMu held during buffer copy only,
//     not during the write syscall.
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
			l.syncBw.Write(buf)
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
				l.syncBw.Flush()
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
			l.syncBw.Write(buf)
		} else {
			l.syncFileWrite(buf)
		}
		count := l.syncWriteCount.Add(1)
		if count >= int64(l.cfg.FsyncEveryNCount) {
			if l.syncBw != nil {
				l.syncBw.Flush()
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
		l.syncWriteErrors.Add(1)
		fmt.Fprintf(os.Stderr, "loggerj: sync write error (tier=%s): %v\n",
			l.cfg.DurabilityTier, err)
	}
}

// syncFileSync wraps l.syncFile.Sync with error reporting. fsync failures
// are rare but indicate serious durability problems.
func (l *Logger) syncFileSync() {
	if err := l.syncFile.Sync(); err != nil {
		l.syncWriteErrors.Add(1)
		fmt.Fprintf(os.Stderr, "loggerj: sync fsync error (tier=%s): %v\n",
			l.cfg.DurabilityTier, err)
	}
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
	// Sync mode: no worker needed. Close started/workerDone immediately
	// and return. This allows code that unconditionally calls Start()
	// to work correctly in both async and sync modes.
	if l.cfg.SyncMode {
		l.startOnce.Do(func() { close(l.started) })
		l.closeOnce.Do(func() { close(l.workerDone) })
		return
	}

	// Prevent double-start panics and concurrent starts.
	// If the worker is already running (or has run and exited), this
	// call becomes a safe no-op.
	if !l.running.CompareAndSwap(false, true) {
		return
	}

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
	}()

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
				l.writeOut(bw, buf)
				buf = buf[:0]
				if l.cfg.OutputFile != "" {
					if rotated, err := l.rotateLogFile(); err != nil {
						fmt.Fprintf(os.Stderr, "loggerj: rotation error: %v\n", err)
					} else if rotated {
						// CRITICAL: Refresh the writer handle after rotation.
						// The old bw points to a closed file; l.currentWriter
						// is the fresh handle opened by rotateLogFile.
						bw = l.currentWriter
					}
				}
			}

		// ── Periodic flush ──
		case <-ticker.C:
			if len(buf) > 0 {
				l.writeOut(bw, buf)
				buf = buf[:0]
				// Optional: check rotation on ticker as well (safety net)
				if l.cfg.OutputFile != "" {
					if rotated, err := l.rotateLogFile(); err != nil {
						fmt.Fprintf(os.Stderr, "loggerj: rotation error: %v\n", err)
					} else if rotated {
						bw = l.currentWriter
					}
				}
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

	if len(e.FieldsV) > 0 {
		fieldsStart := len(buf)
		buf = append(buf, ' ')
		buf = appendTypedFieldsText(buf, e.FieldsV)
		// If only the leading space was added, all fields were zero-value —
		// remove the stray space to keep output clean.
		if len(buf) == fieldsStart+1 {
			buf = buf[:fieldsStart]
		}
	} else if len(e.Fields) > 0 {
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

	if len(e.FieldsV) > 0 {
		fieldsStart := len(buf)
		buf = append(buf, `,"fields":{`...)
		buf = appendTypedFieldsJSON(buf, e.FieldsV)
		// If nothing was appended after the opening brace, all fields were
		// zero-value (e.g., Err(nil)) — remove the empty object entirely.
		if len(buf) == fieldsStart+len(`,"fields":{`) {
			buf = buf[:fieldsStart]
		} else {
			buf = append(buf, '}')
		}
	} else if len(e.Fields) > 0 {
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
// Typed Field Encoders (shared by sync and async paths)
// -----------------------------------------------------------------------------

// appendTypedFieldsJSON appends typed fields in JSON format.
// Zero-value fields (Type==0 and empty Key) are skipped — this handles
// Err(nil) gracefully without branching at the caller.
func appendTypedFieldsJSON(buf []byte, fields []Field) []byte {
	first := true
	for i := range fields {
		f := &fields[i]
		if f.Key == "" && f.Type == StringType && f.Str == "" {
			continue // zero-value Field (e.g., Err(nil)) — skip
		}
		if !first {
			buf = append(buf, ',')
		}
		first = false
		buf = appendJSONString(buf, f.Key)
		buf = append(buf, ':')
		buf = appendFieldValueJSON(buf, f)
	}
	return buf
}

// appendFieldValueJSON appends the typed value of a single Field as JSON.
func appendFieldValueJSON(buf []byte, f *Field) []byte {
	switch f.Type {
	case StringType:
		buf = appendJSONString(buf, f.Str)
	case Int64Type:
		buf = strconv.AppendInt(buf, int64(f.Num), 10)
	case Uint64Type:
		buf = strconv.AppendUint(buf, f.Num, 10)
	case Float64Type:
		buf = strconv.AppendFloat(buf, float64FromBits(f.Num), 'g', -1, 64)
	case BoolType:
		if f.Num == 1 {
			buf = append(buf, "true"...)
		} else {
			buf = append(buf, "false"...)
		}
	case DurationType:
		// Render as string for readability: "150.5ms", "2.3s"
		buf = append(buf, '"')
		buf = appendDuration(buf, time.Duration(f.Num))
		buf = append(buf, '"')
	case ErrorType:
		buf = appendJSONString(buf, f.Str)
	default:
		buf = append(buf, "null"...)
	}
	return buf
}

// appendTypedFieldsText appends typed fields in text format: key=val key2=val2
func appendTypedFieldsText(buf []byte, fields []Field) []byte {
	for i := range fields {
		f := &fields[i]
		if f.Key == "" && f.Type == StringType && f.Str == "" {
			continue // zero-value Field — skip
		}
		if i > 0 {
			buf = append(buf, ' ')
		}
		buf = append(buf, f.Key...)
		buf = append(buf, '=')
		buf = appendFieldValueText(buf, f)
	}
	return buf
}

// appendFieldValueText appends the typed value of a single Field as text.
func appendFieldValueText(buf []byte, f *Field) []byte {
	switch f.Type {
	case StringType:
		buf = append(buf, f.Str...)
	case Int64Type:
		buf = strconv.AppendInt(buf, int64(f.Num), 10)
	case Uint64Type:
		buf = strconv.AppendUint(buf, f.Num, 10)
	case Float64Type:
		buf = strconv.AppendFloat(buf, float64FromBits(f.Num), 'g', -1, 64)
	case BoolType:
		if f.Num == 1 {
			buf = append(buf, "true"...)
		} else {
			buf = append(buf, "false"...)
		}
	case DurationType:
		buf = appendDuration(buf, time.Duration(f.Num))
	case ErrorType:
		buf = append(buf, f.Str...)
	default:
		buf = append(buf, "null"...)
	}
	return buf
}

// appendDuration formats a duration in human-readable form without
// allocation (Go's time.Duration.String() allocates).
func appendDuration(buf []byte, d time.Duration) []byte {
	if d == 0 {
		return append(buf, "0s"...)
	}
	if d < time.Microsecond {
		buf = strconv.AppendInt(buf, d.Nanoseconds(), 10)
		return append(buf, "ns"...)
	}
	if d < time.Millisecond {
		buf = strconv.AppendInt(buf, d.Microseconds(), 10)
		return append(buf, "µs"...)
	}
	if d < time.Second {
		buf = strconv.AppendInt(buf, d.Milliseconds(), 10)
		return append(buf, "ms"...)
	}
	if d < time.Minute {
		// e.g., "2.5s" — use integer seconds + fractional ms
		sec := d / time.Second
		ms := (d - sec*time.Second) / time.Millisecond
		buf = strconv.AppendInt(buf, int64(sec), 10)
		if ms > 0 {
			buf = append(buf, '.')
			buf = strconv.AppendInt(buf, int64(ms), 10)
		}
		return append(buf, 's')
	}
	// Fallback for >= 1 minute: "2m30s"
	min := d / time.Minute
	d -= min * time.Minute
	sec := d / time.Second
	buf = strconv.AppendInt(buf, int64(min), 10)
	buf = append(buf, 'm')
	if sec > 0 {
		buf = strconv.AppendInt(buf, int64(sec), 10)
		buf = append(buf, 's')
	}
	return buf
}

// float64FromBits converts uint64 bits back to float64.
//
//go:nosplit
func float64FromBits(b uint64) float64 {
	return *(*float64)(unsafe.Pointer(&b))
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

// rotateLogFile performs size-based log rotation. Returns (true, nil) if
// rotation occurred, (false, nil) if no rotation needed, or (false, err)
// on failure. The caller must refresh its bufio.Writer handle after a
// successful rotation to avoid writing to a closed file.
func (l *Logger) rotateLogFile() (rotated bool, err error) {
	if l.cfg.MaxFileSize <= 0 || l.currentSize < l.cfg.MaxFileSize {
		return false, nil
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
		return false, fmt.Errorf("failed to open new log file: %w", err)
	}
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return false, fmt.Errorf("failed to stat new log file: %w", err)
	}

	l.currentFile = f
	l.currentWriter = bufio.NewWriterSize(f, l.cfg.WriterBufferSize)
	l.currentSize = stat.Size()

	l.globalWriterMu.Lock()
	l.globalWriter = l.currentWriter
	l.globalWriterMu.Unlock()

	// At the end, before returning:
	return true, nil
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
// If no worker is running, Flush drains and discards all pending entries
// to prevent channel blockage. This also fixes the 1-second block that
// occurred when OutputFile was set but Start() hadn't been called yet.
func (l *Logger) Flush() {
	// Sync mode: writes are already synchronous. Nothing to flush.
	if l.cfg.SyncMode {
		return
	}

	l.flushMu.Lock()
	defer l.flushMu.Unlock()

	// If the worker is not running, drain the channel and discard entries.
	// This is critical for OutputFile configurations where globalWriter
	// is set during NewLogger, which previously caused Flush() to block
	// for 1 second waiting for a non-existent worker.
	if !l.running.Load() {
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

// Close releases resources held by the logger.
// In async mode: flushes the buffered writer and closes the log file.
// In sync mode: flushes the shared buffered writer and closes the sync file.
func (l *Logger) Close() error {
	if l.cfg.SyncMode {
		// CRITICAL: Final flush for OSBuffered and FsyncEveryN tiers.
		// This ensures all buffered data is persisted to disk before closing.
		if l.syncBw != nil {
			l.syncMu.Lock()
			l.syncBw.Flush()
			l.syncMu.Unlock()
		}
		if l.syncFile != nil && l.syncFile != os.Stderr {
			return l.syncFile.Close()
		}
		return nil
	}
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
// due to a full channel. Thread-safe; can be called concurrently with logging.
// Pass nil to unregister the callback.
func (l *Logger) SetOnDrop(fn func(dropped uint64)) {
	if fn == nil {
		l.onDrop.Store(nil)
		return
	}
	// Copy the function value to a local variable and store its pointer.
	// This causes 1 heap allocation, but SetOnDrop is a cold-path operation
	// (usually called once at startup), so this is perfectly acceptable.
	f := fn
	l.onDrop.Store(&f)
}

// Stats represents a snapshot of logger statistics. Using a struct
// instead of a map avoids heap allocations on every call, which is
// important for observability loops (e.g., Prometheus exporters) that
// poll Stats() frequently.
type Stats struct {
	Drops           uint64
	ChannelSize     uint64
	ChannelCap      uint64
	SyncWriteErrors uint64 // Non-zero means at least one sync-mode write failed

}

// Stats returns a snapshot of logger statistics.
func (l *Logger) Stats() Stats {
	return Stats{
		Drops:           l.drops.Load(),
		ChannelSize:     uint64(len(l.logCh)),
		ChannelCap:      uint64(cap(l.logCh)),
		SyncWriteErrors: l.syncWriteErrors.Load(),
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
// std log are trimmed for cleaner loggerj output. Uses bytes.TrimRight
// to avoid the string(p) allocation that occurred with strings.TrimRight.
//
// Zero-allocation guarantee: The Log() method immediately copies msg via
// append(e.Msg[:0], msg...), so the io.Writer contract (not retaining p
// after Write returns) is satisfied. This eliminates the only documented
// allocation in the AsWriter adapter path.
func (w *StdLogWriter) Write(p []byte) (int, error) {
	msg := bytes.TrimRight(p, "\n")
	w.logger.Log(w.level, w.logType, msg)
	return len(p), nil
}
