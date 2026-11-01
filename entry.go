package loggerj

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

	// clear() drops the string references held by the pooled backing arrays
	// so that a large field value is not kept alive until the Entry is reused.
	if cap(e.Fields) > 64 {
		e.Fields = nil
	} else {
		clear(e.Fields)
		e.Fields = e.Fields[:0]
	}

	if cap(e.FieldsV) > 64 {
		e.FieldsV = nil
	} else {
		clear(e.FieldsV)
		e.FieldsV = e.FieldsV[:0]
	}
}
