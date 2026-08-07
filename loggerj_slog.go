package loggerj

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// -----------------------------------------------------------------------------
// slog.Handler Adapter (Go 1.21+ Ecosystem Integration)
// -----------------------------------------------------------------------------
//
// SlogHandler implements slog.Handler, routing all slog calls through
// loggerj's zero-allocation typed-field pipeline.
//
// Group handling follows the loggerj Field model: nested groups are
// flattened to dotted keys. Example:
//
//	slog.Group("http", "method", "GET")
//
// becomes:
//
//	"http.method":"GET"
//
// This keeps the Field API allocation-free and works well with flat log
// pipelines such as Loki, Elasticsearch, and Datadog.
type SlogHandler struct {
	logger  *Logger
	logType string

	// attrs holds pre-converted WithAttrs fields.
	attrs []Field

	// group is the current WithGroup prefix.
	group string

	// fieldPool amortizes the per-Handle []Field slice allocation.
	fieldPool *sync.Pool
}

// slogFieldPool is a package-level pool of []Field slices used by
// SlogHandler.Handle.
var slogFieldPool = sync.Pool{
	New: func() any {
		buf := make([]Field, 0, 32)
		return &buf
	},
}

// NewSlogHandler returns a slog.Handler that routes all records into the
// given Logger under the specified logType.
func NewSlogHandler(l *Logger, logType string) *SlogHandler {
	return &SlogHandler{
		logger:    l,
		logType:   logType,
		fieldPool: &slogFieldPool,
	}
}

// Enabled implements slog.Handler.
func (h *SlogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= loggerjToSlogLevel(h.logger.GetLevel())
}

// Handle implements slog.Handler.
func (h *SlogHandler) Handle(ctx context.Context, r slog.Record) error {
	level := slogToLoggerjLevel(r.Level)

	bp := h.fieldPool.Get().(*[]Field)
	fields := (*bp)[:0]

	// Prepend pre-converted WithAttrs fields.
	fields = append(fields, h.attrs...)

	// Convert per-record attributes.
	r.Attrs(func(a slog.Attr) bool {
		fields = h.appendAttr(fields, a)
		return true
	})

	// Dispatch through the typed-field hot path.
	// skip=4: Handle -> slog dispatch -> slog.Info/Warn/etc -> caller.
	h.logger.logTyped(level, h.logType, []byte(r.Message), 4, fields...)

	// Return buffer to pool. Release oversized buffers.
	if cap(fields) > 256 {
		*bp = make([]Field, 0, 32)
	} else {
		*bp = fields
	}
	h.fieldPool.Put(bp)

	return nil
}

// WithAttrs implements slog.Handler.
func (h *SlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}

	newAttrs := make([]Field, 0, len(h.attrs)+len(attrs))
	newAttrs = append(newAttrs, h.attrs...)

	for i := range attrs {
		newAttrs = h.appendAttr(newAttrs, attrs[i])
	}

	nh := *h
	nh.attrs = newAttrs
	return &nh
}

// WithGroup implements slog.Handler.
//
// Nested groups produce dotted prefixes:
//
//	WithGroup("outer").WithGroup("inner")
//
// results in keys like:
//
//	"outer.inner.key"
func (h *SlogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	nh := *h
	if h.group != "" {
		nh.group = h.group + "." + name
	} else {
		nh.group = name
	}
	return &nh
}

// appendAttr converts one slog.Attr into zero or more loggerj Fields.
//
// Group attributes are flattened recursively into dotted keys.
func (h *SlogHandler) appendAttr(buf []Field, a slog.Attr) []Field {
	v := a.Value
	if v.Kind() == slog.KindLogValuer {
		v = v.Resolve()
	}

	// Empty attr key:
	// - scalar values are skipped
	// - groups are inlined
	if a.Key == "" {
		return appendSlogValue(buf, "", v)
	}

	key := a.Key
	if h.group != "" {
		key = h.group + "." + key
	}

	return appendSlogValue(buf, key, v)
}

// appendSlogValue appends typed loggerj fields for a slog.Value.
//
// slog.KindGroup is flattened recursively:
//
//	http.method
//	http.request.id
func appendSlogValue(buf []Field, key string, v slog.Value) []Field {
	if v.Kind() == slog.KindLogValuer {
		v = v.Resolve()
	}

	switch v.Kind() {
	case slog.KindGroup:
		attrs := v.Group()
		if len(attrs) == 0 {
			return buf
		}

		for i := range attrs {
			subKey := attrs[i].Key

			if key != "" {
				if subKey == "" {
					subKey = key
				} else {
					subKey = key + "." + subKey
				}
			}

			buf = appendSlogValue(buf, subKey, attrs[i].Value)
		}
		return buf
	}

	// Scalar values with empty keys are skipped.
	if key == "" {
		return buf
	}

	switch v.Kind() {
	case slog.KindString:
		return append(buf, Str(key, v.String()))

	case slog.KindInt64:
		return append(buf, Int64(key, v.Int64()))

	case slog.KindUint64:
		return append(buf, Uint64(key, v.Uint64()))

	case slog.KindFloat64:
		return append(buf, Float64(key, v.Float64()))

	case slog.KindBool:
		return append(buf, Bool(key, v.Bool()))

	case slog.KindDuration:
		return append(buf, Dur(key, v.Duration()))

	case slog.KindTime:
		// RFC3339Nano preserves sub-second precision for audit trails and
		// distributed tracing correlation. RFC3339 silently drops nanoseconds.
		return append(buf, Str(key, v.Time().Format(time.RFC3339Nano)))

	default:
		// KindAny and unknown kinds use string fallback.
		return append(buf, Str(key, v.String()))
	}
}

// -----------------------------------------------------------------------------
// Level Mapping (slog <-> loggerj)
// -----------------------------------------------------------------------------

// slogToLoggerjLevel maps a slog.Level to the nearest loggerj Level.
func slogToLoggerjLevel(l slog.Level) Level {
	switch {
	case l < slog.LevelInfo:
		return LevelDebug
	case l < slog.LevelWarn:
		return LevelInfo
	case l < slog.LevelError:
		return LevelWarn
	default:
		return LevelError
	}
}

// loggerjToSlogLevel maps a loggerj Level back to slog.Level.
func loggerjToSlogLevel(l Level) slog.Level {
	switch l {
	case LevelDebug:
		return slog.LevelDebug
	case LevelInfo:
		return slog.LevelInfo
	case LevelWarn:
		return slog.LevelWarn
	case LevelError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
