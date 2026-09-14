package tester

import (
	"math"
	"time"
)

// ComputeSampleStandardDeviation computes the sample standard deviation
// (s = sqrt(1/(n-1) * sum((x - mean)^2))) for a slice of round-trip durations.
// If len(samples) < 2, it returns 0.
func ComputeSampleStandardDeviation(samples []time.Duration) time.Duration {
	n := len(samples)
	if n < 2 {
		return 0
	}

	var sum float64
	for _, d := range samples {
		sum += float64(d)
	}
	mean := sum / float64(n)

	var varianceSum float64
	for _, d := range samples {
		diff := float64(d) - mean
		varianceSum += diff * diff
	}

	variance := varianceSum / float64(n-1)
	return time.Duration(math.Round(math.Sqrt(variance)))
}
