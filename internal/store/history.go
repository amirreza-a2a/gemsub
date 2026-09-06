package store

import (
	"math"
	"time"
)

// ProbeSample records the discrete outcome of testing a single candidate during a cycle.
type ProbeSample struct {
	CycleID    uint64        `json:"cycle_id"`
	TestedAt   time.Time     `json:"tested_at"`
	Status     Status        `json:"status"`
	Category   ErrorCategory `json:"category"`
	StatusCode int           `json:"status_code,omitempty"`
	Latency    time.Duration `json:"latency,omitempty"`
	Attempts   int           `json:"attempts"`
}

// BoundedHistory is a fixed-capacity circular buffer of probe samples with strict O(1) memory per candidate.
type BoundedHistory struct {
	Capacity int           `json:"capacity"`
	Count    int           `json:"count"`
	Start    int           `json:"start"`
	Samples  []ProbeSample `json:"samples"`
}

// NewBoundedHistory creates a bounded history buffer with fixed capacity.
func NewBoundedHistory(capacity int) BoundedHistory {
	if capacity <= 0 {
		capacity = 10
	}
	return BoundedHistory{
		Capacity: capacity,
		Count:    0,
		Start:    0,
		Samples:  make([]ProbeSample, capacity),
	}
}

// Clone returns a deep copy of the BoundedHistory with its own backing slice.
func (h BoundedHistory) Clone() BoundedHistory {
	cp := h
	if len(h.Samples) > 0 {
		cp.Samples = make([]ProbeSample, len(h.Samples))
		copy(cp.Samples, h.Samples)
	}
	return cp
}

// Push adds a new probe sample to the circular buffer, evicting the oldest sample if capacity is reached.
func (h *BoundedHistory) Push(sample ProbeSample) {
	if h.Capacity <= 0 {
		h.Capacity = 10
		h.Samples = make([]ProbeSample, h.Capacity)
	}

	if h.Count < h.Capacity {
		idx := (h.Start + h.Count) % h.Capacity
		h.Samples[idx] = sample
		h.Count++
	} else {
		// Buffer full: overwrite the oldest entry at Start and advance Start
		h.Samples[h.Start] = sample
		h.Start = (h.Start + 1) % h.Capacity
	}
}

// ChronologicalSamples returns all valid samples ordered from oldest to newest.
func (h *BoundedHistory) ChronologicalSamples() []ProbeSample {
	if h.Count == 0 {
		return nil
	}
	out := make([]ProbeSample, h.Count)
	for i := 0; i < h.Count; i++ {
		idx := (h.Start + i) % h.Capacity
		out[i] = h.Samples[idx]
	}
	return out
}

// Last returns the most recent probe sample, or false if the buffer is empty.
func (h *BoundedHistory) Last() (ProbeSample, bool) {
	if h.Count == 0 {
		return ProbeSample{}, false
	}
	lastIdx := (h.Start + h.Count - 1) % h.Capacity
	return h.Samples[lastIdx], true
}

// PreviouslyPassed returns the Last-Known-Good compatibility projection.
// It reflects the most recent conclusive observation:
// StatusPassed -> true, StatusFailed -> false, trailing StatusInconclusive -> retains prior conclusive state.
func (h *BoundedHistory) PreviouslyPassed() bool {
	if h.Count == 0 {
		return false
	}
	// Scan backward from newest to oldest
	for i := h.Count - 1; i >= 0; i-- {
		idx := (h.Start + i) % h.Capacity
		switch h.Samples[idx].Status {
		case StatusPassed:
			return true
		case StatusFailed:
			return false
		}
	}
	return false
}

// ConsecutiveInconclusive derives the count of contiguous trailing StatusInconclusive samples.
func (h *BoundedHistory) ConsecutiveInconclusive() int {
	if h.Count == 0 {
		return 0
	}
	count := 0
	for i := h.Count - 1; i >= 0; i-- {
		idx := (h.Start + i) % h.Capacity
		if h.Samples[idx].Status == StatusInconclusive {
			count++
		} else {
			break
		}
	}
	return count
}

// LatestConclusive returns the most recent conclusive (StatusPassed or StatusFailed) sample,
// or false if no conclusive sample exists in the history.
func (h *BoundedHistory) LatestConclusive() (ProbeSample, bool) {
	if h.Count == 0 {
		return ProbeSample{}, false
	}
	for i := h.Count - 1; i >= 0; i-- {
		idx := (h.Start + i) % h.Capacity
		if h.Samples[idx].Status == StatusPassed || h.Samples[idx].Status == StatusFailed {
			return h.Samples[idx], true
		}
	}
	return ProbeSample{}, false
}

// ComputeScore calculates the deterministic recency-weighted reliability score:
// S = sum(lambda^(k-i) * W(O_i)) / sum(lambda^(k-i))
func (h *BoundedHistory) ComputeScore(decayLambda float64, weights map[ErrorCategory]float64) float64 {
	if h.Count == 0 {
		return 0.0
	}
	if decayLambda <= 0.0 || decayLambda > 1.0 {
		decayLambda = 0.75
	}

	var num, den float64
	k := h.Count

	for i := 0; i < k; i++ {
		idx := (h.Start + i) % h.Capacity
		sample := h.Samples[idx]

		power := float64(k - 1 - i)
		decay := math.Pow(decayLambda, power)

		w := 0.1 // baseline transport failure fallback
		if sample.Status == StatusPassed {
			w = 1.0
		} else if customW, ok := weights[sample.Category]; ok {
			w = customW
		} else if sample.Status == StatusInconclusive {
			w = 0.4
		}

		num += decay * w
		den += decay
	}

	if den == 0.0 {
		return 0.0
	}
	return num / den
}
