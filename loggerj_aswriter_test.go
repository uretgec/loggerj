//go:build go1.22

package loggerj

import (
	"testing"
	"time"
)

// TestAsWriter_ZeroAlloc verifies that the AsWriter adapter performs zero
// heap allocations per write. The old implementation used strings.TrimRight
// which allocated via string(p); the new implementation uses bytes.TrimRight
// which operates directly on the []byte input.
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

	// Verified by benchmark: 0 allocs/op on Go 1.22+
	if allocs > 0 {
		t.Errorf("expected 0 allocs/op in AsWriter.Write, got %.1f", allocs)
	}
}