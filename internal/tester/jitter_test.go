package tester_test

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"

	"gemsub/internal/config"
	"gemsub/internal/parser"
	"gemsub/internal/store"
	"gemsub/internal/tester"
)

func TestComputeSampleStandardDeviation(t *testing.T) {
	tests := []struct {
		name     string
		samples  []time.Duration
		expected time.Duration
	}{
		{
			name:     "nil samples",
			samples:  nil,
			expected: 0,
		},
		{
			name:     "empty samples",
			samples:  []time.Duration{},
			expected: 0,
		},
		{
			name:     "single sample",
			samples:  []time.Duration{100 * time.Millisecond},
			expected: 0,
		},
		{
			name:     "two identical samples",
			samples:  []time.Duration{50 * time.Millisecond, 50 * time.Millisecond},
			expected: 0,
		},
		{
			name:     "three linear samples",
			samples:  []time.Duration{100 * time.Millisecond, 150 * time.Millisecond, 200 * time.Millisecond},
			expected: 50 * time.Millisecond,
		},
		{
			name:    "five samples",
			samples: []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond, 40 * time.Millisecond, 50 * time.Millisecond},
			// variance = (400 + 100 + 0 + 100 + 400)/4 = 250 ms^2 -> sqrt(250) = 15.8113883 ms
			expected: time.Duration(math.Round(math.Sqrt(250) * 1e6)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tester.ComputeSampleStandardDeviation(tt.samples)
			// Allow up to 1 microsecond rounding tolerance
			diff := got - tt.expected
			if diff < 0 {
				diff = -diff
			}
			if diff > time.Microsecond {
				t.Errorf("expected %v, got %v (diff %v)", tt.expected, got, diff)
			}
		})
	}
}

func TestProbe_JitterSampling_PassedCandidateReusesBox(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var healthHits int64
	healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&healthHits, 1)
		time.Sleep(10 * time.Millisecond) // Ensure measurable latency
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthServer.Close()

	var geminiHits int64
	geminiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&geminiHits, 1)
		w.Header().Set("Server", "ESF")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<!doctype html><html><head><title>Google Gemini</title></head><body><div id=\"bardchatui\"><p>Welcome to Gemini</p></div></body></html>"))
	}))
	defer geminiServer.Close()

	cand := parser.Candidate{
		Link: "vmess://direct-jitter-test",
		Outbound: option.Outbound{
			Type: "direct",
			Tag:  "probe",
		},
	}

	testCfg := &config.TestConfig{
		HealthURL:     healthServer.URL,
		HealthTimeout: 2 * time.Second,
		TargetURL:     geminiServer.URL,
		BlockPhrases:  []string{"not available in your region"},
		Timeout:       5 * time.Second,
		DialTimeout:   2 * time.Second,
		MaxRetries:    0,
		JitterSamples: 3,
	}

	res := tester.Probe(ctx, cand, testCfg, nil)

	if res.Status != store.StatusPassed {
		t.Fatalf("expected StatusPassed, got %s (reason: %q)", res.Status, res.Reason)
	}

	// Health hits should be 1 (Stage 1) + 3 (Jitter samples) = 4 hits
	hits := atomic.LoadInt64(&healthHits)
	if hits != 4 {
		t.Errorf("expected 4 hits on healthServer (1 stage1 + 3 jitter samples), got %d", hits)
	}

	gHits := atomic.LoadInt64(&geminiHits)
	if gHits != 1 {
		t.Errorf("expected 1 hit on geminiServer, got %d", gHits)
	}

	if res.Jitter <= 0 {
		t.Errorf("expected positive res.Jitter, got %v", res.Jitter)
	}
}

func TestProbe_JitterSampling_DisabledWhenZero(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var healthHits int64
	healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&healthHits, 1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthServer.Close()

	geminiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "ESF")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<!doctype html><html><head><title>Google Gemini</title></head><body><div id=\"bardchatui\"><p>Welcome to Gemini</p></div></body></html>"))
	}))
	defer geminiServer.Close()

	cand := parser.Candidate{
		Link: "vmess://direct-zero-jitter",
		Outbound: option.Outbound{
			Type: "direct",
			Tag:  "probe",
		},
	}

	testCfg := &config.TestConfig{
		HealthURL:     healthServer.URL,
		HealthTimeout: 2 * time.Second,
		TargetURL:     geminiServer.URL,
		Timeout:       5 * time.Second,
		DialTimeout:   2 * time.Second,
		MaxRetries:    0,
		JitterSamples: 0, // Disabled
	}

	res := tester.Probe(ctx, cand, testCfg, nil)

	if res.Status != store.StatusPassed {
		t.Fatalf("expected StatusPassed, got %s", res.Status)
	}

	hits := atomic.LoadInt64(&healthHits)
	if hits != 1 {
		t.Errorf("expected exactly 1 hit on healthServer when JitterSamples=0, got %d", hits)
	}

	if res.Jitter != 0 {
		t.Errorf("expected Jitter=0 when disabled, got %v", res.Jitter)
	}
}

func TestProbe_JitterSampling_BestEffortResilience(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var healthHits int64
	healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := atomic.AddInt64(&healthHits, 1)
		if h == 1 {
			// Stage 1 succeeds
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Subsequent jitter samples fail with 500 error
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer healthServer.Close()

	geminiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "ESF")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<!doctype html><html><head><title>Google Gemini</title></head><body><div id=\"bardchatui\"><p>Welcome to Gemini</p></div></body></html>"))
	}))
	defer geminiServer.Close()

	cand := parser.Candidate{
		Link: "vmess://direct-resilience-test",
		Outbound: option.Outbound{
			Type: "direct",
			Tag:  "probe",
		},
	}

	testCfg := &config.TestConfig{
		HealthURL:     healthServer.URL,
		HealthTimeout: 2 * time.Second,
		TargetURL:     geminiServer.URL,
		Timeout:       5 * time.Second,
		DialTimeout:   2 * time.Second,
		MaxRetries:    0,
		JitterSamples: 3,
	}

	res := tester.Probe(ctx, cand, testCfg, nil)

	// Crucial: Candidate's StatusPassed must NOT be altered by failed jitter samples
	if res.Status != store.StatusPassed {
		t.Fatalf("expected StatusPassed despite jitter sample errors, got %s (reason: %q)", res.Status, res.Reason)
	}

	// Since only 1 successful measurement existed (initial stage 1), jitter must be 0 (< 2 samples)
	if res.Jitter != 0 {
		t.Errorf("expected Jitter=0 when < 2 samples succeeded, got %v", res.Jitter)
	}
}

func TestProbe_JitterSampling_SkippedOnFailedCandidate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var healthHits int64
	healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&healthHits, 1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthServer.Close()

	geminiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Region blocked
		w.Header().Set("Server", "ESF")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<!doctype html><html><head><title>Google Gemini</title></head><body><p>Gemini isn't currently supported in your country</p></body></html>"))
	}))
	defer geminiServer.Close()

	cand := parser.Candidate{
		Link: "vmess://direct-failed-test",
		Outbound: option.Outbound{
			Type: "direct",
			Tag:  "probe",
		},
	}

	testCfg := &config.TestConfig{
		HealthURL:     healthServer.URL,
		HealthTimeout: 2 * time.Second,
		TargetURL:     geminiServer.URL,
		BlockPhrases:  []string{"isn't currently supported in your country"},
		Timeout:       5 * time.Second,
		DialTimeout:   2 * time.Second,
		MaxRetries:    0,
		JitterSamples: 3,
	}

	res := tester.Probe(ctx, cand, testCfg, nil)

	if res.Status != store.StatusFailed {
		t.Fatalf("expected StatusFailed, got %s", res.Status)
	}

	// Health hits should be exactly 1 (Stage 1 only); jitter samples must NOT run on failed candidates
	hits := atomic.LoadInt64(&healthHits)
	if hits != 1 {
		t.Errorf("expected exactly 1 hit on healthServer for failed candidate, got %d", hits)
	}

	if res.Jitter != 0 {
		t.Errorf("expected Jitter=0 for failed candidate, got %v", res.Jitter)
	}
}

func TestProbe_JitterSampling_EndToEndStorePersistence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthServer.Close()

	geminiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "ESF")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<!doctype html><html><head><title>Google Gemini</title></head><body><div id=\"bardchatui\"><p>Welcome to Gemini</p></div></body></html>"))
	}))
	defer geminiServer.Close()

	link := "vmess://direct-e2e-jitter-store"
	cand := parser.Candidate{
		Link: link,
		Outbound: option.Outbound{
			Type: "direct",
			Tag:  "probe",
		},
	}

	testCfg := &config.TestConfig{
		HealthURL:     healthServer.URL,
		HealthTimeout: 2 * time.Second,
		TargetURL:     geminiServer.URL,
		Timeout:       5 * time.Second,
		DialTimeout:   2 * time.Second,
		MaxRetries:    0,
		JitterSamples: 3,
	}

	res := tester.Probe(ctx, cand, testCfg, nil)
	if res.Status != store.StatusPassed {
		t.Fatalf("expected StatusPassed, got %s", res.Status)
	}
	if res.Jitter <= 0 {
		t.Fatalf("expected positive Jitter, got %v", res.Jitter)
	}

	// Persist to store
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	st := store.New(stateFile, 2)
	st.PutWithTransition(res)
	if err := st.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Reload in fresh store
	st2 := store.New(stateFile, 2)
	if err := st2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	rec, ok := st2.GetRecord(link)
	if !ok {
		t.Fatalf("expected record in reloaded store")
	}
	if rec.Latest.Jitter != res.Jitter {
		t.Errorf("expected Latest.Jitter %v, got %v", res.Jitter, rec.Latest.Jitter)
	}
	smp, ok := rec.History.LatestConclusive()
	if !ok {
		t.Fatalf("expected conclusive sample in history")
	}
	if smp.Jitter != res.Jitter {
		t.Errorf("expected sample Jitter %v, got %v", res.Jitter, smp.Jitter)
	}
}
