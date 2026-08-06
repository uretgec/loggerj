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

// SlogHandler implements slog.Handler, routing all slog calls through
// loggerj's zero-allocation typed-field pipeline. This lets applications
// adopted to the log/slog standard benefit from loggerj's async throughput
// (or sync durability) without changing their call sites.
//
// Design notes:
//   - slog.Attr → loggerj.Field conversion is boxing-free: slog.Value is
//     already a tagged union, so we map Kind → FieldType directly.
//   - WithAttrs pre-converts attributes into Fields once (cold path);
//     the hot-path Handle() only converts the per-record attributes.
//   - WithGroup flattens nested groups into dotted keys (e.g., "g.b").
//     loggerj's Field API does not support nested objects; flattening
//     preserves the data at the cost of JSON nesting depth.
//   - Field buffers are pooled, keeping Handle() allocation-free once warm.
type SlogHandler struct {
	logger  *Logger
	logType string

	// attrs holds pre-converted WithAttrs fields. Populated once at
	// WithAttrs time (cold path); copied by value into every Handle call.
	attrs []Field

	// group is the current WithGroup prefix ("" = no group). Group names
	// are dotted together for nested groups (e.g., "outer.inner").
	group string

	// fieldPool amortizes the per-Handle []Field slice allocation.
	// Shared across all handlers derived from the same root logger.
	fieldPool *sync.Pool
}

// slogFieldPool is a package-level pool of []Field slices used by
// SlogHandler.Handle. Shared across all SlogHandler instances to
// maximize buffer reuse under concurrent slog traffic.
var slogFieldPool = sync.Pool{
	New: func() any {
		buf := make([]Field, 0, 32)
		return &buf
	},
}

// NewSlogHandler returns a slog.Handler that routes all records into the
// given Logger under the specified logType. Use with slog.New:
//
//	logger := loggerj.NewLogger(loggerj.Config{JSONOutput: true})
//	go logger.Start(ctx)
//	slog.SetDefault(slog.New(loggerj.NewSlogHandler(logger, "APP")))
//	slog.Info("request", "method", "GET", "status", 200)
//
// The handler respects the logger's current level (SetLevelValue) and
// inherits its durability/async mode.
func NewSlogHandler(l *Logger, logType string) *SlogHandler {
	return &SlogHandler{
		logger:    l,
		logType:   logType,
		fieldPool: &slogFieldPool,
	}
}

// Enabled implements slog.Handler. Reports whether the handler would
// process a record at the given level. Delegates to loggerj's atomic
// level check (~2ns) so SetLevelValue takes effect immediately.
func (h *SlogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= loggerjToSlogLevel(h.logger.GetLevel())
}

// Handle implements slog.Handler. Converts the record's attributes to
// loggerj Fields (boxing-free) and dispatches through logTyped.
// Pre-attributes from WithAttrs are prepended; group prefixes from
// WithGroup are applied as dotted keys.
func (h *SlogHandler) Handle(ctx context.Context, r slog.Record) error {
	level := slogToLoggerjLevel(r.Level)

	// Acquire a pooled Field buffer: cap covers pre-attrs + record attrs.
	bp := h.fieldPool.Get().(*[]Field)
	fields := (*bp)[:0]

	// Prepend pre-converted WithAttrs fields.
	fields = append(fields, h.attrs...)

	// Convert per-record attributes (slog.Attr → loggerj.Field).
	r.Attrs(func(a slog.Attr) bool {
		fields = append(fields, h.convertAttr(a))
		return true
	})

	// Dispatch through the typed-field hot path (zero-alloc after warmup).
	// skip=4: Handle → slog dispatch → slog.Info/Warn/etc → caller.
	h.logger.logTyped(level, h.logType, []byte(r.Message), 4, fields...)

	// Return buffer to pool. Release oversized buffers to GC to avoid
	// permanent retention of rare large-attribute records.
	if cap(fields) > 256 {
		*bp = make([]Field, 0, 32)
	} else {
		*bp = fields
	}
	h.fieldPool.Put(bp)
	return nil
}

// WithAttrs implements slog.Handler. Returns a new handler whose records
// include the given attributes. Attributes are converted to Fields once
// here (cold path) and prepended on every Handle call.
func (h *SlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	// Pre-convert into a new slice (copy-on-write; parent handler unchanged).
	newAttrs := make([]Field, 0, len(h.attrs)+len(attrs))
	newAttrs = append(newAttrs, h.attrs...)
	for i := range attrs {
		newAttrs = append(newAttrs, h.convertAttr(attrs[i]))
	}
	nh := *h
	nh.attrs = newAttrs
	return &nh
}

// WithGroup implements slog.Handler. Returns a new handler that prefixes
// subsequent attribute keys with the group name. Nested groups produce
// dotted keys (e.g., "outer.inner.key"). Empty group names are ignored
// per the slog.Handler contract.
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

// convertAttr maps a slog.Attr to a loggerj.Field without boxing.
// Applies the current group prefix as a dotted key. Handles all slog
// Value kinds; Group values are flattened with dot notation; LogValuer
// is resolved before conversion.
func (h *SlogHandler) convertAttr(a slog.Attr) Field {
	key := a.Key
	if h.group != "" {
		key = h.group + "." + key
	}

	v := a.Value
	// Resolve LogValuer (e.g., custom types implementing LogValue).
	if v.Kind() == slog.KindLogValuer {
		v = v.Resolve()
	}

	switch v.Kind() {
	case slog.KindString:
		return Str(key, v.String())
	case slog.KindInt64:
		return Int64(key, v.Int64())
	case slog.KindUint64:
		return Uint64(key, v.Uint64())
	case slog.KindFloat64:
		return Float64(key, v.Float64())
	case slog.KindBool:
		return Bool(key, v.Bool())
	case slog.KindDuration:
		return Dur(key, v.Duration())
	case slog.KindTime:
		// Render times as RFC3339 strings for stable JSON output.
		return Str(key, v.Time().Format(time.RFC3339))
	case slog.KindGroup:
		// Flatten nested groups into dotted keys. Nested objects are not
		// supported by the Field API; flattening preserves the data.
		attrs := v.Group()
		// Emit each sub-attr with a dotted key. Since Field is a single
		// key-value, we collapse the group to its string form when it
		// cannot be represented as one field.
		if len(attrs) == 1 {
			sub := attrs[0]
			return Field{
				Key:  key + "." + sub.Key,
				Type: fieldKindOf(sub.Value),
				Num:  fieldNumOf(sub.Value),
				Str:  fieldStrOf(sub.Value),
			}
		}
		// Multi-key group: fall back to string representation.
		return Str(key, v.String())
	default:
		// KindAny and unknown kinds: string fallback.
		return Str(key, v.String())
	}
}

// fieldKindOf maps a slog.Value kind to a loggerj FieldType (group helper).
func fieldKindOf(v slog.Value) FieldType {
	switch v.Kind() {
	case slog.KindString:
		return StringType
	case slog.KindInt64:
		return Int64Type
	case slog.KindUint64:
		return Uint64Type
	case slog.KindFloat64:
		return Float64Type
	case slog.KindBool:
		return BoolType
	case slog.KindDuration:
		return DurationType
	default:
		return StringType
	}
}

// fieldNumOf extracts the numeric payload for a slog.Value (group helper).
func fieldNumOf(v slog.Value) uint64 {
	switch v.Kind() {
	case slog.KindInt64:
		return uint64(v.Int64())
	case slog.KindUint64:
		return v.Uint64()
	case slog.KindFloat64:
		return floatToBits(v.Float64())
	case slog.KindBool:
		if v.Bool() {
			return 1
		}
		return 0
	case slog.KindDuration:
		return uint64(v.Duration())
	default:
		return 0
	}
}

// fieldStrOf extracts the string payload for a slog.Value (group helper).
func fieldStrOf(v slog.Value) string {
	switch v.Kind() {
	case slog.KindString:
		return v.String()
	case slog.KindTime:
		return v.Time().Format(time.RFC3339)
	default:
		return v.String()
	}
}

// -----------------------------------------------------------------------------
// Level Mapping (slog ↔ loggerj)
// -----------------------------------------------------------------------------

// slogToLoggerjLevel maps a slog.Level to the nearest loggerj Level.
// slog levels are spaced by 4 (Debug=-4, Info=0, Warn=4, Error=8) and
// support custom intermediate levels; we map to the nearest bucket.
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

// loggerjToSlogLevel maps a loggerj Level back to slog.Level for the
// Enabled() comparison.
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