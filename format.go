package loggerj

import (
	"math"
	"strconv"
	"time"
	"unicode/utf8"
)

// -----------------------------------------------------------------------------
// Formatting
// -----------------------------------------------------------------------------

// workerBatchMax bounds how many already-queued entries the worker formats
// with a single clock read and without going back through its multi-channel
// select. It also bounds how long ctx cancellation, the flush ticker and
// Flush() requests can be delayed by a saturating producer.
const workerBatchMax = 128

// formatEntry formats a log entry into the buffer, stamping it with the
// current time. Sync mode uses it, so sync entries are stamped at call time.
func (l *Logger) formatEntry(buf []byte, e *Entry) []byte {
	var tb [48]byte
	return l.formatEntryAt(buf, e, l.renderTimestamp(tb[:0], time.Now().UnixMilli()))
}

// formatEntryAt formats a log entry using an already rendered timestamp (see
// renderTimestamp), so a batch renders the timestamp once.
func (l *Logger) formatEntryAt(buf []byte, e *Entry, ts []byte) []byte {
	if l.cfg.JSONOutput {
		return l.formatJSON(buf, e, ts)
	}
	return l.formatText(buf, e, ts)
}

// formatBatch formats first and then, without blocking, up to
// workerBatchMax-1 entries that are already queued, reading the clock once
// for the whole batch. It stops early when buf reaches FlushThreshold so the
// caller can write the buffer out; entries not taken stay in the channel.
//
// Timestamps have millisecond resolution and a batch is formatted within
// microseconds, so a shared stamp changes little in practice, but the
// entries of one batch do carry the same ts. Every entry is recycled to the
// pool here.
func (l *Logger) formatBatch(buf []byte, first *Entry) []byte {
	var tb [48]byte
	ts := l.renderTimestamp(tb[:0], time.Now().UnixMilli())
	buf = l.formatEntryAt(buf, first, ts)
	first.Reset()
	l.pool.Put(first)

	for n := 1; n < workerBatchMax && len(buf) < l.cfg.FlushThreshold; n++ {
		select {
		case e := <-l.logCh:
			buf = l.formatEntryAt(buf, e, ts)
			e.Reset()
			l.pool.Put(e)
		default:
			return buf
		}
	}
	return buf
}

// renderTimestamp appends the text of the entry timestamp for ts (Unix
// milliseconds): the number by default, or the UTC time in
// Config.TimestampFormat. JSON output adds the quotes around a formatted time.
func (l *Logger) renderTimestamp(dst []byte, ts int64) []byte {
	if l.cfg.TimestampFormat == "" {
		return strconv.AppendInt(dst, ts, 10)
	}
	return time.UnixMilli(ts).UTC().AppendFormat(dst, l.cfg.TimestampFormat)
}

// formatText formats a log entry as human-readable text:
//
//	[1704067200123] INFO [HTTP] request received method=GET path=/api
func (l *Logger) formatText(buf []byte, e *Entry, ts []byte) []byte {
	buf = append(buf, '[')
	buf = append(buf, ts...)
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

	buf = appendTextBytes(buf, e.Msg)

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
func (l *Logger) formatJSON(buf []byte, e *Entry, ts []byte) []byte {
	buf = append(buf, `{"ts":`...)
	if l.cfg.TimestampFormat != "" {
		buf = append(buf, '"')
		buf = append(buf, ts...)
		buf = append(buf, '"')
	} else {
		buf = append(buf, ts...)
	}
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
		buf = appendTypedFieldsJSONUnit(buf, e.FieldsV, l.cfg.DurationUnit)
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

// appendTextBytes appends s with control characters escaped, so that a message
// or value cannot start a new log line (log forging): \n and \r become the two
// characters "\n" and "\r", other control characters (and DEL) become \xNN.
// A tab is kept. Keys are trusted and not escaped.
func appendTextBytes(buf []byte, s []byte) []byte {
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 0x20 && c != 0x7F) || c == '\t' {
			continue
		}
		if start < i {
			buf = append(buf, s[start:i]...)
		}
		switch c {
		case '\n':
			buf = append(buf, '\\', 'n')
		case '\r':
			buf = append(buf, '\\', 'r')
		default:
			buf = append(buf, '\\', 'x',
				"0123456789abcdef"[c>>4],
				"0123456789abcdef"[c&0xf],
			)
		}
		start = i + 1
	}
	if start < len(s) {
		buf = append(buf, s[start:]...)
	}
	return buf
}

// appendTextString is appendTextBytes for a string (zero-copy conversion).
func appendTextString(buf []byte, s string) []byte {
	return appendTextBytes(buf, unsafeStringToBytes(s))
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
			buf = appendTextString(buf, fields[i+1])
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

// appendJSONString appends a JSON-escaped string (with surrounding quotes)
// to buf. Control characters (< 0x20), DEL (0x7F), double quotes, and
// backslashes are escaped per RFC 8259. Invalid UTF-8 bytes are replaced by
// the escape \ufffd (one per invalid byte, like encoding/json), so the output
// is always valid JSON.
func appendJSONString(buf []byte, s string) []byte {
	// The conversion is zero-copy and read-only; appendJSONStringBytes never
	// retains or mutates its input.
	return appendJSONStringBytes(buf, unsafeStringToBytes(s))
}

// appendJSONStringBytes is the byte-slice form of appendJSONString and holds
// the single implementation of the escaping rules.
func appendJSONStringBytes(buf []byte, s []byte) []byte {
	buf = append(buf, '"')

	start := 0
	for i := 0; i < len(s); {
		c := s[i]

		if c < utf8.RuneSelf {
			if c >= 0x20 && c != 0x7F && c != '"' && c != '\\' {
				i++
				continue
			}

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
					"0123456789abcdef"[c&0xf],
				)
			}

			i++
			start = i
			continue
		}

		// Multi-byte sequence: keep valid UTF-8 as is, replace invalid bytes.
		r, size := utf8.DecodeRune(s[i:])
		if r == utf8.RuneError && size == 1 {
			if start < i {
				buf = append(buf, s[start:i]...)
			}
			buf = append(buf, `\ufffd`...)
			i += size
			start = i
			continue
		}
		i += size
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
	return appendTypedFieldsJSONUnit(buf, fields, 0)
}

// appendTypedFieldsJSONUnit is appendTypedFieldsJSON with the duration
// encoding of Config.DurationUnit (0 means the string form).
func appendTypedFieldsJSONUnit(buf []byte, fields []Field, unit time.Duration) []byte {
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
		buf = appendFieldValueJSON(buf, f, unit)
	}
	return buf
}

// appendFieldValueJSON appends the typed value of a single Field as JSON.
func appendFieldValueJSON(buf []byte, f *Field, unit time.Duration) []byte {
	switch f.Type {
	case StringType:
		buf = appendJSONString(buf, f.Str)
	case Int64Type:
		buf = strconv.AppendInt(buf, int64(f.Num), 10)
	case Uint64Type:
		buf = strconv.AppendUint(buf, f.Num, 10)
	case Float64Type:
		// JSON has no NaN/Inf literals; emit them as strings (same as zap
		// and zerolog) so the line stays valid JSON.
		fv := float64FromBits(f.Num)
		switch {
		case math.IsNaN(fv):
			buf = append(buf, `"NaN"`...)
		case math.IsInf(fv, 1):
			buf = append(buf, `"+Inf"`...)
		case math.IsInf(fv, -1):
			buf = append(buf, `"-Inf"`...)
		default:
			buf = strconv.AppendFloat(buf, fv, 'g', -1, 64)
		}
	case BoolType:
		if f.Num == 1 {
			buf = append(buf, "true"...)
		} else {
			buf = append(buf, "false"...)
		}
	case DurationType:
		if unit > 0 {
			buf = appendDurationNumber(buf, time.Duration(f.Num), unit)
		} else {
			// Render as string for readability: "150ms", "2.005s"
			buf = append(buf, '"')
			buf = appendDuration(buf, time.Duration(f.Num))
			buf = append(buf, '"')
		}
	case ErrorType:
		buf = appendJSONString(buf, f.Str)
	default:
		buf = append(buf, "null"...)
	}
	return buf
}

// appendTypedFieldsText appends typed fields in text format: key=val key2=val2
func appendTypedFieldsText(buf []byte, fields []Field) []byte {
	first := true
	for i := range fields {
		f := &fields[i]
		if f.Key == "" && f.Type == StringType && f.Str == "" {
			continue // zero-value Field — skip
		}
		if !first {
			buf = append(buf, ' ')
		}
		first = false
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
		buf = appendTextString(buf, f.Str)
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
		buf = appendTextString(buf, f.Str)
	default:
		buf = append(buf, "null"...)
	}
	return buf
}

// appendDurationNumber writes d as a JSON number expressed in unit (unit > 0):
// an integer when d is a whole multiple of unit, a decimal otherwise.
func appendDurationNumber(buf []byte, d, unit time.Duration) []byte {
	if d%unit == 0 {
		return strconv.AppendInt(buf, int64(d/unit), 10)
	}
	return strconv.AppendFloat(buf, float64(d)/float64(unit), 'f', -1, 64)
}

// appendDuration formats a duration in human-readable form without
// allocation (Go's time.Duration.String() allocates). Output is parseable by
// time.ParseDuration. Precision: nanoseconds below 1µs, microseconds below
// 1ms, milliseconds below 1 minute, whole seconds from 1 minute up; the
// remainder is truncated, never rounded. Examples: "150ms", "2.005s",
// "2m30s", "1h30m", "-1.500s".
func appendDuration(buf []byte, d time.Duration) []byte {
	if d < 0 {
		buf = append(buf, '-')
		// Negate through uint64 so that math.MinInt64 does not overflow.
		return appendDurationAbs(buf, uint64(-(d+1))+1)
	}
	return appendDurationAbs(buf, uint64(d))
}

// appendDurationAbs formats a non-negative duration given in nanoseconds.
func appendDurationAbs(buf []byte, ns uint64) []byte {
	switch {
	case ns == 0:
		return append(buf, "0s"...)
	case ns < uint64(time.Microsecond):
		buf = strconv.AppendUint(buf, ns, 10)
		return append(buf, "ns"...)
	case ns < uint64(time.Millisecond):
		buf = strconv.AppendUint(buf, ns/uint64(time.Microsecond), 10)
		return append(buf, "\u00b5s"...)
	case ns < uint64(time.Second):
		buf = strconv.AppendUint(buf, ns/uint64(time.Millisecond), 10)
		return append(buf, "ms"...)
	case ns < uint64(time.Minute):
		sec := ns / uint64(time.Second)
		ms := (ns % uint64(time.Second)) / uint64(time.Millisecond)
		buf = strconv.AppendUint(buf, sec, 10)
		if ms > 0 {
			// Zero-padded to 3 digits: 2005ms is "2.005s", not "2.5s".
			buf = append(buf, '.',
				byte('0'+ms/100),
				byte('0'+(ms/10)%10),
				byte('0'+ms%10),
			)
		}
		return append(buf, 's')
	}

	// >= 1 minute: "2m30s", "1h30m", "100h". Zero components are omitted.
	hours := ns / uint64(time.Hour)
	mins := (ns % uint64(time.Hour)) / uint64(time.Minute)
	secs := (ns % uint64(time.Minute)) / uint64(time.Second)
	if hours > 0 {
		buf = strconv.AppendUint(buf, hours, 10)
		buf = append(buf, 'h')
	}
	if mins > 0 {
		buf = strconv.AppendUint(buf, mins, 10)
		buf = append(buf, 'm')
	}
	if secs > 0 {
		buf = strconv.AppendUint(buf, secs, 10)
		buf = append(buf, 's')
	}
	return buf
}

// float64FromBits converts uint64 bits back to float64 using math.Float64frombits.
// The Go compiler optimizes this to a single intrinsic CPU instruction.
func float64FromBits(b uint64) float64 {
	return math.Float64frombits(b)
}
