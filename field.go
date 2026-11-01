package loggerj

import (
	"math"
	"time"
)

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

// floatToBits converts float64 to uint64 bits using math.Float64bits.
// The Go compiler optimizes this to a single intrinsic CPU instruction,
// eliminating the need for unsafe.Pointer type punning while maintaining
// identical hot-path performance.
func floatToBits(f float64) uint64 {
	return math.Float64bits(f)
}
