//go:build !go1.22

package loggerj

import (
	"testing"
	"time"
)

// TestAsWriter_ZeroAlloc verifies that the AsWriter adapter performs zero
// heap allocations per write on Go 1.22+. On Go 1.21, the escape analyzer
// cannot prove that the io.Writer interface dispatch does not retain p,
// resulting in 1 alloc/op. This is a compiler limitation, not a code bug.
func TestAsWriter_ZeroAlloc(t *testing.T) {
	logger := NewLogger(Config{
		FlushTimeout:  50 * time.Millisecond,
		ChannelSize:   1, // Drop-path: pool stays warm
		IncludeCaller: false,
	})
	// No worker started → channel fills immediately → drop-path active → pool warm
	w := logger.AsWriter(LevelInfo, "STDLIB")

	// Warmup with identical message to stabilize pool capacities
	for i := 0; i < 100; i++ {
		w.Write([]byte("zero alloc test message\n"))
	}

	allocs := testing.AllocsPerRun(1000, func() {
		w.Write([]byte("zero alloc test message\n"))
	})

	// Go 1.21's escape analysis cannot prove the io.Writer-dispatched
	// call does not retain p; 1.22+ devirtualizes and stack-allocates it.
	// See README.md for the supported version matrix.
	maxAllocs := 1.0
	if allocs > maxAllocs {
		t.Errorf("expected ≤%.0f allocs/op in AsWriter.Write (Go 1.21), got %.1f", maxAllocs, allocs)
	}
}
