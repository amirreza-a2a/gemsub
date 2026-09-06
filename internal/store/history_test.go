package store_test

import (
	"math"
	"testing"
	"time"

	"gemsub/internal/store"
)

func TestBoundedHistory_EmptyAndPartialFill(t *testing.T) {
	h := store.NewBoundedHistory(5)

	if h.Count != 0 {
		t.Errorf("expected count 0, got %d", h.Count)
	}
	if h.Capacity != 5 {
		t.Errorf("expected capacity 5, got %d", h.Capacity)
	}
	if len(h.ChronologicalSamples()) != 0 {
		t.Errorf("expected 0 chronological samples")
	}

	h.Push(store.ProbeSample{
		CycleID:  1,
		Status:   store.StatusPassed,
		Category: store.ErrNone,
		TestedAt: time.Now(),
	})

	if h.Count != 1 {
		t.Errorf("expected count 1, got %d", h.Count)
	}
	samples := h.ChronologicalSamples()
	if len(samples) != 1 || samples[0].CycleID != 1 {
		t.Fatalf("expected 1 sample with CycleID=1, got %+v", samples)
	}
}

func TestBoundedHistory_FullAndFIFOWraparound(t *testing.T) {
	h := store.NewBoundedHistory(3)

	// Push 3 samples
	for i := uint64(1); i <= 3; i++ {
		h.Push(store.ProbeSample{CycleID: i, Status: store.StatusPassed})
	}

	if h.Count != 3 {
		t.Fatalf("expected count 3, got %d", h.Count)
	}

	// Push 4th sample -> evicts sample 1
	h.Push(store.ProbeSample{CycleID: 4, Status: store.StatusFailed})

	if h.Count != 3 {
		t.Fatalf("expected count 3, got %d", h.Count)
	}

	samples := h.ChronologicalSamples()
	if len(samples) != 3 {
		t.Fatalf("expected 3 samples, got %d", len(samples))
	}
	expectedIDs := []uint64{2, 3, 4}
	for idx, exp := range expectedIDs {
		if samples[idx].CycleID != exp {
			t.Errorf("samples[%d]: expected CycleID=%d, got %d", idx, exp, samples[idx].CycleID)
		}
	}

	// Push 5th and 6th -> wraparound verified
	h.Push(store.ProbeSample{CycleID: 5, Status: store.StatusPassed})
	h.Push(store.ProbeSample{CycleID: 6, Status: store.StatusPassed})

	samples = h.ChronologicalSamples()
	expectedIDs = []uint64{4, 5, 6}
	for idx, exp := range expectedIDs {
		if samples[idx].CycleID != exp {
			t.Errorf("samples[%d]: expected CycleID=%d, got %d", idx, exp, samples[idx].CycleID)
		}
	}
}

func TestBoundedHistory_ScoreCalculation_VerifiedNumericalExamples(t *testing.T) {
	cfg := store.DefaultScoringConfig()
	// DefaultScoringConfig: DecayLambda = 0.75, HistoryCapacity = 10, Timeout weight = 0.40, Pass weight = 1.0

	// Case 1: 9 passes followed by 1 timeout
	h1 := store.NewBoundedHistory(10)
	for i := 0; i < 9; i++ {
		h1.Push(store.ProbeSample{Status: store.StatusPassed, Category: store.ErrNone, Attempts: 1})
	}
	h1.Push(store.ProbeSample{Status: store.StatusInconclusive, Category: store.ErrTimeout, Attempts: 1})

	score1 := h1.ComputeScore(cfg.DecayLambda, cfg.CategoryWeights)
	expectedScore1 := 0.84105
	if math.Abs(score1-expectedScore1) > 0.0001 {
		t.Errorf("Case 1 (9 pass + 1 timeout): expected %f, got %f", expectedScore1, score1)
	}

	// Case 2: 8 passes followed by 2 timeouts
	h2 := store.NewBoundedHistory(10)
	for i := 0; i < 8; i++ {
		h2.Push(store.ProbeSample{Status: store.StatusPassed, Category: store.ErrNone, Attempts: 1})
	}
	for i := 0; i < 2; i++ {
		h2.Push(store.ProbeSample{Status: store.StatusInconclusive, Category: store.ErrTimeout, Attempts: 1})
	}

	score2 := h2.ComputeScore(cfg.DecayLambda, cfg.CategoryWeights)
	expectedScore2 := 0.72183
	if math.Abs(score2-expectedScore2) > 0.0001 {
		t.Errorf("Case 2 (8 pass + 2 timeouts): expected %f, got %f", expectedScore2, score2)
	}

	// Case 3: 7 passes followed by 3 timeouts
	h3 := store.NewBoundedHistory(10)
	for i := 0; i < 7; i++ {
		h3.Push(store.ProbeSample{Status: store.StatusPassed, Category: store.ErrNone, Attempts: 1})
	}
	for i := 0; i < 3; i++ {
		h3.Push(store.ProbeSample{Status: store.StatusInconclusive, Category: store.ErrTimeout, Attempts: 1})
	}

	score3 := h3.ComputeScore(cfg.DecayLambda, cfg.CategoryWeights)
	expectedScore3 := 0.63243
	if math.Abs(score3-expectedScore3) > 0.0001 {
		t.Errorf("Case 3 (7 pass + 3 timeouts): expected %f, got %f", expectedScore3, score3)
	}
}

func TestBoundedHistory_PreviouslyPassed_LKG(t *testing.T) {
	h := store.NewBoundedHistory(5)

	// Initially empty -> false
	if h.PreviouslyPassed() {
		t.Errorf("expected Initially false")
	}

	// 1. Pass -> true
	h.Push(store.ProbeSample{Status: store.StatusPassed, Category: store.ErrNone})
	if !h.PreviouslyPassed() {
		t.Errorf("expected true after StatusPassed")
	}

	// 2. Trailing inconclusive -> retains true
	h.Push(store.ProbeSample{Status: store.StatusInconclusive, Category: store.ErrTimeout})
	if !h.PreviouslyPassed() {
		t.Errorf("expected true preserved on trailing inconclusive")
	}

	// 3. Failed -> false
	h.Push(store.ProbeSample{Status: store.StatusFailed, Category: store.ErrRegionBlocked})
	if h.PreviouslyPassed() {
		t.Errorf("expected false after StatusFailed")
	}

	// 4. Trailing inconclusive -> retains false
	h.Push(store.ProbeSample{Status: store.StatusInconclusive, Category: store.ErrTimeout})
	if h.PreviouslyPassed() {
		t.Errorf("expected false preserved on trailing inconclusive")
	}
}

func TestBoundedHistory_ConsecutiveInconclusive(t *testing.T) {
	h := store.NewBoundedHistory(5)

	if h.ConsecutiveInconclusive() != 0 {
		t.Errorf("expected 0 for empty buffer")
	}

	h.Push(store.ProbeSample{Status: store.StatusPassed, Category: store.ErrNone})
	if h.ConsecutiveInconclusive() != 0 {
		t.Errorf("expected 0 after pass")
	}

	h.Push(store.ProbeSample{Status: store.StatusInconclusive, Category: store.ErrTimeout})
	if h.ConsecutiveInconclusive() != 1 {
		t.Errorf("expected 1 after 1 inconclusive")
	}

	h.Push(store.ProbeSample{Status: store.StatusInconclusive, Category: store.ErrProxyRateLimited})
	if h.ConsecutiveInconclusive() != 2 {
		t.Errorf("expected 2 after 2 inconclusive")
	}

	h.Push(store.ProbeSample{Status: store.StatusPassed, Category: store.ErrNone})
	if h.ConsecutiveInconclusive() != 0 {
		t.Errorf("expected reset to 0 after pass")
	}
}
