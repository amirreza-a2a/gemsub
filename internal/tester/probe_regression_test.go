package tester

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"

	"gemsub/internal/config"
	"gemsub/internal/parser"
	"gemsub/internal/store"
)

func getClosedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// --- P1-1: Retry-After regression tests ---

// TestExecuteAttempt_RetryAfterPreserved_Seconds verifies that Retry-After in
// integer seconds from an HTTP 429 response is parsed and returned by executeAttempt.
func TestExecuteAttempt_RetryAfterPreserved_Seconds(t *testing.T) {
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthSrv.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	cand := parser.Candidate{
		Link: "vmess://direct-test",
		Outbound: option.Outbound{
			Type: "direct",
			Tag:  "probe",
		},
	}

	cfg := &config.TestConfig{
		HealthURL:   healthSrv.URL + "/generate_204",
		TargetURL:   srv.URL,
		Timeout:     2 * time.Second,
		DialTimeout: 1 * time.Second,
	}

	classRes, retryable, retryAfter := executeAttempt(context.Background(), cand, cfg)

	if classRes.Status != store.StatusInconclusive {
		t.Errorf("expected StatusInconclusive on 429, got %s", classRes.Status)
	}
	if classRes.Category != store.ErrTargetRateLimited {
		t.Errorf("expected ErrTargetRateLimited on 429, got %s", classRes.Category)
	}
	if !retryable {
		t.Errorf("expected 429 to be retryable")
	}
	if retryAfter == nil {
		t.Fatalf("REGRESSION P1-1: retryAfter is nil, expected 7s parsed from Retry-After header")
	}
	if *retryAfter != 7*time.Second {
		t.Errorf("expected retryAfter = 7s, got %v", *retryAfter)
	}
}

// TestExecuteAttempt_RetryAfterPreserved_HttpDate verifies that Retry-After in
// HTTP-date format (RFC 1123) is parsed and returned by executeAttempt.
func TestExecuteAttempt_RetryAfterPreserved_HttpDate(t *testing.T) {
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthSrv.Close()

	targetTime := time.Now().Add(10 * time.Second).UTC()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", targetTime.Format(http.TimeFormat))
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	cand := parser.Candidate{
		Link: "vmess://direct-test",
		Outbound: option.Outbound{
			Type: "direct",
			Tag:  "probe",
		},
	}

	cfg := &config.TestConfig{
		HealthURL:   healthSrv.URL + "/generate_204",
		TargetURL:   srv.URL,
		Timeout:     2 * time.Second,
		DialTimeout: 1 * time.Second,
	}

	_, retryable, retryAfter := executeAttempt(context.Background(), cand, cfg)

	if !retryable {
		t.Errorf("expected 429 to be retryable")
	}
	if retryAfter == nil {
		t.Fatalf("REGRESSION P1-1: retryAfter is nil for HTTP-date")
	}
	if *retryAfter < 8*time.Second || *retryAfter > 12*time.Second {
		t.Errorf("expected retryAfter ~10s, got %v", *retryAfter)
	}
}

// TestExecuteAttempt_RetryAfter_NilWhenMissing verifies that 429 without Retry-After
// returns nil for retryAfter.
func TestExecuteAttempt_RetryAfter_NilWhenMissing(t *testing.T) {
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthSrv.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	cand := parser.Candidate{
		Link: "vmess://direct-test",
		Outbound: option.Outbound{
			Type: "direct",
			Tag:  "probe",
		},
	}

	cfg := &config.TestConfig{
		HealthURL:   healthSrv.URL + "/generate_204",
		TargetURL:   srv.URL,
		Timeout:     2 * time.Second,
		DialTimeout: 1 * time.Second,
	}

	_, retryable, retryAfter := executeAttempt(context.Background(), cand, cfg)

	if !retryable {
		t.Errorf("expected 429 to be retryable")
	}
	if retryAfter != nil {
		t.Errorf("expected nil retryAfter when header missing, got %v", retryAfter)
	}
}

// TestProbeCandidate_HonorsRetryAfterBackoff verifies end-to-end that ProbeCandidate
// with real executeAttempt actually honors the parsed Retry-After delay during retries.
func TestProbeCandidate_HonorsRetryAfterBackoff(t *testing.T) {
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthSrv.Close()

	var attempts int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		att := atomic.AddInt64(&attempts, 1)
		if att == 1 {
			// First attempt: 429 with 1 second Retry-After
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		// Second attempt: Success with Gemini markers
		w.Header().Set("Server", "ESF")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Google Gemini</title></head><body><div id="bardchatui"></div></body></html>`))
	}))
	defer srv.Close()

	cand := parser.Candidate{
		Link: "vmess://direct-test",
		Outbound: option.Outbound{
			Type: "direct",
			Tag:  "probe",
		},
	}

	cfg := &config.TestConfig{
		HealthURL:    healthSrv.URL + "/generate_204",
		TargetURL:    srv.URL,
		Timeout:      3 * time.Second,
		DialTimeout:  1 * time.Second,
		MaxRetries:   1,
		RetryBackoff: 10 * time.Millisecond, // Much smaller than 1s Retry-After
	}

	start := time.Now()
	res := Probe(context.Background(), cand, cfg, nil)
	elapsed := time.Since(start)

	if attempts != 2 {
		t.Fatalf("expected exactly 2 attempts, got %d", attempts)
	}
	if res.Status != store.StatusPassed {
		t.Fatalf("expected StatusPassed after retry, got %s (reason=%q)", res.Status, res.Reason)
	}
	// The delay should have honored Retry-After: 1s, meaning total time >= ~900ms
	if elapsed < 900*time.Millisecond {
		t.Fatalf("REGRESSION P1-1: ProbeCandidate did not honor Retry-After backoff; elapsed=%v < 900ms", elapsed)
	}
}

// --- P1-2: Transport error classification regression tests ---

// TestExecuteAttempt_TransportErrorsNotCollapsed verifies that network/dial failures
// during executeAttempt are properly classified by ClassifyDialError into their canonical
// categories rather than being collapsed into ErrProxyError with Retryable=false.
func TestExecuteAttempt_TransportErrorsNotCollapsed(t *testing.T) {
	t.Run("connection refused -> ErrConnRefused", func(t *testing.T) {
		closedPort := getClosedPort(t)
		cand := parser.Candidate{
			Link: "vmess://direct-test",
			Outbound: option.Outbound{
				Type: "direct",
				Tag:  "probe",
			},
		}
		cfg := &config.TestConfig{
			HealthURL:   fmt.Sprintf("http://127.0.0.1:%d/", closedPort),
			TargetURL:   fmt.Sprintf("http://127.0.0.1:%d/", closedPort),
			Timeout:     2 * time.Second,
			DialTimeout: 1 * time.Second,
		}

		classRes, retryable, _ := executeAttempt(context.Background(), cand, cfg)

		if classRes.Category != store.ErrConnRefused {
			t.Fatalf("REGRESSION P1-2: expected ErrConnRefused for connection refused, got category=%s (reason=%q)",
				classRes.Category, classRes.Reason)
		}
		if classRes.Status != store.StatusFailed {
			t.Errorf("expected StatusFailed, got %s", classRes.Status)
		}
		if retryable {
			t.Errorf("connection refused should not be retryable")
		}
	})

	t.Run("dial timeout -> ErrTimeout", func(t *testing.T) {
		// Listen and accept but never write or close
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
				time.Sleep(1 * time.Second)
			}
		}()

		cand := parser.Candidate{
			Link: "vmess://direct-test",
			Outbound: option.Outbound{
				Type: "direct",
				Tag:  "probe",
			},
		}
		cfg := &config.TestConfig{
			HealthURL:     fmt.Sprintf("http://%s/", l.Addr().String()),
			HealthTimeout: 50 * time.Millisecond,
			TargetURL:     fmt.Sprintf("http://%s/", l.Addr().String()),
			Timeout:       100 * time.Millisecond,
			DialTimeout:   50 * time.Millisecond,
		}

		classRes, retryable, _ := executeAttempt(context.Background(), cand, cfg)

		if classRes.Category != store.ErrTimeout {
			t.Fatalf("REGRESSION P1-2: expected ErrTimeout for timeout, got category=%s (reason=%q)",
				classRes.Category, classRes.Reason)
		}
		if classRes.Status != store.StatusFailed {
			t.Errorf("expected StatusFailed, got %s", classRes.Status)
		}
		if retryable {
			t.Errorf("timeout should not be retryable")
		}
	})

	t.Run("context canceled -> ErrTimeout with StatusInconclusive", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel before executeAttempt starts

		cand := parser.Candidate{
			Link: "vmess://direct-test",
			Outbound: option.Outbound{
				Type: "direct",
				Tag:  "probe",
			},
		}
		cfg := &config.TestConfig{
			HealthURL:   "http://127.0.0.1:1/",
			TargetURL:   "http://127.0.0.1:1/",
			Timeout:     2 * time.Second,
			DialTimeout: 1 * time.Second,
		}

		classRes, retryable, _ := executeAttempt(ctx, cand, cfg)

		if classRes.Category != store.ErrTimeout {
			t.Fatalf("REGRESSION P1-2: expected ErrTimeout for context canceled, got category=%s", classRes.Category)
		}
		if classRes.Status != store.StatusInconclusive {
			t.Errorf("expected StatusInconclusive for context canceled, got %s", classRes.Status)
		}
		if retryable {
			t.Errorf("context canceled should not be retryable")
		}
	})

	t.Run("connection reset / EOF -> ErrReset", func(t *testing.T) {
		// TCP listener that accepts and immediately closes the connection
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
				// Immediately close without sending HTTP response
				conn.Close()
			}
		}()

		cand := parser.Candidate{
			Link: "vmess://direct-test",
			Outbound: option.Outbound{
				Type: "direct",
				Tag:  "probe",
			},
		}
		cfg := &config.TestConfig{
			HealthURL:   fmt.Sprintf("http://%s/", l.Addr().String()),
			TargetURL:   fmt.Sprintf("http://%s/", l.Addr().String()),
			Timeout:     2 * time.Second,
			DialTimeout: 1 * time.Second,
		}

		classRes, retryable, _ := executeAttempt(context.Background(), cand, cfg)

		if classRes.Category != store.ErrReset {
			t.Fatalf("REGRESSION P1-2: expected ErrReset for EOF/reset, got category=%s (reason=%q)",
				classRes.Category, classRes.Reason)
		}
		if classRes.Status != store.StatusFailed {
			t.Errorf("expected StatusFailed, got %s", classRes.Status)
		}
		if retryable {
			t.Errorf("connection reset should not be retryable")
		}
	})
}

// TestProbeWithExecutor_PropagatesExplicitTransportEvidence verifies that ProbeWithExecutor
// propagates TransportOK and TransportLatency from ClassificationResult to store.Result.
func TestProbeWithExecutor_PropagatesExplicitTransportEvidence(t *testing.T) {
	cand := parser.Candidate{Link: "vless://test-node"}
	cfg := &config.TestConfig{MaxRetries: 0}

	// Case 1: Explicit TransportOK=true
	execSuccess := func(ctx context.Context, c parser.Candidate, conf *config.TestConfig) (ClassificationResult, bool, *time.Duration) {
		return ClassificationResult{
			Status:                 store.StatusPassed,
			Category:               store.ErrNone,
			TransportOK:            true,
			TransportLatency:       42 * time.Millisecond,
			TransportEvidenceKnown: true,
		}, false, nil
	}

	resSuccess := ProbeWithExecutor(context.Background(), cand, cfg, nil, execSuccess)
	if !resSuccess.TransportOK {
		t.Errorf("expected resSuccess.TransportOK == true")
	}
	if !resSuccess.TransportEvidenceKnown {
		t.Errorf("expected resSuccess.TransportEvidenceKnown == true")
	}
	if resSuccess.TransportLatency != 42*time.Millisecond {
		t.Errorf("expected resSuccess.TransportLatency == 42ms, got %v", resSuccess.TransportLatency)
	}

	// Case 2: Explicit TransportOK=false
	execFailure := func(ctx context.Context, c parser.Candidate, conf *config.TestConfig) (ClassificationResult, bool, *time.Duration) {
		return ClassificationResult{
			Status:                 store.StatusFailed,
			Category:               store.ErrConnRefused,
			TransportOK:            false,
			TransportLatency:       15 * time.Millisecond,
			TransportEvidenceKnown: true,
		}, false, nil
	}

	resFailure := ProbeWithExecutor(context.Background(), cand, cfg, nil, execFailure)
	if resFailure.TransportOK {
		t.Errorf("expected resFailure.TransportOK == false")
	}
	if !resFailure.TransportEvidenceKnown {
		t.Errorf("expected resFailure.TransportEvidenceKnown == true")
	}
	if resFailure.TransportLatency != 15*time.Millisecond {
		t.Errorf("expected resFailure.TransportLatency == 15ms, got %v", resFailure.TransportLatency)
	}
}
