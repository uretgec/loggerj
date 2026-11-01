package loggerj

import (
	"runtime"
	"time"
)

// -----------------------------------------------------------------------------
// Lock-Free Rate Limit State Layout
// -----------------------------------------------------------------------------
//
// The rate-limit state is packed into one atomic.Uint64:
//
//   - lower 24 bits: in-window counter
//   - upper 40 bits: profile-relative window index
//
// This layout supports exact limits up to 2^24-1 (16,777,215).
// Larger limits are capped.
//
// The window index is computed relative to the profile's start time instead
// of the Unix epoch. This avoids premature window-index overflow for very
// small windows such as 1ms.
const (
	rlCountBits = 24
	rlCountMask = (uint64(1) << rlCountBits) - 1

	// rlMaxExactLimit is the maximum exact per-window count representable
	// by the packed rate-limit state.
	rlMaxExactLimit = int64(rlCountMask)

	// rlMaxWindow is the maximum profile-relative window index.
	rlMaxWindow = (uint64(1) << (64 - rlCountBits)) - 1
)

// monoEpoch is the origin of the rate limiter's clock. It carries a monotonic
// reading, so time.Since(monoEpoch) reads only the monotonic clock: it is
// unaffected by wall-clock adjustments (NTP steps, manual changes) and does
// not need the second clock read that time.Now() performs.
var monoEpoch = time.Now()

// monoMillis returns milliseconds elapsed on the monotonic clock since
// monoEpoch, plus one so that the result is never 0 (SubProfile.rlStartMs uses
// 0 for "unset").
func monoMillis() int64 {
	return int64(time.Since(monoEpoch)/time.Millisecond) + 1
}

// rateLimitFallbackEpochMs is used only when a rate-limited profile does
// not have an explicit rlStartMs. Normal profiles registered via
// WithRateLimit receive a start time at registration.
var rateLimitFallbackEpochMs = int64(1)

// WithRateLimit sets a lock-free rate limit for this specific logType.
//
// limit is the max logs per window. window is the duration, for example
// time.Second or 500 * time.Millisecond. Sub-second windows are supported.
// The window is a fixed window that starts when the profile is registered,
// so up to 2*limit entries can pass around a window boundary.
//
// GATE ORDER (v1.5.0+): rate limiting is applied AFTER sampling, so the limit
// caps what is actually written. With both WithRateLimit(100, time.Second)
// and WithSampleRate(10):
//
//  1. Input: 500 logs/s
//  2. Sampling: 1 out of 10 is kept, 50 logs/s
//  3. Rate limit: at most 100 logs per second are admitted
//     Result: min(500/10, 100) = 50 logs/s are written.
//
// Entries discarded by sampling do not use up the rate limit, and they do not
// pay for the rate limiter's clock read. Before v1.5.0 the rate
// limiter ran first: the limit counted entries before sampling and the
// example above wrote ~10 logs/s.
//
// Exact counting supports limits up to rlMaxExactLimit (16,777,215).
// Larger limits are capped to that value.
func WithRateLimit(limit int64, window time.Duration) SubOption {
	return func(p *SubProfile) {
		if limit < 0 {
			limit = 0
		}
		if limit > rlMaxExactLimit {
			limit = rlMaxExactLimit
		}

		p.rlLimit = limit
		p.rlWindowMs = window.Milliseconds()

		if p.rlWindowMs < 1 {
			p.rlWindowMs = 1
		}

		p.rlStartMs = monoMillis()
	}
}

// checkAtomicRateLimit performs lock-free rate limiting using the current
// wall-clock time. It is a thin wrapper around the deterministic
// checkAtomicRateLimitAt function.
func (l *Logger) checkAtomicRateLimit(p *SubProfile) bool {
	return checkAtomicRateLimitAt(p, monoMillis())
}

// casBackoffThreshold is the number of consecutive failed CAS attempts
// before yielding the processor. This prevents unbounded CPU spin under
// extreme rate-limit contention while keeping the uncontended hot path
// completely zero-cost.
const casBackoffThreshold = 8

// checkAtomicRateLimitAt performs deterministic lock-free rate limiting.
//
// State layout:
//
//   - lower 24 bits: counter inside the current window
//   - upper 40 bits: profile-relative window index
//
// The maximum exact limit is rlCountMask. Higher limits are capped.
//
// Over-limit calls return false without performing a CAS. This reduces
// CPU spin under heavy rate-limit saturation.
// Under extreme CAS contention, the loop yields via runtime.Gosched()
// after casBackoffThreshold failures to prevent CPU burning.
func checkAtomicRateLimitAt(p *SubProfile, nowMs int64) bool {
	if p.rlLimit <= 0 {
		return true
	}

	start := p.rlStartMs
	if start <= 0 {
		start = rateLimitFallbackEpochMs
	}

	elapsed := nowMs - start
	if elapsed < 0 {
		elapsed = 0
	}

	windowMs := p.rlWindowMs
	if windowMs <= 0 {
		windowMs = 1000
	}

	nowWin := uint64(elapsed) / uint64(windowMs)
	if nowWin > rlMaxWindow {
		nowWin = rlMaxWindow
	}

	limit := uint64(p.rlLimit)
	if limit > rlCountMask {
		limit = rlCountMask
	}

	attempts := 0
	for {
		state := p.rlState.Load()
		win := state >> rlCountBits
		cnt := state & rlCountMask

		var next uint64

		if win < nowWin {
			// Our timestamp opens a newer window: reset the counter.
			next = (nowWin << rlCountBits) | 1
		} else {
			// win >= nowWin. win > nowWin means this caller read its clock
			// before another goroutine advanced the window; count the call
			// against the newer window instead of moving the state back
			// (which would let up to `limit` extra entries through).
			if cnt >= limit {
				return false
			}
			next = state + 1
		}

		if p.rlState.CompareAndSwap(state, next) {
			return true
		}

		// CAS failed: another goroutine modified the state.
		// Yield after N failures to prevent unbounded CPU spin.
		attempts++
		if attempts >= casBackoffThreshold {
			runtime.Gosched()
			attempts = 0
		}
	}
}
