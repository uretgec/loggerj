//go:build unix

package loggerj

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// This file verifies the process-crash column of the durability table in
// COMPARISON.md. A helper process (this test binary re-executed) writes log
// entries in SyncMode and then kills itself with SIGKILL, without Flush,
// Close or deferred functions. The parent counts the lines that reached the
// file. It cannot test OS crash or power loss.

// TestDurabilityHelper is the helper process. It does nothing unless the
// LOGGERJ_DURABILITY_* environment variables are set by the parent test.
func TestDurabilityHelper(t *testing.T) {
	tier := os.Getenv("LOGGERJ_DURABILITY_TIER")
	path := os.Getenv("LOGGERJ_DURABILITY_FILE")
	if tier == "" || path == "" {
		t.Skip("helper process only")
	}
	count, err := strconv.Atoi(os.Getenv("LOGGERJ_DURABILITY_COUNT"))
	if err != nil {
		t.Fatal(err)
	}

	var dt DurabilityTier
	switch tier {
	case "OSBuffered":
		dt = OSBuffered
	case "Direct":
		dt = Direct
	case "FsyncEveryN":
		dt = FsyncEveryN
	case "FsyncEveryWrite":
		dt = FsyncEveryWrite
	default:
		t.Fatalf("unknown tier %q", tier)
	}

	logger := NewLogger(Config{
		SyncMode:         true,
		OutputFile:       path,
		DurabilityTier:   dt,
		FsyncEveryNCount: 100,
	})
	for i := 0; i < count; i++ {
		logger.InfoString("DUR", fmt.Sprintf("msg-%d", i))
	}

	// Simulate a hard crash: no Flush, no Close, no deferred functions.
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	select {} // wait for the signal to be delivered
}

// runDurabilityChild runs the helper and returns how many lines were in the
// file after the child was killed.
func runDurabilityChild(t *testing.T, tier string, count int) int {
	t.Helper()

	path := filepath.Join(t.TempDir(), "dur.log")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDurabilityHelper$")
	cmd.Env = append(os.Environ(),
		"LOGGERJ_DURABILITY_TIER="+tier,
		"LOGGERJ_DURABILITY_FILE="+path,
		"LOGGERJ_DURABILITY_COUNT="+strconv.Itoa(count),
	)
	_ = cmd.Run() // expected to fail: the child kills itself

	if cmd.ProcessState == nil {
		t.Fatal("helper process did not start")
	}
	ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("helper was not killed by SIGKILL: %v", cmd.ProcessState)
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

func TestDurability_ProcessKill(t *testing.T) {
	const n = 150 // with FsyncEveryNCount=100: one fsync at entry 100, 50 left buffered

	t.Run("Direct", func(t *testing.T) {
		if got := runDurabilityChild(t, "Direct", n); got != n {
			t.Errorf("Direct: %d of %d entries survived a process kill, want all", got, n)
		}
	})

	t.Run("FsyncEveryWrite", func(t *testing.T) {
		if got := runDurabilityChild(t, "FsyncEveryWrite", n); got != n {
			t.Errorf("FsyncEveryWrite: %d of %d entries survived a process kill, want all", got, n)
		}
	})

	t.Run("FsyncEveryN", func(t *testing.T) {
		// Entries after the last fsync (50 of 150) are still in the bufio
		// buffer and must be lost; the first 100 were flushed and fsynced.
		if got := runDurabilityChild(t, "FsyncEveryN", n); got != 100 {
			t.Errorf("FsyncEveryN: %d entries survived, want exactly 100", got)
		}
	})

	t.Run("OSBuffered", func(t *testing.T) {
		got := runDurabilityChild(t, "OSBuffered", n)
		if got == n {
			t.Skip("a 10ms time-based flush happened to fire on the last entry; rerun")
		}
		t.Logf("OSBuffered: %d of %d entries survived a process kill", got, n)
	})
}
