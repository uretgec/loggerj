package loggerj

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// -----------------------------------------------------------------------------
// Config.DurationUnit
// -----------------------------------------------------------------------------

func TestDurationUnit_JSON(t *testing.T) {
	tests := []struct {
		unit time.Duration
		d    time.Duration
		want string
	}{
		{time.Millisecond, 150 * time.Millisecond, `"d":150`},
		{time.Millisecond, 1500 * time.Microsecond, `"d":1.5`},
		{time.Millisecond, 0, `"d":0`},
		{time.Second, 2500 * time.Millisecond, `"d":2.5`},
		{time.Second, 3 * time.Second, `"d":3`},
		{time.Millisecond, -2 * time.Millisecond, `"d":-2`},
	}
	for _, tt := range tests {
		logger, buf, _ := setupTestLogger(t, Config{
			JSONOutput:   true,
			DurationUnit: tt.unit,
			FlushTimeout: 10 * time.Millisecond,
			ChannelSize:  100,
		})
		logger.InfoFields("DUR", []byte("m"), Dur("d", tt.d))
		output := flushAndRead(t, logger, buf)

		if !strings.Contains(output, tt.want) {
			t.Errorf("unit=%v d=%v: want %s in %s", tt.unit, tt.d, tt.want, output)
		}
		var rec map[string]interface{}
		if err := json.Unmarshal([]byte(output), &rec); err != nil {
			t.Errorf("invalid JSON: %v\n%s", err, output)
		}
	}
}

func TestDurationUnit_DefaultAndText(t *testing.T) {
	// Default JSON keeps the string form.
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:   true,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})
	logger.InfoFields("DUR", []byte("m"), Dur("d", 150*time.Millisecond))
	if out := flushAndRead(t, logger, buf); !strings.Contains(out, `"d":"150ms"`) {
		t.Errorf("default JSON duration changed: %s", out)
	}

	// Text output ignores DurationUnit.
	logger2, buf2, _ := setupTestLogger(t, Config{
		DurationUnit: time.Millisecond,
		FlushTimeout: 10 * time.Millisecond,
		ChannelSize:  100,
	})
	logger2.InfoFields("DUR", []byte("m"), Dur("d", 150*time.Millisecond))
	if out := flushAndRead(t, logger2, buf2); !strings.Contains(out, "d=150ms") {
		t.Errorf("text duration changed: %s", out)
	}
}

func TestAppendDurationNumber(t *testing.T) {
	if got := string(appendDurationNumber(nil, 1500*time.Microsecond, time.Millisecond)); got != "1.5" {
		t.Errorf("got %q, want 1.5", got)
	}
	if got := string(appendDurationNumber(nil, 2*time.Second, time.Millisecond)); got != "2000" {
		t.Errorf("got %q, want 2000", got)
	}
}

// -----------------------------------------------------------------------------
// Config.TimestampFormat
// -----------------------------------------------------------------------------

func TestTimestampFormat_JSON(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		JSONOutput:      true,
		TimestampFormat: time.RFC3339Nano,
		FlushTimeout:    10 * time.Millisecond,
		ChannelSize:     100,
	})

	before := time.Now().Add(-time.Second)
	logger.InfoString("TS", "formatted")
	output := flushAndRead(t, logger, buf)
	after := time.Now().Add(time.Second)

	var rec struct {
		TS string `json:"ts"`
	}
	if err := json.Unmarshal([]byte(output), &rec); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, output)
	}
	got, err := time.Parse(time.RFC3339Nano, rec.TS)
	if err != nil {
		t.Fatalf("ts %q is not RFC3339Nano: %v", rec.TS, err)
	}
	if got.Before(before) || got.After(after) {
		t.Errorf("ts %v outside [%v, %v]", got, before, after)
	}
	if got.Location() != time.UTC {
		t.Errorf("ts is not in UTC: %v", got.Location())
	}
}

func TestTimestampFormat_Text(t *testing.T) {
	logger, buf, _ := setupTestLogger(t, Config{
		TimestampFormat: time.RFC3339,
		FlushTimeout:    10 * time.Millisecond,
		ChannelSize:     100,
	})
	logger.InfoString("TS", "formatted")
	output := flushAndRead(t, logger, buf)

	if !strings.HasPrefix(output, "[") {
		t.Fatalf("unexpected text output: %q", output)
	}
	end := strings.Index(output, "]")
	if end < 0 {
		t.Fatalf("unexpected text output: %q", output)
	}
	if _, err := time.Parse(time.RFC3339, output[1:end]); err != nil {
		t.Errorf("ts %q is not RFC3339: %v", output[1:end], err)
	}
}
