package loggerj

import (
	"sync/atomic"
)

// -----------------------------------------------------------------------------
// SubProfile (Pre-Compiled Execution Profile)
// -----------------------------------------------------------------------------

// SubProfile represents a pre-compiled execution profile for a specific logType.
// Unlike traditional loggers that use mutexes and maps in the hot path,
// SubProfile holds lock-free atomic counters for rate limiting/sampling and
// pre-baked []byte prefixes for zero-CPU formatting.
type SubProfile struct {
	Name string

	// Pre-baked prefixes for zero-CPU formatting in the worker.
	textPrefix []byte // e.g., "module=HTTP env=prod "
	jsonPrefix []byte // e.g., ,"module":"HTTP","env":"prod"

	// Lock-Free Rate Limiting (single-word CAS)
	// rlState packs the profile-relative window index (upper 40 bits) and
	// the in-window count (lower 24 bits) into one atomic.Uint64.
	// Exact counting supports limits up to 16,777,215 (rlMaxExactLimit).
	rlLimit    int64         // Max logs per window (0 = unlimited)
	rlWindowMs int64         // Window size in milliseconds
	rlStartMs  int64         // Profile-relative epoch (set at RegisterSub)
	rlState    atomic.Uint64 // packed: (windowIdx << 24) | count

	// Lock-Free Sampling
	sampleRate  int64        // Log 1 out of N (0 = no sampling)
	sampleCount atomic.Int64 // Atomic counter

	// tempFields is used only during initialization to hold raw fields
	// before they are baked into textPrefix/jsonPrefix. It is set to nil
	// after registration to allow GC to reclaim the memory.
	tempFields []string
}

// SubOption configures a SubProfile during RegisterSub.
type SubOption func(*SubProfile)

// WithSampleRate enables statistical sampling for this logType.
//
// Only 1 out of every `rate` entries that reach the sampler is emitted. For
// example, WithSampleRate(10) emits roughly 10% of them (the 10th, 20th, ...
// call; the first nine are dropped). Useful for high-volume debug traces
// where statistical representation is sufficient.
//
// GATE ORDER (v1.5.0+): when a profile also has WithRateLimit, sampling runs
// BEFORE the rate limiter. The rate limiter therefore only sees (and counts)
// the sampled entries, so WithRateLimit caps what is actually written. See
// WithRateLimit for a worked example. Before v1.5.0 the rate limiter ran
// first.
//
// The sampling counter is atomic and lock-free, supporting concurrent logging.
func WithSampleRate(rate int64) SubOption {
	return func(p *SubProfile) {
		p.sampleRate = rate
	}
}

// WithFields adds static key-value pairs that will be pre-baked into the
// JSON/Text prefixes. This avoids formatting these fields in the hot path.
func WithFields(fields ...string) SubOption {
	return func(p *SubProfile) {
		p.tempFields = fields
	}
}

// -----------------------------------------------------------------------------
// Profile Registry (Copy-on-Write, Lock-Free Reads)
// -----------------------------------------------------------------------------

// profileRegistry holds an immutable snapshot of all registered profiles.
// Hot-path reads are lock-free via atomic.Pointer. Cold-path writes
// (RegisterSub) create a new copy and swap atomically.
//
// Lookup strategy (adaptive, benchmark-driven):
//   - n ≤ 8: linear scan over names[] (cache-friendly, ~2-3ns per compare).
//     For very small registries the map overhead (~5ns) exceeds the scan cost.
//   - n > 8: map[string]*SubProfile for O(1) lookup (~7.7ns vs ~20.5ns
//     for a 16-profile linear scan — measured on Apple M1 Pro).
//
// Threshold 8 is conservative; benchmark on your registry size if you
// register hundreds of profiles.
type profileRegistry struct {
	names    []string
	profiles []*SubProfile
	lookup   map[string]*SubProfile // nil when n ≤ 8; populated when n > 8
}
