package loggerj

import (
	"encoding/json"
	"testing"
	"time"
	"unicode/utf8"
)

// FuzzAppendJSONString verifies appendJSONString escaping correctness for
// valid UTF-8 input.
//
// Invalid UTF-8 handling is intentionally not fuzzed in this first pass.
// Current appendJSONString assumes valid UTF-8 for non-control bytes.
// A follow-up correctness task should decide whether invalid UTF-8 must be
// replaced with U+FFFD or escaped in some other RFC 8259 compatible way.
func FuzzAppendJSONString(f *testing.F) {
	f.Add("hello")
	f.Add("quote \" backslash \\ newline \n tab \t")
	f.Add("control \x01 \x1f \x7f")
	f.Add("unicode: çğıöşü")

	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) {
			t.Skip("invalid UTF-8 is tracked separately")
		}

		out := appendJSONString(nil, s)

		var got string
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("invalid JSON string: %v\ninput=%q\noutput=%q", err, s, out)
		}

		if got != s {
			t.Fatalf("roundtrip mismatch\ninput=%q\ngot=%q", s, got)
		}
	})
}

// FuzzAppendDuration verifies appendDuration never panics and emits non-empty
// UTF-8 output for arbitrary duration values.
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

	f.Fuzz(func(t *testing.T, n int64) {
		out := appendDuration(nil, time.Duration(n))

		if len(out) == 0 {
			t.Fatalf("empty duration output")
		}

		if !utf8.Valid(out) {
			t.Fatalf("invalid UTF-8 duration output: %q", out)
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