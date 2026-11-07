package loggerj

import (
	"bytes"
	"context"
	"io"
)

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
	if LevelInfo < Level(l.currentLevel.Load()) {
		return // filtered: skip the context lookups
	}
	fields = appendCtxFields(ctx, fields)
	l.log(LevelInfo, logType, unsafeStringToBytes(msg), 2, fields...)
}

// DebugCtx logs a message at LevelDebug with optional context extraction.
// If ctx is nil or contains no known keys, it behaves exactly like
// DebugString with zero additional cost.
func (l *Logger) DebugCtx(ctx context.Context, logType string, msg string, fields ...string) {
	if LevelDebug < Level(l.currentLevel.Load()) {
		return // filtered: skip the context lookups
	}
	fields = appendCtxFields(ctx, fields)
	l.log(LevelDebug, logType, unsafeStringToBytes(msg), 2, fields...)
}

// WarnCtx logs a message at LevelWarn with optional context extraction.
// If ctx is nil or contains no known keys, it behaves exactly like
// WarnString with zero additional cost.
func (l *Logger) WarnCtx(ctx context.Context, logType string, msg string, fields ...string) {
	if LevelWarn < Level(l.currentLevel.Load()) {
		return // filtered: skip the context lookups
	}
	fields = appendCtxFields(ctx, fields)
	l.log(LevelWarn, logType, unsafeStringToBytes(msg), 2, fields...)
}

// ErrorCtx logs a message at LevelError with optional context extraction.
// If ctx is nil or contains no known keys, it behaves exactly like
// ErrorString with zero additional cost.
func (l *Logger) ErrorCtx(ctx context.Context, logType string, msg string, fields ...string) {
	if LevelError < Level(l.currentLevel.Load()) {
		return // filtered: skip the context lookups
	}
	fields = appendCtxFields(ctx, fields)
	l.log(LevelError, logType, unsafeStringToBytes(msg), 2, fields...)
}

// appendCtxFieldsTyped is appendCtxFields for typed fields (slog adapter).
func appendCtxFieldsTyped(ctx context.Context, fields []Field) []Field {
	if ctx == nil {
		return fields
	}
	if v, ok := ctx.Value(TraceIDKey).(string); ok && v != "" {
		fields = append(fields, Str("trace_id", v))
	}
	if v, ok := ctx.Value(RequestIDKey).(string); ok && v != "" {
		fields = append(fields, Str("request_id", v))
	}
	if v, ok := ctx.Value(SpanIDKey).(string); ok && v != "" {
		fields = append(fields, Str("span_id", v))
	}
	return fields
}

// -----------------------------------------------------------------------------
// Public API: Typed Field Methods (Zero-Allocation)
// -----------------------------------------------------------------------------

// LogFields logs a message with typed fields at the given level.
// This is the zero-allocation alternative to Log() with string fields.
// Caller skip is 1.
func (l *Logger) LogFields(level Level, logType string, msg []byte, fields ...Field) {
	l.logTyped(level, logType, msg, 1, 0, fields...)
}

// DebugFields logs a message at LevelDebug with typed fields. Caller skip is 2.
func (l *Logger) DebugFields(logType string, msg []byte, fields ...Field) {
	l.logTyped(LevelDebug, logType, msg, 2, 0, fields...)
}

// InfoFields logs a message at LevelInfo with typed fields. Caller skip is 2.
func (l *Logger) InfoFields(logType string, msg []byte, fields ...Field) {
	l.logTyped(LevelInfo, logType, msg, 2, 0, fields...)
}

// WarnFields logs a message at LevelWarn with typed fields. Caller skip is 2.
func (l *Logger) WarnFields(logType string, msg []byte, fields ...Field) {
	l.logTyped(LevelWarn, logType, msg, 2, 0, fields...)
}

// ErrorFields logs a message at LevelError with typed fields. Caller skip is 2.
func (l *Logger) ErrorFields(logType string, msg []byte, fields ...Field) {
	l.logTyped(LevelError, logType, msg, 2, 0, fields...)
}

// --- String variants (zero-copy message conversion) ---

// DebugFieldsString logs a string message at LevelDebug with typed fields.
func (l *Logger) DebugFieldsString(logType string, msg string, fields ...Field) {
	l.logTyped(LevelDebug, logType, unsafeStringToBytes(msg), 2, 0, fields...)
}

// InfoFieldsString logs a string message at LevelInfo with typed fields.
func (l *Logger) InfoFieldsString(logType string, msg string, fields ...Field) {
	l.logTyped(LevelInfo, logType, unsafeStringToBytes(msg), 2, 0, fields...)
}

// WarnFieldsString logs a string message at LevelWarn with typed fields.
func (l *Logger) WarnFieldsString(logType string, msg string, fields ...Field) {
	l.logTyped(LevelWarn, logType, unsafeStringToBytes(msg), 2, 0, fields...)
}

// ErrorFieldsString logs a string message at LevelError with typed fields.
func (l *Logger) ErrorFieldsString(logType string, msg string, fields ...Field) {
	l.logTyped(LevelError, logType, unsafeStringToBytes(msg), 2, 0, fields...)
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
	for i := range l.dropsByLevel {
		l.dropsByLevel[i].Store(0)
	}
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
	DropsByLevel    [4]uint64 // indexed by Level: Debug, Info, Warn, Error
	ChannelSize     uint64
	ChannelCap      uint64
	SyncWriteErrors uint64 // Non-zero means at least one sync-mode write failed
	RotationErrors  uint64 // Non-zero means at least one rotation step failed
	WriteErrors     uint64 // Non-zero means at least one async write or flush failed
}

// Stats returns a snapshot of logger statistics.
func (l *Logger) Stats() Stats {
	var byLevel [4]uint64
	for i := range byLevel {
		byLevel[i] = l.dropsByLevel[i].Load()
	}
	return Stats{
		Drops:           l.drops.Load(),
		DropsByLevel:    byLevel,
		ChannelSize:     uint64(len(l.logCh)),
		ChannelCap:      uint64(cap(l.logCh)),
		SyncWriteErrors: l.syncWriteErrors.Load(),
		RotationErrors:  l.rotationErrors.Load(),
		WriteErrors:     l.writeErrors.Load(),
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
// Zero-allocation guarantee: The Write method uses bytes.TrimRight (not
// strings.TrimRight) to remove trailing newlines without allocation. The
// underlying Log() method immediately copies msg via append(e.Msg[:0], msg...),
// satisfying the io.Writer contract (not retaining p after Write returns).
//
// This adapter is suitable for intercepting legacy or third-party logs that
// use the standard library log package. For the application's primary
// high-throughput path, prefer the native logger.InfoString or logger.Info
// methods to avoid the function call overhead of the io.Writer interface.
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
