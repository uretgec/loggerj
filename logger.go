package loggerj

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

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
	dropsByLevel [4]atomic.Uint64 // indexed by Level
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
	syncFile        *os.File      // Direct file handle for sync writes (O_APPEND)
	syncBufPool     sync.Pool     // Pooled []byte buffers for sync formatting
	syncBw          *bufio.Writer // Shared buffered writer for OSBuffered/FsyncEveryN
	syncMu          sync.Mutex    // Protects syncBw (held during copy, flush and fsync)
	syncLastFlushMs atomic.Int64  // UnixMilli timestamp of last OSBuffered flush
	syncWriteCount  atomic.Int64  // Tracks writes for FsyncEveryN tier

	// syncWriteErrors counts failed write(2) calls in sync mode.
	// Audit-oriented users must monitor this; a non-zero value means
	// at least one log entry was lost despite the durability tier.
	syncWriteErrors atomic.Uint64

	// rotationErrors counts failed rotation operations (rename, remove, reopen).
	// A non-zero value means the rotation chain broke silently — logs may be
	// written to an unrotated file or lost entirely if reopen fails.
	rotationErrors atomic.Uint64

	// writeErrors counts failed writes and flushes of the async worker.
	writeErrors atomic.Uint64

	// lastWriteWarn and lastRotateWarn rate-limit the stderr messages for
	// write and rotation errors (see warnLimited).
	lastWriteWarn  atomic.Int64
	lastRotateWarn atomic.Int64

	// workerOut is the writer the running worker was started with. Only the
	// worker goroutine uses it.
	workerOut io.Writer

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

	// workerMu guards workerCancel and workerStopped, which belong to the
	// current (or last) worker run. Close uses them to stop the worker and
	// wait for it to exit.
	workerMu      sync.Mutex
	workerCancel  context.CancelFunc
	workerStopped chan struct{}

	// closed makes Close idempotent.
	closed atomic.Bool

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
	if l.cfg.FsyncEveryNCount <= 0 {
		l.cfg.FsyncEveryNCount = 100 // documented default
	}
	if l.cfg.FileMode == 0 {
		l.cfg.FileMode = 0o644
	}
	if l.cfg.DurationUnit < 0 {
		l.cfg.DurationUnit = 0
	}

	// Sync mode has no worker, so it needs neither the channel nor the
	// buffered file handle opened by openLogFile (it opens its own handle).
	if !l.cfg.SyncMode {
		l.logCh = make(chan *Entry, l.cfg.ChannelSize)
	}
	l.currentLevel.Store(uint32(LevelInfo))
	l.defaultProfile = &SubProfile{Name: "DEFAULT"}

	if l.cfg.OutputFile != "" && !l.cfg.SyncMode {
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
			f, err := os.OpenFile(l.cfg.OutputFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, l.cfg.FileMode)
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

		}

		// For OSBuffered tier: initialize last flush timestamp.
		if l.cfg.DurabilityTier == OSBuffered {
			l.syncLastFlushMs.Store(time.Now().UnixMilli())
		}

		l.startOnce.Do(func() { close(l.started) })
	}

	return l
}

// reservedKeys are the keys loggerj writes itself at the top level of a JSON
// entry. A WithFields key with one of these names would produce a duplicate
// key, so RegisterSub ignores such fields.
var reservedKeys = [...]string{"ts", "level", "type", "msg", "file", "line", "fields"}

func isReservedKey(k string) bool {
	for _, r := range reservedKeys {
		if k == r {
			return true
		}
	}
	return false
}

// filterReservedFields returns fields without the key-value pairs whose key
// is reserved, and warns on stderr for each one. It returns fields itself
// (not a copy) when nothing was removed.
func filterReservedFields(logType string, fields []string) []string {
	var out []string
	for i := 0; i < len(fields); i += 2 {
		if isReservedKey(fields[i]) {
			fmt.Fprintf(os.Stderr, "loggerj: RegisterSub(%q): field key %q is reserved and was ignored\n", logType, fields[i])
			if out == nil {
				out = append(make([]string, 0, len(fields)), fields[:i]...)
			}
			continue
		}
		if out != nil {
			out = append(out, fields[i])
			if i+1 < len(fields) {
				out = append(out, fields[i+1])
			}
		}
	}
	if out == nil {
		return fields
	}
	return out
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
		p.tempFields = filterReservedFields(logType, p.tempFields)
	}
	if len(p.tempFields) > 0 {
		p.textPrefix = l.buildTextPrefix(p.tempFields)
		p.jsonPrefix = l.buildJSONPrefix(p.tempFields)
	}
	// Release raw fields immediately after baking
	p.tempFields = nil

	// Initialize rate limit window. WithRateLimit sets rlWindowMs and
	// rlStartMs directly. If rlLimit is somehow positive without a window,
	// fall back to Config.RateLimitWindow (seconds -> ms).
	if p.rlLimit > 0 {
		if p.rlWindowMs <= 0 {
			p.rlWindowMs = l.cfg.RateLimitWindow * 1000
		}
		if p.rlWindowMs < 1 {
			p.rlWindowMs = 1
		}
		if p.rlStartMs == 0 {
			p.rlStartMs = monoMillis()
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
			buf = appendTextString(buf, fields[i+1])
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
