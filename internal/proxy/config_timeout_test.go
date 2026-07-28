package proxy

import (
	"testing"
	"time"
)

func TestInferenceTimeoutDurationIsCapped(t *testing.T) {
	for seconds, want := range map[int]time.Duration{
		0:   120 * time.Second,
		60:  60 * time.Second,
		120: 120 * time.Second,
		300: 120 * time.Second,
	} {
		cfg := DefaultConfig()
		cfg.Timeouts.InferenceTimeout = seconds
		if got := cfg.InferenceTimeoutDuration(); got != want {
			t.Fatalf("configured %ds: got %v want %v", seconds, got, want)
		}
	}
}
