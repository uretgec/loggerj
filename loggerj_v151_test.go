package loggerj

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
