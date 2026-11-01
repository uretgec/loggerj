package loggerj

import (
	"os"
	"time"
)

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
	// OSBuffered (default): writes go to a shared 64KB bufio.Writer. The
	// buffer is flushed to the file when it is nearly full, or when a log
	// call observes that at least 10ms have passed since the last flush.
	// There is NO background timer: entries written after the last flush
	// stay in process memory until the next log call, Flush() or Close().
	//
	// Survives: nothing beyond the last flush. Entries still in the buffer
	// are lost on a process crash (panic, SIGKILL, OOM). Entries already
	// flushed to the file survive a process crash but not an OS crash or
	// power loss (they may still be in the page cache).
	//
	// Throughput: ~110ns/op on the reference machine (BENCH.md) because a
	// write is a memory copy, not a syscall. Do not compare this number
	// with loggers that issue one write(2) per entry; compare Direct.
	OSBuffered DurabilityTier = iota

	// Direct: one write(2) syscall per log entry, no user-space buffering.
	// Survives a process crash (data is in the OS page cache). Does NOT
	// survive an OS crash or power loss.
	//
	// Throughput: ~1.5µs/op on the reference machine (syscall dominated).
	Direct

	// FsyncEveryN: entries go to the shared bufio.Writer; every Nth entry
	// (Config.FsyncEveryNCount) the buffer is flushed and fsync(2) is called.
	// Flush() and Close() also flush and fsync.
	//
	// Survives an OS crash or power loss for entries up to the last fsync.
	// The entries written after the last fsync (up to N-1) are still in
	// process memory and are lost on a process crash.
	//
	// Throughput: see BENCH.md (BenchmarkSyncMode_FsyncEveryN).
	FsyncEveryN

	// FsyncEveryWrite: write(2) followed by fsync(2) for every entry.
	// Survives a process crash and, if the hardware honors fsync, an OS
	// crash or power loss. Intended for low-volume audit trails.
	//
	// Throughput: milliseconds per entry on the reference machine
	// (BENCH.md); depends heavily on OS and disk.
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
	// WithRateLimit always sets its own window, so this value is effectively
	// unused.
	//
	// Deprecated: set the window with WithRateLimit. Kept for compatibility.
	RateLimitWindow int64

	// ErrorBlockTimeout makes ERROR entries wait up to this long for space
	// when the channel is full, instead of being dropped at once. The caller
	// blocks while it waits, so use it only where losing an error is worse
	// than a slow call. If no worker is draining the channel, every ERROR
	// call waits for the full timeout before it is dropped. 0 (default)
	// never blocks. Ignored in SyncMode.
	ErrorBlockTimeout time.Duration

	// DurationUnit selects how JSON output encodes Dur fields. 0 (default)
	// writes a string such as "150ms". A positive unit (time.Millisecond,
	// time.Second, ...) writes a number in that unit, e.g. 150 or 1.5, which
	// log pipelines can aggregate. Text output always uses the string form.
	DurationUnit time.Duration

	// TimestampFormat selects the format of the entry timestamp. "" (default)
	// writes Unix milliseconds as a number ("ts":1704067200123 in JSON,
	// [1704067200123] in text). Any time layout, for example
	// time.RFC3339Nano, writes the UTC time in that layout instead; JSON
	// output quotes it, so the layout must not produce characters that need
	// JSON escaping. Timestamps have millisecond resolution.
	TimestampFormat string

	// IncludeCaller adds file:line information to each log entry.
	// WARNING: Adds ~460ns overhead and 2 allocations per log entry.
	// Should be disabled in production for maximum performance. Default: false
	IncludeCaller bool

	// OutputFile is the path to the log file. If empty, logs are written to stderr.
	OutputFile string

	// FileMode is the permission used when the log file (and its rotated
	// replacement) is created. The process umask still applies. Use 0o640 or
	// 0o600 for logs that may contain personal data. Default: 0o644
	FileMode os.FileMode

	// MaxFileSize is the maximum size of the log file before rotation.
	// If 0, rotation is disabled. Rotation is performed by the async worker
	// only; it is NOT supported in SyncMode (the value is ignored there).
	// Default: 0
	MaxFileSize int64

	// MaxBackupFiles is the number of rotated files to keep
	// (OutputFile.1 ... OutputFile.N). 0 keeps a single backup
	// (OutputFile.1). Only effective if MaxFileSize > 0 and not in SyncMode.
	// Default: 0
	MaxBackupFiles int

	// SyncMode bypasses the async channel/worker pipeline: each log call
	// formats the entry on the caller's goroutine and writes it according to
	// DurabilityTier. When SyncMode is true:
	//
	//   - The log channel and worker goroutine are NOT created.
	//   - Start()/StartWithWriter() become no-ops.
	//   - Flush() flushes the shared buffer used by the OSBuffered and
	//     FsyncEveryN tiers (and fsyncs for FsyncEveryN). It is a no-op for
	//     Direct and FsyncEveryWrite, which have no user-space buffer.
	//   - Log rotation (MaxFileSize/MaxBackupFiles) is NOT supported.
	//
	// The file is opened with O_APPEND. Concurrency per tier:
	//
	//   - Direct / FsyncEveryWrite: loggerj takes no mutex. Concurrent
	//     Write calls on the same *os.File are serialized by the Go runtime,
	//     and on Linux local file systems an O_APPEND write is applied at
	//     the end of the file as a whole. Do not rely on that for NFS or
	//     other network file systems.
	//   - OSBuffered / FsyncEveryN: a mutex (syncMu) protects the shared
	//     bufio.Writer and is held during flush and fsync.
	//
	// This mode is FIXED at creation time and cannot be toggled at runtime.
	// Use it when you need a documented durability tier; see DurabilityTier
	// for what each tier survives.
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
		FileMode:         0o644,
		DurabilityTier:   OSBuffered,
		FsyncEveryNCount: 100,
	}
}
