package loggerj

import (
	"encoding/json"
	"math"
	"testing"
	"time"
	"unicode/utf8"
)

// FuzzAppendJSONString verifies appendJSONString always emits valid JSON that
// round-trips through encoding/json. Invalid UTF-8 bytes are replaced by
// U+FFFD (one per invalid byte), exactly like encoding/json does.
func FuzzAppendJSONString(f *testing.F) {
	f.Add("hello")
	f.Add("quote \" backslash \\ newline \n tab \t")
	f.Add("control \x01 \x1f \x7f")
	f.Add("unicode: çğıöşü")
	f.Add("invalid: \xff\xfe \xc3")

	f.Fuzz(func(t *testing.T, s string) {
		out := appendJSONString(nil, s)

		var got string
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("invalid JSON string: %v\ninput=%q\noutput=%q", err, s, out)
		}

		// Converting to []rune replaces each invalid byte with U+FFFD.
		if want := string([]rune(s)); got != want {
			t.Fatalf("roundtrip mismatch\ninput=%q\ngot=%q\nwant=%q", s, got, want)
		}
	})
}

// absDuration returns |d| in nanoseconds without overflowing on MinInt64.
func absDuration(d time.Duration) uint64 {
	if d < 0 {
		return uint64(-(d + 1)) + 1
	}
	return uint64(d)
}

// durationTruncation returns the exclusive upper bound of the error that
// appendDuration may introduce for a magnitude given in nanoseconds.
func durationTruncation(ns uint64) uint64 {
	switch {
	case ns < uint64(time.Microsecond):
		return 1 // exact
	case ns < uint64(time.Millisecond):
		return uint64(time.Microsecond)
	case ns < uint64(time.Minute):
		return uint64(time.Millisecond)
	default:
		return uint64(time.Second)
	}
}

// FuzzAppendDuration verifies appendDuration emits output that
// time.ParseDuration accepts and that differs from the input only by the
// documented truncation.
func FuzzAppendDuration(f *testing.F) {
	f.Add(int64(0))
	f.Add(int64(1))
	f.Add(int64(-1))
	f.Add(int64(time.Nanosecond))
	f.Add(int64(time.Microsecond))
	f.Add(int64(time.Millisecond))
	f.Add(int64(time.Second))
	f.Add(int64(time.Minute))
	f.Add(int64(time.Hour))
	f.Add(int64(2005 * time.Millisecond))
	f.Add(int64(100 * time.Hour))
	f.Add(int64(math.MinInt64))
	f.Add(int64(math.MaxInt64))

	f.Fuzz(func(t *testing.T, n int64) {
		d := time.Duration(n)
		out := appendDuration(nil, d)

		if len(out) == 0 {
			t.Fatalf("empty duration output")
		}
		if !utf8.Valid(out) {
			t.Fatalf("invalid UTF-8 duration output: %q", out)
		}

		parsed, err := time.ParseDuration(string(out))
		if err != nil {
			t.Fatalf("unparsable duration %q for %d: %v", out, n, err)
		}

		want, got := absDuration(d), absDuration(parsed)
		if got > want {
			t.Fatalf("%q parses to a larger magnitude than input %d", out, n)
		}
		if parsed != 0 && (d < 0) != (parsed < 0) {
			t.Fatalf("%q has the wrong sign for input %d", out, n)
		}
		if want-got >= durationTruncation(want) {
			t.Fatalf("%q loses too much precision for input %d", out, n)
		}
	})
}

// FuzzRateLimitWindow verifies deterministic rate-limit window behavior.
//
// Properties checked:
//
//  1. Within a fixed timestamp, at most limit calls pass.
//  2. In the next window, at least one call passes.
func FuzzRateLimitWindow(f *testing.F) {
	f.Add(uint8(10), uint16(1000), uint32(1000))
	f.Add(uint8(1), uint16(1), uint32(0))
	f.Add(uint8(255), uint16(65535), uint32(4_000_000_000))

	f.Fuzz(func(t *testing.T, limit8 uint8, window16 uint16, offset uint32) {
		limit := int64(limit8) + 1
		window := int64(window16) + 1

		p := &SubProfile{
			rlLimit:    limit,
			rlWindowMs: window,
			rlStartMs:  1,
		}

		now := p.rlStartMs + int64(offset)

		passed := int64(0)
		attempts := limit + 2

		for i := int64(0); i < attempts; i++ {
			if checkAtomicRateLimitAt(p, now) {
				passed++
			}
		}

		if passed > limit {
			t.Fatalf("rate limit exceeded: limit=%d passed=%d", limit, passed)
		}

		nextWindow := now + window
		if !checkAtomicRateLimitAt(p, nextWindow) {
			t.Fatalf("next window should allow at least one call: limit=%d window=%d", limit, window)
		}
	})
}
