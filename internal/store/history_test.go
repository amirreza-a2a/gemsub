package store_test

import (
	"encoding/json"
	"math"
	"strings"
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

func TestBoundedHistory_NormalizeAndValidate(t *testing.T) {
	// Case 1: Uninitialized struct (Capacity 0, Samples nil)
	var h1 store.BoundedHistory
	h1.NormalizeAndValidate(10)
	if h1.Capacity != 10 || len(h1.Samples) != 10 || h1.Count != 0 || h1.Start != 0 {
		t.Fatalf("h1 normalization failed: %+v", h1)
	}
	// Push must not panic
	h1.Push(store.ProbeSample{Status: store.StatusPassed})
	if h1.Count != 1 {
		t.Errorf("expected count 1 after push, got %d", h1.Count)
	}

	// Case 2: len(Samples) != Capacity (mismatched length from malformed snapshot)
	h2 := store.BoundedHistory{
		Capacity: 5,
		Count:    2,
		Start:    0,
		Samples:  []store.ProbeSample{{CycleID: 1, Status: store.StatusPassed}, {CycleID: 2, Status: store.StatusFailed}},
	}
	// Capacity is 5, but len(Samples) is 2
	h2.NormalizeAndValidate(10)
	if h2.Capacity != 5 || len(h2.Samples) != 5 {
		t.Fatalf("h2 normalization failed: %+v", h2)
	}
	if h2.Count != 2 {
		t.Fatalf("expected preserved count 2, got %d", h2.Count)
	}
	// Push 3 more to fill to 5 without panic
	for i := uint64(3); i <= 5; i++ {
		h2.Push(store.ProbeSample{CycleID: i, Status: store.StatusPassed})
	}
	if h2.Count != 5 {
		t.Fatalf("expected count 5, got %d", h2.Count)
	}
	// 6th push triggers eviction
	h2.Push(store.ProbeSample{CycleID: 6, Status: store.StatusPassed})
	if h2.Count != 5 {
		t.Fatalf("expected capped count 5, got %d", h2.Count)
	}
	samples := h2.ChronologicalSamples()
	if samples[0].CycleID != 2 || samples[4].CycleID != 6 {
		t.Fatalf("unexpected chronological samples: %+v", samples)
	}

	// Case 3: Out of bounds Start and Count
	h3 := store.BoundedHistory{
		Capacity: 3,
		Count:    10, // exceeds capacity
		Start:    -1, // invalid start
		Samples:  make([]store.ProbeSample, 3),
	}
	h3.NormalizeAndValidate(3)
	if h3.Count != 3 || h3.Start != 0 {
		t.Fatalf("expected count clamped to 3 and start reset to 0: %+v", h3)
	}

	// Case 4: Absurdly large capacity clamped to configured maximum
	h4 := store.BoundedHistory{
		Capacity: 1_000_000_000,
		Count:    0,
		Samples:  nil,
	}
	h4.NormalizeAndValidate(10)
	if h4.Capacity != 10 || len(h4.Samples) != 10 {
		t.Fatalf("expected capacity clamped to 10 and len(Samples)==10, got capacity=%d len=%d", h4.Capacity, len(h4.Samples))
	}

	// Case 5: Shrinking capacity preserves most recent samples in FIFO order
	h5 := store.NewBoundedHistory(5)
	for i := uint64(1); i <= 5; i++ {
		h5.Push(store.ProbeSample{CycleID: i, Status: store.StatusPassed})
	}
	// Shrink capacity from 5 to 3
	h5.NormalizeAndValidate(3)
	if h5.Capacity != 3 || len(h5.Samples) != 3 || h5.Count != 3 {
		t.Fatalf("expected h5 clamped to 3, got %+v", h5)
	}
	chron5 := h5.ChronologicalSamples()
	if len(chron5) != 3 || chron5[0].CycleID != 3 || chron5[1].CycleID != 4 || chron5[2].CycleID != 5 {
		t.Fatalf("expected preserved most recent samples [3, 4, 5], got %+v", chron5)
	}
}

func TestBoundedHistory_LastPassedLatency(t *testing.T) {
	h := store.NewBoundedHistory(5)

	// Empty history
	if _, ok := h.LastPassedLatency(); ok {
		t.Errorf("expected false on empty history")
	}

	// Only failed/inconclusive samples
	h.Push(store.ProbeSample{Status: store.StatusFailed, Latency: 500 * time.Millisecond})
	h.Push(store.ProbeSample{Status: store.StatusInconclusive, Latency: 4000 * time.Millisecond})
	if _, ok := h.LastPassedLatency(); ok {
		t.Errorf("expected false when no StatusPassed sample exists")
	}

	// Add a StatusPassed sample
	h.Push(store.ProbeSample{Status: store.StatusPassed, Latency: 120 * time.Millisecond})
	lat, ok := h.LastPassedLatency()
	if !ok || lat != 120*time.Millisecond {
		t.Fatalf("expected 120ms, got %v (ok=%v)", lat, ok)
	}

	// Add subsequent inconclusive and failed samples
	h.Push(store.ProbeSample{Status: store.StatusInconclusive, Latency: 4000 * time.Millisecond})
	h.Push(store.ProbeSample{Status: store.StatusFailed, Latency: 300 * time.Millisecond})

	// LastPassedLatency MUST still return 120ms
	lat, ok = h.LastPassedLatency()
	if !ok || lat != 120*time.Millisecond {
		t.Fatalf("expected last passed latency to remain 120ms, got %v (ok=%v)", lat, ok)
	}
}

func TestBoundedHistory_Serialization_Empty(t *testing.T) {
	h := store.NewBoundedHistory(10)
	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}

	var h2 store.BoundedHistory
	if err := json.Unmarshal(data, &h2); err != nil {
		t.Fatalf("unmarshal empty: %v", err)
	}
	h2.NormalizeAndValidate(10)
	if h2.Count != 0 {
		t.Errorf("expected count 0, got %d", h2.Count)
	}
	if h2.Capacity != 10 {
		t.Errorf("expected capacity 10, got %d", h2.Capacity)
	}
	if len(h2.ChronologicalSamples()) != 0 {
		t.Errorf("expected 0 chronological samples")
	}
}

func TestBoundedHistory_Serialization_OneSample(t *testing.T) {
	h := store.NewBoundedHistory(10)
	now := time.Now().Truncate(time.Millisecond)
	h.Push(store.ProbeSample{
		CycleID:     1,
		TestedAt:    now,
		Status:      store.StatusPassed,
		Category:    store.ErrNone,
		Latency:     50 * time.Millisecond,
		Attempts:    1,
		TransportOK: true,
	})

	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal 1 sample: %v", err)
	}

	// Confirm that no dummy sample padding is serialized
	if strings.Contains(string(data), "0001-01-01T00:00:00Z") {
		t.Errorf("compact payload must not contain dummy zero-value timestamps: %s", string(data))
	}

	var h2 store.BoundedHistory
	if err := json.Unmarshal(data, &h2); err != nil {
		t.Fatalf("unmarshal 1 sample: %v", err)
	}
	h2.NormalizeAndValidate(10)
	if h2.Count != 1 {
		t.Fatalf("expected count 1, got %d", h2.Count)
	}
	if h2.Capacity != 10 {
		t.Fatalf("expected capacity 10, got %d", h2.Capacity)
	}
	samples := h2.ChronologicalSamples()
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(samples))
	}
	if samples[0].CycleID != 1 || samples[0].Status != store.StatusPassed || samples[0].Latency != 50*time.Millisecond {
		t.Errorf("sample content mismatch: %+v", samples[0])
	}
}

func TestBoundedHistory_Serialization_PartialHistory(t *testing.T) {
	h := store.NewBoundedHistory(10)
	for i := uint64(1); i <= 4; i++ {
		h.Push(store.ProbeSample{
			CycleID:  i,
			Status:   store.StatusPassed,
			Latency:  time.Duration(i*10) * time.Millisecond,
			Attempts: 1,
		})
	}

	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal partial: %v", err)
	}

	var h2 store.BoundedHistory
	if err := json.Unmarshal(data, &h2); err != nil {
		t.Fatalf("unmarshal partial: %v", err)
	}
	h2.NormalizeAndValidate(10)
	if h2.Count != 4 {
		t.Fatalf("expected count 4, got %d", h2.Count)
	}
	samples := h2.ChronologicalSamples()
	if len(samples) != 4 {
		t.Fatalf("expected 4 samples, got %d", len(samples))
	}
	for i := 0; i < 4; i++ {
		if samples[i].CycleID != uint64(i+1) {
			t.Errorf("sample %d: expected CycleID %d, got %d", i, i+1, samples[i].CycleID)
		}
	}
}

func TestBoundedHistory_Serialization_FullHistory(t *testing.T) {
	h := store.NewBoundedHistory(5)
	for i := uint64(1); i <= 5; i++ {
		h.Push(store.ProbeSample{
			CycleID:  i,
			Status:   store.StatusPassed,
			Latency:  time.Duration(i*10) * time.Millisecond,
			Attempts: 1,
		})
	}

	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal full: %v", err)
	}

	var h2 store.BoundedHistory
	if err := json.Unmarshal(data, &h2); err != nil {
		t.Fatalf("unmarshal full: %v", err)
	}
	h2.NormalizeAndValidate(5)
	if h2.Count != 5 {
		t.Fatalf("expected count 5, got %d", h2.Count)
	}
	samples := h2.ChronologicalSamples()
	if len(samples) != 5 {
		t.Fatalf("expected 5 samples, got %d", len(samples))
	}
	for i := 0; i < 5; i++ {
		if samples[i].CycleID != uint64(i+1) {
			t.Errorf("sample %d: expected CycleID %d, got %d", i, i+1, samples[i].CycleID)
		}
	}
}

func TestBoundedHistory_Serialization_WrappedCircularHistory(t *testing.T) {
	h := store.NewBoundedHistory(3)
	// Push 5 samples into capacity 3 buffer: evicts 1 and 2, retaining [3, 4, 5]
	for i := uint64(1); i <= 5; i++ {
		h.Push(store.ProbeSample{
			CycleID:  i,
			Status:   store.StatusPassed,
			Latency:  time.Duration(i*10) * time.Millisecond,
			Attempts: 1,
		})
	}

	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal wrapped: %v", err)
	}

	var h2 store.BoundedHistory
	if err := json.Unmarshal(data, &h2); err != nil {
		t.Fatalf("unmarshal wrapped: %v", err)
	}
	h2.NormalizeAndValidate(3)
	if h2.Count != 3 {
		t.Fatalf("expected count 3, got %d", h2.Count)
	}
	samples := h2.ChronologicalSamples()
	expectedIDs := []uint64{3, 4, 5}
	for i := 0; i < 3; i++ {
		if samples[i].CycleID != expectedIDs[i] {
			t.Errorf("sample %d: expected CycleID %d, got %d", i, expectedIDs[i], samples[i].CycleID)
		}
	}

	// Push 6th sample to verify continued FIFO operation after unmarshaling
	h2.Push(store.ProbeSample{CycleID: 6, Status: store.StatusPassed})
	samplesAfter := h2.ChronologicalSamples()
	expectedAfter := []uint64{4, 5, 6}
	for i := 0; i < 3; i++ {
		if samplesAfter[i].CycleID != expectedAfter[i] {
			t.Errorf("sample after push %d: expected CycleID %d, got %d", i, expectedAfter[i], samplesAfter[i].CycleID)
		}
	}
}

func TestBoundedHistory_Serialization_LegacyFullCapacityV2(t *testing.T) {
	// Legacy V2 with 10 preallocated samples where only 2 are valid and 8 are dummy
	legacyJSON := `{
		"capacity": 10,
		"count": 2,
		"start": 0,
		"samples": [
			{"cycle_id": 1, "tested_at": "2026-09-01T12:00:00Z", "status": "passed", "attempts": 1},
			{"cycle_id": 2, "tested_at": "2026-09-01T12:01:00Z", "status": "passed", "attempts": 1},
			{"cycle_id": 0, "tested_at": "0001-01-01T00:00:00Z", "status": "", "attempts": 0},
			{"cycle_id": 0, "tested_at": "0001-01-01T00:00:00Z", "status": "", "attempts": 0},
			{"cycle_id": 0, "tested_at": "0001-01-01T00:00:00Z", "status": "", "attempts": 0},
			{"cycle_id": 0, "tested_at": "0001-01-01T00:00:00Z", "status": "", "attempts": 0},
			{"cycle_id": 0, "tested_at": "0001-01-01T00:00:00Z", "status": "", "attempts": 0},
			{"cycle_id": 0, "tested_at": "0001-01-01T00:00:00Z", "status": "", "attempts": 0},
			{"cycle_id": 0, "tested_at": "0001-01-01T00:00:00Z", "status": "", "attempts": 0},
			{"cycle_id": 0, "tested_at": "0001-01-01T00:00:00Z", "status": "", "attempts": 0}
		]
	}`

	var h store.BoundedHistory
	if err := json.Unmarshal([]byte(legacyJSON), &h); err != nil {
		t.Fatalf("unmarshal legacy V2: %v", err)
	}
	h.NormalizeAndValidate(10)
	if h.Count != 2 {
		t.Fatalf("expected count 2, got %d", h.Count)
	}
	if h.Capacity != 10 {
		t.Fatalf("expected capacity 10, got %d", h.Capacity)
	}
	samples := h.ChronologicalSamples()
	if len(samples) != 2 {
		t.Fatalf("expected 2 chronological samples, got %d", len(samples))
	}
	if samples[0].CycleID != 1 || samples[1].CycleID != 2 {
		t.Errorf("samples mismatch: %+v", samples)
	}
}

func TestBoundedHistory_Serialization_InvalidAndCorruptHandling(t *testing.T) {
	// Negative capacity and out of range count
	corruptJSON := `{
		"capacity": -5,
		"count": 999,
		"start": -1,
		"samples": [
			{"cycle_id": 1, "status": "passed"}
		]
	}`

	var h store.BoundedHistory
	if err := json.Unmarshal([]byte(corruptJSON), &h); err != nil {
		t.Fatalf("unmarshal corrupt: %v", err)
	}
	h.NormalizeAndValidate(10)
	if h.Capacity != 10 {
		t.Errorf("expected clamped capacity 10, got %d", h.Capacity)
	}
	if h.Count != 1 {
		t.Errorf("expected clamped count 1, got %d", h.Count)
	}

	// Malformed JSON syntax
	var h2 store.BoundedHistory
	if err := json.Unmarshal([]byte(`{not-json}`), &h2); err == nil {
		t.Errorf("expected error on malformed JSON")
	}
}
