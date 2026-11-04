package loggerj

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The lookup tables must encode exactly the rules they replaced.

func TestJSONSafeTable(t *testing.T) {
	for c := 0; c < 256; c++ {
		want := c >= 0x20 && c < 0x7F && c != '"' && c != '\\'
		if jsonSafe[c] != want {
			t.Errorf("jsonSafe[%#x] = %v, want %v", c, jsonSafe[c], want)
		}
	}
}

func TestTextSafeTable(t *testing.T) {
	for c := 0; c < 256; c++ {
		want := (c >= 0x20 && c != 0x7F) || c == '\t'
		if textSafe[c] != want {
			t.Errorf("textSafe[%#x] = %v, want %v", c, textSafe[c], want)
		}
	}
}

// Sync mode stamps each entry when it is logged, not per batch.
func TestSyncMode_TimestampAtCallTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ts.log")
	logger := NewLogger(Config{SyncMode: true, JSONOutput: true, OutputFile: path, DurabilityTier: Direct})

	before := time.Now().UnixMilli()
	logger.InfoString("TS", "sync stamp")
	after := time.Now().UnixMilli()
	logger.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		TS int64 `json:"ts"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, data)
	}
	if rec.TS < before || rec.TS > after {
		t.Errorf("ts %d outside [%d, %d]", rec.TS, before, after)
	}
}
