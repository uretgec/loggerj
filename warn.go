package loggerj

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

// warnInterval is the minimum time between two stderr messages of the same
// kind (write errors, rotation errors). A persistent failure, such as a full
// disk or a broken connection, would otherwise print on every flush, many
// times per second. The counters in Stats keep counting every failure.
const warnInterval = 10 * time.Second

// shouldWarn reports whether a message may be printed now, and records the
// time if so. last holds the monotonic time of the previous message (0: none).
func shouldWarn(last *atomic.Int64) bool {
	now := monoMillis()
	prev := last.Load()
	if prev != 0 && now-prev < warnInterval.Milliseconds() {
		return false
	}
	return last.CompareAndSwap(prev, now)
}

// warnLimited prints to stderr at most once per warnInterval for the limiter.
func warnLimited(last *atomic.Int64, format string, args ...any) {
	if shouldWarn(last) {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}
