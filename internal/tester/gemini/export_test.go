package gemini

import "time"

// ClampLatencyForTest exposes clampLatency for deterministic unit testing.
func ClampLatencyForTest(d time.Duration) time.Duration {
	return clampLatency(d)
}
