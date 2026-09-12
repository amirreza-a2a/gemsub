package tester

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	"golang.org/x/time/rate"

	"gemsub/internal/config"
	"gemsub/internal/parser"
	"gemsub/internal/store"
)

func newDirectCandidate(tag string) parser.Candidate {
	return parser.Candidate{
		Link: "vmess://" + tag,
		Outbound: option.Outbound{
			Type: "direct",
			Tag:  tag,
		},
	}
}

// TestTwoStage_A_TransportFailure_EarlyExit proves that when Stage 1 fails:
// 1. Stage 2 (Gemini) is never executed.
// 2. TransportEvidenceKnown=true, TransportOK=false.
// 3. TransportLatency is recorded.
func TestTwoStage_A_TransportFailure_EarlyExit(t *testing.T) {
	var geminiCalls int64
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&geminiCalls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer geminiSrv.Close()

	closedPort := getClosedPort(t)
	cand := newDirectCandidate("probe-fail-early")
	cfg := &config.TestConfig{
		HealthURL:     fmt.Sprintf("http://127.0.0.1:%d/generate_204", closedPort),
		HealthTimeout: 1 * time.Second,
		TargetURL:     geminiSrv.URL,
		Timeout:       2 * time.Second,
		DialTimeout:   1 * time.Second,
	}

	classRes, retryable, _ := executeAttempt(context.Background(), cand, cfg)

	if atomic.LoadInt64(&geminiCalls) != 0 {
		t.Fatalf("Stage 2 executed despite Stage 1 failure; geminiCalls=%d", geminiCalls)
	}
	if !classRes.TransportEvidenceKnown {
		t.Errorf("expected TransportEvidenceKnown == true")
	}
	if classRes.TransportOK {
		t.Errorf("expected TransportOK == false")
	}
	if classRes.Category != store.ErrConnRefused {
		t.Errorf("expected Category == ErrConnRefused, got %s", classRes.Category)
	}
	if classRes.Status != store.StatusFailed {
		t.Errorf("expected Status == StatusFailed, got %s", classRes.Status)
	}
	if retryable {
		t.Errorf("connection refused should not be retryable")
	}
}

// TestTwoStage_B_TransportSuccess_GeminiSuccess proves that when Stage 1 succeeds:
// 1. Stage 2 is executed.
// 2. StatusPassed is produced.
// 3. TransportEvidenceKnown=true, TransportOK=true, TransportLatency preserved from Stage 1.
func TestTwoStage_B_TransportSuccess_GeminiSuccess(t *testing.T) {
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthSrv.Close()

	var geminiCalls int64
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&geminiCalls, 1)
		w.Header().Set("Server", "ESF")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Google Gemini</title></head><body><div id="bardchatui"></div></body></html>`))
	}))
	defer geminiSrv.Close()

	cand := newDirectCandidate("probe-success-success")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     geminiSrv.URL,
		Timeout:       2 * time.Second,
		DialTimeout:   1 * time.Second,
	}

	classRes, retryable, _ := executeAttempt(context.Background(), cand, cfg)

	if atomic.LoadInt64(&geminiCalls) != 1 {
		t.Fatalf("expected exactly 1 Gemini call, got %d", geminiCalls)
	}
	if classRes.Status != store.StatusPassed {
		t.Errorf("expected StatusPassed, got %s (reason=%q)", classRes.Status, classRes.Reason)
	}
	if classRes.Category != store.ErrNone {
		t.Errorf("expected ErrNone, got %s", classRes.Category)
	}
	if !classRes.TransportEvidenceKnown {
		t.Errorf("expected TransportEvidenceKnown == true")
	}
	if !classRes.TransportOK {
		t.Errorf("expected TransportOK == true")
	}
	if classRes.TransportLatency <= 0 {
		t.Errorf("expected positive TransportLatency, got %v", classRes.TransportLatency)
	}
	if retryable {
		t.Errorf("successful attempt should not be retryable")
	}
}

// TestTwoStage_C_TransportSuccess_GeminiRegionBlocked proves that when Stage 1 succeeds
// and Gemini reports a regional block:
// 1. StatusFailed with ErrRegionBlocked is reported.
// 2. TransportEvidenceKnown=true, TransportOK=true (transport remains healthy).
func TestTwoStage_C_TransportSuccess_GeminiRegionBlocked(t *testing.T) {
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthSrv.Close()

	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><html><body>Gemini isn't supported in your country</body></html>`))
	}))
	defer geminiSrv.Close()

	cand := newDirectCandidate("probe-region-blocked")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     geminiSrv.URL,
		BlockPhrases:  []string{"isn't supported in your country"},
		Timeout:       2 * time.Second,
		DialTimeout:   1 * time.Second,
	}

	classRes, _, _ := executeAttempt(context.Background(), cand, cfg)

	if classRes.Status != store.StatusFailed {
		t.Errorf("expected StatusFailed, got %s", classRes.Status)
	}
	if classRes.Category != store.ErrRegionBlocked {
		t.Errorf("expected ErrRegionBlocked, got %s", classRes.Category)
	}
	if !classRes.TransportEvidenceKnown || !classRes.TransportOK {
		t.Errorf("expected transport evidence to be Known=true and TransportOK=true; got Known=%v OK=%v",
			classRes.TransportEvidenceKnown, classRes.TransportOK)
	}
}

// TestTwoStage_D_TransportSuccess_Gemini403 proves that when Stage 1 succeeds and Gemini returns 403:
// 1. StatusFailed with ErrTargetDenied, StatusCode=403 is reported.
// 2. TransportEvidenceKnown=true, TransportOK=true.
func TestTwoStage_D_TransportSuccess_Gemini403(t *testing.T) {
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthSrv.Close()

	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer geminiSrv.Close()

	cand := newDirectCandidate("probe-gemini-403")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     geminiSrv.URL,
		Timeout:       2 * time.Second,
		DialTimeout:   1 * time.Second,
	}

	classRes, retryable, _ := executeAttempt(context.Background(), cand, cfg)

	if classRes.Status != store.StatusFailed {
		t.Errorf("expected StatusFailed, got %s", classRes.Status)
	}
	if classRes.Category != store.ErrTargetDenied {
		t.Errorf("expected ErrTargetDenied, got %s", classRes.Category)
	}
	if classRes.StatusCode != 403 {
		t.Errorf("expected StatusCode 403, got %d", classRes.StatusCode)
	}
	if !classRes.TransportEvidenceKnown || !classRes.TransportOK {
		t.Errorf("expected Known=true, TransportOK=true; got Known=%v OK=%v",
			classRes.TransportEvidenceKnown, classRes.TransportOK)
	}
	if retryable {
		t.Errorf("403 should not be retryable")
	}
}

// TestTwoStage_E_TransportSuccess_Gemini429_RetryAfter proves that when Stage 1 succeeds
// and Gemini returns 429 with Retry-After:
// 1. Retry occurs at candidate attempt level.
// 2. Retry-After is honored.
// 3. Each retry restarts from Stage 1.
func TestTwoStage_E_TransportSuccess_Gemini429_RetryAfter(t *testing.T) {
	var healthCalls int64
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&healthCalls, 1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthSrv.Close()

	var geminiCalls int64
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt64(&geminiCalls, 1)
		if call == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Server", "ESF")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Google Gemini</title></head><body><div id="bardchatui"></div></body></html>`))
	}))
	defer geminiSrv.Close()

	cand := newDirectCandidate("probe-429-retry")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     geminiSrv.URL,
		Timeout:       3 * time.Second,
		DialTimeout:   1 * time.Second,
		MaxRetries:    1,
		RetryBackoff:  10 * time.Millisecond,
	}

	start := time.Now()
	res := Probe(context.Background(), cand, cfg, nil)
	elapsed := time.Since(start)

	if atomic.LoadInt64(&healthCalls) != 2 {
		t.Errorf("expected Stage 1 to run on each retry attempt; healthCalls=%d, want 2", healthCalls)
	}
	if atomic.LoadInt64(&geminiCalls) != 2 {
		t.Errorf("expected exactly 2 Gemini calls; geminiCalls=%d, want 2", geminiCalls)
	}
	if res.Status != store.StatusPassed {
		t.Fatalf("expected StatusPassed, got %s (reason=%q)", res.Status, res.Reason)
	}
	if !res.TransportEvidenceKnown || !res.TransportOK {
		t.Errorf("expected final Result to have Known=true and TransportOK=true")
	}
	if elapsed < 900*time.Millisecond {
		t.Errorf("Retry-After delay was not honored; elapsed=%v < 900ms", elapsed)
	}
}

// TestTwoStage_F_Stage1_HealthTimeout proves that when Stage 1 times out:
// 1. Completion is bounded by HealthTimeout (not broader Gemini timeout).
// 2. Stage 2 is never executed.
// 3. Known=true, TransportOK=false.
func TestTwoStage_F_Stage1_HealthTimeout(t *testing.T) {
	// TCP listener that accepts connections but never writes or closes
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			time.Sleep(2 * time.Second)
		}
	}()

	var geminiCalls int64
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&geminiCalls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer geminiSrv.Close()

	cand := newDirectCandidate("probe-health-timeout")
	cfg := &config.TestConfig{
		HealthURL:     fmt.Sprintf("http://%s/generate_204", l.Addr().String()),
		HealthTimeout: 100 * time.Millisecond,
		TargetURL:     geminiSrv.URL,
		Timeout:       2 * time.Second, // Much larger than HealthTimeout
		DialTimeout:   50 * time.Millisecond,
	}

	start := time.Now()
	classRes, retryable, _ := executeAttempt(context.Background(), cand, cfg)
	elapsed := time.Since(start)

	if atomic.LoadInt64(&geminiCalls) != 0 {
		t.Fatalf("Stage 2 executed despite Stage 1 timeout; geminiCalls=%d", geminiCalls)
	}
	if elapsed >= 1*time.Second {
		t.Fatalf("attempt was not bounded by HealthTimeout (100ms); elapsed=%v", elapsed)
	}
	if !classRes.TransportEvidenceKnown {
		t.Errorf("expected TransportEvidenceKnown == true")
	}
	if classRes.TransportOK {
		t.Errorf("expected TransportOK == false on Stage 1 timeout")
	}
	if classRes.Status != store.StatusFailed {
		t.Errorf("expected StatusFailed for Stage 1 health timeout, got %s", classRes.Status)
	}
	if classRes.Category != store.ErrTimeout {
		t.Errorf("expected ErrTimeout, got %s", classRes.Category)
	}
	if retryable {
		t.Errorf("timeout should not be retryable")
	}
}

// TestTwoStage_G_Stage2_TimeoutCancellation proves that when Stage 2 times out or expires:
// 1. StatusInconclusive with ErrTimeout is produced.
// 2. TransportEvidenceKnown=true, TransportOK=true (Stage 1 already succeeded).
// 3. Timeout is attributed to Stage 2, not transport failure.
func TestTwoStage_G_Stage2_TimeoutCancellation(t *testing.T) {
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthSrv.Close()

	// Stalling listener for Gemini
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			time.Sleep(2 * time.Second)
		}
	}()

	cand := newDirectCandidate("probe-gemini-timeout")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     fmt.Sprintf("http://%s/", l.Addr().String()),
		Timeout:       100 * time.Millisecond, // Expires during Stage 2
		DialTimeout:   50 * time.Millisecond,
	}

	classRes, _, _ := executeAttempt(context.Background(), cand, cfg)

	if classRes.Status != store.StatusInconclusive {
		t.Errorf("expected StatusInconclusive for Stage 2 timeout, got %s", classRes.Status)
	}
	if classRes.Category != store.ErrTimeout {
		t.Errorf("expected ErrTimeout, got %s", classRes.Category)
	}
	if !classRes.TransportEvidenceKnown {
		t.Errorf("expected TransportEvidenceKnown == true")
	}
	if !classRes.TransportOK {
		t.Errorf("expected TransportOK == true because Stage 1 succeeded")
	}
}

// TestTwoStage_H_ParentCancellation_DuringStage1 proves that when the parent context
// is cancelled during Stage 1:
// 1. Stage 2 never executes.
// 2. StatusInconclusive with ErrTimeout is reported.
// 3. Known=true, TransportOK=false.
func TestTwoStage_H_ParentCancellation_DuringStage1(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			time.Sleep(2 * time.Second)
		}
	}()

	var geminiCalls int64
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&geminiCalls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer geminiSrv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	cand := newDirectCandidate("probe-parent-cancel")
	cfg := &config.TestConfig{
		HealthURL:     fmt.Sprintf("http://%s/generate_204", l.Addr().String()),
		HealthTimeout: 2 * time.Second,
		TargetURL:     geminiSrv.URL,
		Timeout:       3 * time.Second,
		DialTimeout:   1 * time.Second,
	}

	classRes, _, _ := executeAttempt(ctx, cand, cfg)

	if atomic.LoadInt64(&geminiCalls) != 0 {
		t.Fatalf("Stage 2 executed despite parent cancellation; geminiCalls=%d", geminiCalls)
	}
	if classRes.Status != store.StatusInconclusive {
		t.Errorf("expected StatusInconclusive, got %s", classRes.Status)
	}
	if classRes.Category != store.ErrTimeout {
		t.Errorf("expected ErrTimeout, got %s", classRes.Category)
	}
	if !classRes.TransportEvidenceKnown {
		t.Errorf("expected TransportEvidenceKnown == true")
	}
	if classRes.TransportOK {
		t.Errorf("expected TransportOK == false")
	}
}

// TestTwoStage_H2_ParentDeadlineExceeded_DuringStage1 proves that when the parent context
// deadline expires during Stage 1:
// 1. Stage 2 never executes.
// 2. StatusInconclusive with ErrTimeout is reported.
// 3. Known=true, TransportOK=false.
func TestTwoStage_H2_ParentDeadlineExceeded_DuringStage1(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			time.Sleep(2 * time.Second)
		}
	}()

	var geminiCalls int64
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&geminiCalls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer geminiSrv.Close()

	// Parent context expires before HealthTimeout
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	cand := newDirectCandidate("probe-parent-deadline")
	cfg := &config.TestConfig{
		HealthURL:     fmt.Sprintf("http://%s/generate_204", l.Addr().String()),
		HealthTimeout: 2 * time.Second,
		TargetURL:     geminiSrv.URL,
		Timeout:       3 * time.Second,
		DialTimeout:   1 * time.Second,
	}

	classRes, retryable, _ := executeAttempt(ctx, cand, cfg)

	if atomic.LoadInt64(&geminiCalls) != 0 {
		t.Fatalf("Stage 2 executed despite parent deadline expiration; geminiCalls=%d", geminiCalls)
	}
	if classRes.Status != store.StatusInconclusive {
		t.Errorf("expected StatusInconclusive on parent deadline exceeded, got %s", classRes.Status)
	}
	if classRes.Category != store.ErrTimeout {
		t.Errorf("expected ErrTimeout, got %s", classRes.Category)
	}
	if !classRes.TransportEvidenceKnown {
		t.Errorf("expected TransportEvidenceKnown == true")
	}
	if classRes.TransportOK {
		t.Errorf("expected TransportOK == false")
	}
	if retryable {
		t.Errorf("parent deadline expiration should not be retryable")
	}
}

// TestTwoStage_I_OneTokenPerAttempt proves that rate limiting consumes exactly
// 1 token per attempt, regardless of whether Stage 1 succeeds or fails early.
func TestTwoStage_I_OneTokenPerAttempt(t *testing.T) {
	cand := newDirectCandidate("probe-rate-limit")

	t.Run("Stage 1 success -> Stage 2: exactly 1 token", func(t *testing.T) {
		healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		defer healthSrv.Close()

		geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Server", "ESF")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>Google Gemini</title></head><body><div id="bardchatui"></div></body></html>`))
		}))
		defer geminiSrv.Close()

		cfg := &config.TestConfig{
			HealthURL:     healthSrv.URL + "/generate_204",
			HealthTimeout: 1 * time.Second,
			TargetURL:     geminiSrv.URL,
			Timeout:       2 * time.Second,
			DialTimeout:   1 * time.Second,
			MaxRetries:    0,
		}

		limiter := rate.NewLimiter(rate.Every(time.Hour), 1) // Exactly 1 token
		res := Probe(context.Background(), cand, cfg, limiter)

		if res.Status != store.StatusPassed {
			t.Fatalf("expected StatusPassed, got %s", res.Status)
		}
		if limiter.Tokens() >= 1.0 {
			t.Errorf("expected token to be consumed by probe attempt; remaining tokens=%f", limiter.Tokens())
		}
	})

	t.Run("Stage 1 failure -> early exit: exactly 1 token", func(t *testing.T) {
		closedPort := getClosedPort(t)
		cfg := &config.TestConfig{
			HealthURL:     fmt.Sprintf("http://127.0.0.1:%d/generate_204", closedPort),
			HealthTimeout: 1 * time.Second,
			TargetURL:     "http://127.0.0.1:1/",
			Timeout:       2 * time.Second,
			DialTimeout:   1 * time.Second,
			MaxRetries:    0,
		}

		limiter := rate.NewLimiter(rate.Every(time.Hour), 1) // Exactly 1 token
		res := Probe(context.Background(), cand, cfg, limiter)

		if res.Status != store.StatusFailed {
			t.Fatalf("expected StatusFailed, got %s", res.Status)
		}
		if limiter.Tokens() >= 1.0 {
			t.Errorf("expected token to be consumed even on early exit; remaining tokens=%f", limiter.Tokens())
		}
	})
}

// TestTwoStage_J_AttemptLifecycle_SequentialExecution proves that a single candidate attempt
// executes Stage 1 (health check) followed immediately by Stage 2 (Gemini application probe)
// in exact sequence against the target server over the dialed outbound path, observing:
// 1. Exact path request sequence: /generate_204 followed by /
// 2. Exactly 2 inbound connections accepted on the server (one per stage, with no connection leaks or redundant retries)
// 3. StatusPassed with explicit TransportEvidenceKnown=true and TransportOK=true
func TestTwoStage_J_AttemptLifecycle_SequentialExecution(t *testing.T) {
	var (
		mu    sync.Mutex
		paths []string
		conns int64
	)

	// A single server handling both Health and Gemini endpoints
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()

		if r.URL.Path == "/generate_204" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Server", "ESF")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Google Gemini</title></head><body><div id="bardchatui"></div></body></html>`))
	}))
	srv.Config.ConnState = func(c net.Conn, s http.ConnState) {
		if s == http.StateNew {
			atomic.AddInt64(&conns, 1)
		}
	}
	srv.Start()
	defer srv.Close()

	cand := newDirectCandidate("probe-shared-box")
	cfg := &config.TestConfig{
		HealthURL:     srv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     srv.URL + "/",
		Timeout:       2 * time.Second,
		DialTimeout:   1 * time.Second,
	}

	classRes, retryable, _ := executeAttempt(context.Background(), cand, cfg)

	if classRes.Status != store.StatusPassed {
		t.Fatalf("expected StatusPassed, got %s (reason=%q)", classRes.Status, classRes.Reason)
	}
	if !classRes.TransportEvidenceKnown || !classRes.TransportOK {
		t.Fatalf("expected Known=true and TransportOK=true")
	}
	if retryable {
		t.Errorf("successful attempt should not be retryable")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 2 || paths[0] != "/generate_204" || paths[1] != "/" {
		t.Fatalf("unexpected request sequence: %v; want [/generate_204 /]", paths)
	}
	if accepted := atomic.LoadInt64(&conns); accepted != 2 {
		t.Errorf("expected exactly 2 connections dialed during the attempt (one per stage), got %d", accepted)
	}
}

// TestTwoStage_J_SharedDialer_SingleBox preserves the original test entry point,
// delegating to TestTwoStage_J_AttemptLifecycle_SequentialExecution.
func TestTwoStage_J_SharedDialer_SingleBox(t *testing.T) {
	TestTwoStage_J_AttemptLifecycle_SequentialExecution(t)
}

// TestTwoStage_K_RetrySemantics_FreshBoxPerAttempt verifies that retries
// restart from Stage 1, create a fresh Box per candidate attempt, and honor backoff.
func TestTwoStage_K_RetrySemantics_FreshBoxPerAttempt(t *testing.T) {
	var stage1Attempts int64
	var stage2Attempts int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/generate_204" {
			atomic.AddInt64(&stage1Attempts, 1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Gemini stage
		att := atomic.AddInt64(&stage2Attempts, 1)
		if att == 1 {
			// First attempt fails with retryable proxy 503
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// Second attempt passes
		w.Header().Set("Server", "ESF")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Google Gemini</title></head><body><div id="bardchatui"></div></body></html>`))
	}))
	defer srv.Close()

	cand := newDirectCandidate("probe-fresh-box-retry")
	cfg := &config.TestConfig{
		HealthURL:     srv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     srv.URL + "/",
		Timeout:       2 * time.Second,
		DialTimeout:   1 * time.Second,
		MaxRetries:    1,
		RetryBackoff:  10 * time.Millisecond,
	}

	res := Probe(context.Background(), cand, cfg, nil)

	if atomic.LoadInt64(&stage1Attempts) != 2 {
		t.Errorf("expected Stage 1 to be executed exactly 2 times (once per attempt); got %d", stage1Attempts)
	}
	if atomic.LoadInt64(&stage2Attempts) != 2 {
		t.Errorf("expected Stage 2 to be executed exactly 2 times; got %d", stage2Attempts)
	}
	if res.Status != store.StatusPassed {
		t.Errorf("expected StatusPassed on retry, got %s (reason=%q)", res.Status, res.Reason)
	}
	if res.Attempts != 2 {
		t.Errorf("expected 2 attempts recorded, got %d", res.Attempts)
	}
	if !res.TransportEvidenceKnown || !res.TransportOK {
		t.Errorf("expected Known=true and TransportOK=true")
	}
}

// TestTwoStage_L_Stage1_503_RetryAfter_Honored proves that when Stage 1 fails
// with HTTP 503 and a Retry-After header:
// 1. Retry-After is propagated to ProbeWithExecutor.
// 2. The retry delay honors the Retry-After backoff duration.
// 3. Stage 1 executes again on attempt 2 and, upon success, proceeds to Stage 2.
func TestTwoStage_L_Stage1_503_RetryAfter_Honored(t *testing.T) {
	var stage1Attempts int64
	var stage2Attempts int64

	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		att := atomic.AddInt64(&stage1Attempts, 1)
		if att == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthSrv.Close()

	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&stage2Attempts, 1)
		w.Header().Set("Server", "ESF")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Google Gemini</title></head><body><div id="bardchatui"></div></body></html>`))
	}))
	defer geminiSrv.Close()

	cand := newDirectCandidate("probe-stage1-503-retry")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     geminiSrv.URL,
		Timeout:       3 * time.Second,
		DialTimeout:   1 * time.Second,
		MaxRetries:    1,
		RetryBackoff:  10 * time.Millisecond,
	}

	start := time.Now()
	res := Probe(context.Background(), cand, cfg, nil)
	elapsed := time.Since(start)

	if atomic.LoadInt64(&stage1Attempts) != 2 {
		t.Errorf("expected Stage 1 to execute 2 times (initial + retry); got %d", stage1Attempts)
	}
	if atomic.LoadInt64(&stage2Attempts) != 1 {
		t.Errorf("expected Stage 2 to execute 1 time (only on attempt 2); got %d", stage2Attempts)
	}
	if res.Status != store.StatusPassed {
		t.Fatalf("expected StatusPassed on retry, got %s (reason=%q)", res.Status, res.Reason)
	}
	if !res.TransportEvidenceKnown || !res.TransportOK {
		t.Errorf("expected Known=true and TransportOK=true")
	}
	if elapsed < 900*time.Millisecond {
		t.Errorf("Stage 1 Retry-After delay was not honored; elapsed=%v < 900ms", elapsed)
	}
}

// TestTwoStage_M_Stage1_429_RetryAfter_Honored proves that when Stage 1 fails
// with HTTP 429 and a Retry-After header:
// 1. Retry-After is propagated to ProbeWithExecutor.
// 2. The retry delay honors the Retry-After backoff duration.
// 3. Stage 1 executes again on attempt 2 and, upon success, proceeds to Stage 2.
func TestTwoStage_M_Stage1_429_RetryAfter_Honored(t *testing.T) {
	var stage1Attempts int64
	var stage2Attempts int64

	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		att := atomic.AddInt64(&stage1Attempts, 1)
		if att == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthSrv.Close()

	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&stage2Attempts, 1)
		w.Header().Set("Server", "ESF")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Google Gemini</title></head><body><div id="bardchatui"></div></body></html>`))
	}))
	defer geminiSrv.Close()

	cand := newDirectCandidate("probe-stage1-429-retry")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     geminiSrv.URL,
		Timeout:       3 * time.Second,
		DialTimeout:   1 * time.Second,
		MaxRetries:    1,
		RetryBackoff:  10 * time.Millisecond,
	}

	start := time.Now()
	res := Probe(context.Background(), cand, cfg, nil)
	elapsed := time.Since(start)

	if atomic.LoadInt64(&stage1Attempts) != 2 {
		t.Errorf("expected Stage 1 to execute 2 times (initial + retry); got %d", stage1Attempts)
	}
	if atomic.LoadInt64(&stage2Attempts) != 1 {
		t.Errorf("expected Stage 2 to execute 1 time (only on attempt 2); got %d", stage2Attempts)
	}
	if res.Status != store.StatusPassed {
		t.Fatalf("expected StatusPassed on retry, got %s (reason=%q)", res.Status, res.Reason)
	}
	if !res.TransportEvidenceKnown || !res.TransportOK {
		t.Errorf("expected Known=true and TransportOK=true")
	}
	if elapsed < 900*time.Millisecond {
		t.Errorf("Stage 1 Retry-After delay was not honored; elapsed=%v < 900ms", elapsed)
	}
}

// TestTwoStage_N_Stage1_NoSecondaryRetryLoopInExecuteAttempt proves that executeAttempt
// executes Stage 1 exactly once and does NOT contain an inner retry loop when Stage 1 fails.
func TestTwoStage_N_Stage1_NoSecondaryRetryLoopInExecuteAttempt(t *testing.T) {
	var stage1Calls int64
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&stage1Calls, 1)
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer healthSrv.Close()

	cand := newDirectCandidate("probe-no-secondary-loop")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     "http://127.0.0.1:1/",
		Timeout:       2 * time.Second,
		DialTimeout:   1 * time.Second,
	}

	classRes, retryable, retryAfter := executeAttempt(context.Background(), cand, cfg)

	if atomic.LoadInt64(&stage1Calls) != 1 {
		t.Fatalf("executeAttempt performed %d Stage 1 calls; expected exactly 1 (no inner retry loop)", stage1Calls)
	}
	if !retryable {
		t.Errorf("expected 503 to be retryable at orchestrator level")
	}
	if retryAfter == nil || *retryAfter != 2*time.Second {
		t.Errorf("expected 2s RetryAfter returned by executeAttempt, got %v", retryAfter)
	}
	if classRes.Status != store.StatusInconclusive {
		t.Errorf("expected StatusInconclusive, got %s", classRes.Status)
	}
	if classRes.Category != store.ErrProxyError {
		t.Errorf("expected ErrProxyError, got %s", classRes.Category)
	}
	if !classRes.TransportEvidenceKnown || classRes.TransportOK {
		t.Errorf("expected Known=true, TransportOK=false; got Known=%v, OK=%v",
			classRes.TransportEvidenceKnown, classRes.TransportOK)
	}
}

// TestTwoStage_O_Stage1_403_GenericTransportFailure proves that when Stage 1 health endpoint
// returns HTTP 403:
// 1. Stage 2 is skipped (never executed).
// 2. Result is classified as a generic transport failure (ErrProxyError), NOT as ErrTargetDenied.
// 3. Status is StatusFailed, Retryable is false.
// 4. TransportEvidenceKnown=true, TransportOK=false.
func TestTwoStage_O_Stage1_403_GenericTransportFailure(t *testing.T) {
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer healthSrv.Close()

	var geminiCalls int64
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&geminiCalls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer geminiSrv.Close()

	cand := newDirectCandidate("probe-stage1-403")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     geminiSrv.URL,
		Timeout:       2 * time.Second,
		DialTimeout:   1 * time.Second,
	}

	classRes, retryable, _ := executeAttempt(context.Background(), cand, cfg)

	if atomic.LoadInt64(&geminiCalls) != 0 {
		t.Fatalf("Stage 2 executed despite Stage 1 HTTP 403; geminiCalls=%d", geminiCalls)
	}
	if classRes.Status != store.StatusFailed {
		t.Errorf("expected StatusFailed, got %s", classRes.Status)
	}
	if classRes.Category == store.ErrTargetDenied {
		t.Errorf("Stage 1 HTTP 403 must NOT be classified as ErrTargetDenied (target compatibility)")
	}
	if classRes.Category != store.ErrProxyError {
		t.Errorf("expected Category == ErrProxyError for Stage 1 HTTP 403, got %s", classRes.Category)
	}
	if classRes.StatusCode != 403 {
		t.Errorf("expected StatusCode 403, got %d", classRes.StatusCode)
	}
	if !classRes.TransportEvidenceKnown {
		t.Errorf("expected TransportEvidenceKnown == true")
	}
	if classRes.TransportOK {
		t.Errorf("expected TransportOK == false on Stage 1 HTTP 403")
	}
	if retryable {
		t.Errorf("HTTP 403 should not be retryable")
	}
}

// TestTwoStage_P_AttemptTimeoutShorterThanHealthTimeout_DuringStage1 proves that when
// cfg.Timeout expires while Stage 1 is in-flight (shorter than HealthTimeout) while outer ctx is alive:
// 1. Result is StatusInconclusive with ErrTimeout (attributed to attempt-parent timeout, not health timeout).
// 2. Retryable is false.
// 3. TransportEvidenceKnown=true, TransportOK=false.
// 4. Stage 2 must not execute.
func TestTwoStage_P_AttemptTimeoutShorterThanHealthTimeout_DuringStage1(t *testing.T) {
	// Listener that accepts connections but stalls indefinitely
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			time.Sleep(2 * time.Second)
		}
	}()

	var geminiCalls int64
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&geminiCalls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer geminiSrv.Close()

	// Outer context remains alive
	outerCtx := context.Background()

	cand := newDirectCandidate("probe-attempt-timeout-shorter")
	cfg := &config.TestConfig{
		HealthURL:     fmt.Sprintf("http://%s/generate_204", l.Addr().String()),
		HealthTimeout: 2 * time.Second, // Much longer than cfg.Timeout
		TargetURL:     geminiSrv.URL,
		Timeout:       50 * time.Millisecond, // attemptCtx expires first!
		DialTimeout:   1 * time.Second,
	}

	start := time.Now()
	classRes, retryable, _ := executeAttempt(outerCtx, cand, cfg)
	elapsed := time.Since(start)

	if atomic.LoadInt64(&geminiCalls) != 0 {
		t.Fatalf("Stage 2 executed despite attemptCtx expiration; geminiCalls=%d", geminiCalls)
	}
	if elapsed >= 1*time.Second {
		t.Fatalf("attempt was not bounded by cfg.Timeout (50ms); elapsed=%v", elapsed)
	}
	if classRes.Status != store.StatusInconclusive {
		t.Errorf("expected StatusInconclusive when attemptCtx expires during Stage 1, got %s", classRes.Status)
	}
	if classRes.Category != store.ErrTimeout {
		t.Errorf("expected ErrTimeout, got %s", classRes.Category)
	}
	if !classRes.TransportEvidenceKnown {
		t.Errorf("expected TransportEvidenceKnown == true")
	}
	if classRes.TransportOK {
		t.Errorf("expected TransportOK == false")
	}
	if retryable {
		t.Errorf("timeout should not be retryable")
	}
}
