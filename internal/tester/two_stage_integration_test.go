package tester_test

import (
	"context"
	"fmt"
	"io"
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
	"gemsub/internal/tester"
	"gemsub/internal/tester/transport"
)

func newDirectCandidate(tag string) parser.Candidate {
	return parser.Candidate{
		Link: "vmess://" + tag,
		Outbound: option.Outbound{
			Type: "direct",
			Tag:  "probe",
		},
	}
}

func getClosedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen for free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// ---------------------------------------------------------------------------
// Acceptance Criteria §9: Fundamental State Matrix A–H Integration Tests
// ---------------------------------------------------------------------------

// TestTwoStage_StateA_TransportFailure_ConnectionRefused verifies State A:
// Transport fails (connection refused) -> Gemini receives 0 requests.
// Outcome: StatusFailed, ErrConnRefused, Known=true, TransportOK=false.
// Store: Excluded from both Passing() and NetworkPassing().
func TestTwoStage_StateA_TransportFailure_ConnectionRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var geminiCalls int64
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&geminiCalls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer geminiSrv.Close()

	closedPort := getClosedPort(t)
	cand := newDirectCandidate("state-a-refused")
	cfg := &config.TestConfig{
		HealthURL:     fmt.Sprintf("http://127.0.0.1:%d/generate_204", closedPort),
		HealthTimeout: 1 * time.Second,
		TargetURL:     geminiSrv.URL,
		Timeout:       2 * time.Second,
		DialTimeout:   1 * time.Second,
		MaxRetries:    0,
	}

	res := tester.Probe(ctx, cand, cfg, nil)

	if calls := atomic.LoadInt64(&geminiCalls); calls != 0 {
		t.Fatalf("Stage 2 (Gemini) called %d times on Stage 1 transport failure; want 0", calls)
	}
	if res.Status != store.StatusFailed {
		t.Errorf("Status = %s; want %s", res.Status, store.StatusFailed)
	}
	if res.Category != store.ErrConnRefused {
		t.Errorf("Category = %s; want %s", res.Category, store.ErrConnRefused)
	}
	if !res.TransportEvidenceKnown {
		t.Errorf("TransportEvidenceKnown = false; want true")
	}
	if res.TransportOK {
		t.Errorf("TransportOK = true; want false")
	}

	st := store.NewWithConfig("", store.DefaultScoringConfig())
	st.PutWithTransition(res)

	if len(st.Passing()) != 0 {
		t.Errorf("Store.Passing() = %v; want empty", st.Passing())
	}
	if len(st.NetworkPassing()) != 0 {
		t.Errorf("Store.NetworkPassing() = %v; want empty", st.NetworkPassing())
	}
}

// TestTwoStage_StateA_TransportFailure_Timeout verifies State A (timeout variant):
// Transport times out -> Gemini receives 0 requests.
// Outcome: StatusFailed, ErrTimeout, Known=true, TransportOK=false.
// Store: Excluded from both Passing() and NetworkPassing().
func TestTwoStage_StateA_TransportFailure_Timeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer healthSrv.Close()

	var geminiCalls int64
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&geminiCalls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer geminiSrv.Close()

	cand := newDirectCandidate("state-a-timeout")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 100 * time.Millisecond,
		TargetURL:     geminiSrv.URL,
		Timeout:       500 * time.Millisecond,
		DialTimeout:   50 * time.Millisecond,
		MaxRetries:    0,
	}

	res := tester.Probe(ctx, cand, cfg, nil)

	if calls := atomic.LoadInt64(&geminiCalls); calls != 0 {
		t.Fatalf("Stage 2 (Gemini) called %d times on Stage 1 transport timeout; want 0", calls)
	}
	if res.Status != store.StatusFailed {
		t.Errorf("Status = %s; want %s", res.Status, store.StatusFailed)
	}
	if res.Category != store.ErrTimeout {
		t.Errorf("Category = %s; want %s", res.Category, store.ErrTimeout)
	}
	if !res.TransportEvidenceKnown {
		t.Errorf("TransportEvidenceKnown = false; want true")
	}
	if res.TransportOK {
		t.Errorf("TransportOK = true; want false")
	}

	st := store.NewWithConfig("", store.DefaultScoringConfig())
	st.PutWithTransition(res)

	if len(st.Passing()) != 0 {
		t.Errorf("Store.Passing() = %v; want empty", st.Passing())
	}
	if len(st.NetworkPassing()) != 0 {
		t.Errorf("Store.NetworkPassing() = %v; want empty", st.NetworkPassing())
	}
}

// TestTwoStage_StateB_TransportPass_GeminiPass verifies State B:
// Transport returns 204; Gemini returns 200 with valid markers.
// Outcome: StatusPassed, ErrNone, Known=true, TransportOK=true.
// Store: Present in both Passing() and NetworkPassing().
func TestTwoStage_StateB_TransportPass_GeminiPass(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthSrv.Close()

	var geminiCalls int64
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&geminiCalls, 1)
		w.Header().Set("Server", "ESF")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Google Gemini</title></head><body><div id="bardchatui"><p>Gemini is ready</p></div></body></html>`))
	}))
	defer geminiSrv.Close()

	cand := newDirectCandidate("state-b-pass")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     geminiSrv.URL,
		Timeout:       2 * time.Second,
		DialTimeout:   1 * time.Second,
		MaxRetries:    0,
	}

	res := tester.Probe(ctx, cand, cfg, nil)

	if calls := atomic.LoadInt64(&geminiCalls); calls != 1 {
		t.Fatalf("Stage 2 called %d times; want 1", calls)
	}
	if res.Status != store.StatusPassed {
		t.Errorf("Status = %s; want %s", res.Status, store.StatusPassed)
	}
	if res.Category != store.ErrNone {
		t.Errorf("Category = %s; want %s", res.Category, store.ErrNone)
	}
	if !res.TransportEvidenceKnown {
		t.Errorf("TransportEvidenceKnown = false; want true")
	}
	if !res.TransportOK {
		t.Errorf("TransportOK = false; want true")
	}
	if res.TransportLatency <= 0 {
		t.Errorf("TransportLatency = %v; want > 0", res.TransportLatency)
	}

	st := store.NewWithConfig("", store.DefaultScoringConfig())
	st.PutWithTransition(res)

	passing := st.Passing()
	if len(passing) != 1 || passing[0] != cand.Link {
		t.Errorf("Store.Passing() = %v; want [%s]", passing, cand.Link)
	}
	netPassing := st.NetworkPassing()
	if len(netPassing) != 1 || netPassing[0] != cand.Link {
		t.Errorf("Store.NetworkPassing() = %v; want [%s]", netPassing, cand.Link)
	}
}

// TestTwoStage_StateC_TransportPass_GeminiRegionBlocked verifies State C:
// Transport returns 204; Gemini returns 200 with regional block phrase.
// Outcome: StatusFailed, ErrRegionBlocked, Known=true, TransportOK=true.
// Store: Excluded from Passing(), included in NetworkPassing().
func TestTwoStage_StateC_TransportPass_GeminiRegionBlocked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthSrv.Close()

	var geminiCalls int64
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&geminiCalls, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><html><body>Gemini isn't currently supported in your country</body></html>`))
	}))
	defer geminiSrv.Close()

	cand := newDirectCandidate("state-c-blocked")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     geminiSrv.URL,
		BlockPhrases:  []string{"isn't currently supported in your country"},
		Timeout:       2 * time.Second,
		DialTimeout:   1 * time.Second,
		MaxRetries:    0,
	}

	res := tester.Probe(ctx, cand, cfg, nil)

	if calls := atomic.LoadInt64(&geminiCalls); calls != 1 {
		t.Fatalf("Stage 2 called %d times; want 1", calls)
	}
	if res.Status != store.StatusFailed {
		t.Errorf("Status = %s; want %s", res.Status, store.StatusFailed)
	}
	if res.Category != store.ErrRegionBlocked {
		t.Errorf("Category = %s; want %s", res.Category, store.ErrRegionBlocked)
	}
	if !res.TransportEvidenceKnown {
		t.Errorf("TransportEvidenceKnown = false; want true")
	}
	if !res.TransportOK {
		t.Errorf("TransportOK = false; want true (Gemini region block must not invalidate transport health)")
	}

	st := store.NewWithConfig("", store.DefaultScoringConfig())
	st.PutWithTransition(res)

	if len(st.Passing()) != 0 {
		t.Errorf("Store.Passing() = %v; want empty for region-blocked candidate", st.Passing())
	}
	netPassing := st.NetworkPassing()
	if len(netPassing) != 1 || netPassing[0] != cand.Link {
		t.Errorf("Store.NetworkPassing() = %v; want [%s]", netPassing, cand.Link)
	}
}

// TestTwoStage_StateD_TransportPass_GeminiTargetDenied verifies State D:
// Transport returns 204; Gemini returns 403 Forbidden.
// Outcome: StatusFailed, ErrTargetDenied, StatusCode=403, Known=true, TransportOK=true.
// Store: Excluded from Passing(), included in NetworkPassing().
func TestTwoStage_StateD_TransportPass_GeminiTargetDenied(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthSrv.Close()

	var geminiCalls int64
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&geminiCalls, 1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer geminiSrv.Close()

	cand := newDirectCandidate("state-d-denied")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     geminiSrv.URL,
		Timeout:       2 * time.Second,
		DialTimeout:   1 * time.Second,
		MaxRetries:    0,
	}

	res := tester.Probe(ctx, cand, cfg, nil)

	if calls := atomic.LoadInt64(&geminiCalls); calls != 1 {
		t.Fatalf("Stage 2 called %d times; want 1", calls)
	}
	if res.Status != store.StatusFailed {
		t.Errorf("Status = %s; want %s", res.Status, store.StatusFailed)
	}
	if res.Category != store.ErrTargetDenied {
		t.Errorf("Category = %s; want %s", res.Category, store.ErrTargetDenied)
	}
	if res.StatusCode != 403 {
		t.Errorf("StatusCode = %d; want 403", res.StatusCode)
	}
	if !res.TransportEvidenceKnown {
		t.Errorf("TransportEvidenceKnown = false; want true")
	}
	if !res.TransportOK {
		t.Errorf("TransportOK = false; want true (Gemini 403 must not invalidate transport health)")
	}

	st := store.NewWithConfig("", store.DefaultScoringConfig())
	st.PutWithTransition(res)

	if len(st.Passing()) != 0 {
		t.Errorf("Store.Passing() = %v; want empty for target-denied candidate", st.Passing())
	}
	netPassing := st.NetworkPassing()
	if len(netPassing) != 1 || netPassing[0] != cand.Link {
		t.Errorf("Store.NetworkPassing() = %v; want [%s]", netPassing, cand.Link)
	}
}

// TestTwoStage_StateE_TransportPass_GeminiServerError verifies State E:
// Transport returns 204; Gemini returns 503 Service Unavailable.
// Outcome: StatusInconclusive, ErrTargetError, StatusCode=503, Known=true, TransportOK=true.
// Store: Excluded from Passing(), included in NetworkPassing().
func TestTwoStage_StateE_TransportPass_GeminiServerError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthSrv.Close()

	var geminiCalls int64
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&geminiCalls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer geminiSrv.Close()

	cand := newDirectCandidate("state-e-server-error")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     geminiSrv.URL,
		Timeout:       2 * time.Second,
		DialTimeout:   1 * time.Second,
		MaxRetries:    0,
	}

	res := tester.Probe(ctx, cand, cfg, nil)

	if calls := atomic.LoadInt64(&geminiCalls); calls != 1 {
		t.Fatalf("Stage 2 called %d times; want 1", calls)
	}
	if res.Status != store.StatusInconclusive {
		t.Errorf("Status = %s; want %s", res.Status, store.StatusInconclusive)
	}
	if res.Category != store.ErrTargetError {
		t.Errorf("Category = %s; want %s", res.Category, store.ErrTargetError)
	}
	if res.StatusCode != 503 {
		t.Errorf("StatusCode = %d; want 503", res.StatusCode)
	}
	if !res.TransportEvidenceKnown {
		t.Errorf("TransportEvidenceKnown = false; want true")
	}
	if !res.TransportOK {
		t.Errorf("TransportOK = false; want true (Gemini 503 must not invalidate transport health)")
	}

	st := store.NewWithConfig("", store.DefaultScoringConfig())
	st.PutWithTransition(res)

	if len(st.Passing()) != 0 {
		t.Errorf("Store.Passing() = %v; want empty for 503 server error candidate", st.Passing())
	}
	netPassing := st.NetworkPassing()
	if len(netPassing) != 1 || netPassing[0] != cand.Link {
		t.Errorf("Store.NetworkPassing() = %v; want [%s]", netPassing, cand.Link)
	}
}

// TestTwoStage_StateF_TransportPass_GeminiRateLimited verifies State F:
// Transport returns 204; Gemini returns 429 Too Many Requests.
// Outcome: StatusInconclusive, ErrTargetRateLimited, StatusCode=429, Known=true, TransportOK=true.
// Store: Excluded from Passing(), included in NetworkPassing().
func TestTwoStage_StateF_TransportPass_GeminiRateLimited(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthSrv.Close()

	var geminiCalls int64
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&geminiCalls, 1)
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer geminiSrv.Close()

	cand := newDirectCandidate("state-f-rate-limited")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     geminiSrv.URL,
		Timeout:       2 * time.Second,
		DialTimeout:   1 * time.Second,
		MaxRetries:    0,
	}

	res := tester.Probe(ctx, cand, cfg, nil)

	if calls := atomic.LoadInt64(&geminiCalls); calls != 1 {
		t.Fatalf("Stage 2 called %d times; want 1", calls)
	}
	if res.Status != store.StatusInconclusive {
		t.Errorf("Status = %s; want %s", res.Status, store.StatusInconclusive)
	}
	if res.Category != store.ErrTargetRateLimited {
		t.Errorf("Category = %s; want %s", res.Category, store.ErrTargetRateLimited)
	}
	if res.StatusCode != 429 {
		t.Errorf("StatusCode = %d; want 429", res.StatusCode)
	}
	if !res.TransportEvidenceKnown {
		t.Errorf("TransportEvidenceKnown = false; want true")
	}
	if !res.TransportOK {
		t.Errorf("TransportOK = false; want true")
	}

	st := store.NewWithConfig("", store.DefaultScoringConfig())
	st.PutWithTransition(res)

	if len(st.Passing()) != 0 {
		t.Errorf("Store.Passing() = %v; want empty for rate-limited candidate", st.Passing())
	}
	netPassing := st.NetworkPassing()
	if len(netPassing) != 1 || netPassing[0] != cand.Link {
		t.Errorf("Store.NetworkPassing() = %v; want [%s]", netPassing, cand.Link)
	}
}

// TestTwoStage_StateG_TransportPass_GeminiUnverifiedTargetOther verifies State G:
// Transport returns 204; Gemini returns 200 OK without Gemini origin / app markers (e.g. CDN challenge / blog page).
// Outcome: StatusFailed, ErrTargetOther, StatusCode=200, Known=true, TransportOK=true.
// Store: Excluded from Passing(), included in NetworkPassing().
func TestTwoStage_StateG_TransportPass_GeminiUnverifiedTargetOther(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthSrv.Close()

	var geminiCalls int64
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&geminiCalls, 1)
		w.Header().Set("Server", "cloudflare")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html><head><title>Unrelated Site</title></head><body><h1>Hello World</h1></body></html>`))
	}))
	defer geminiSrv.Close()

	cand := newDirectCandidate("state-g-target-other")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     geminiSrv.URL,
		Timeout:       2 * time.Second,
		DialTimeout:   1 * time.Second,
		MaxRetries:    0,
	}

	res := tester.Probe(ctx, cand, cfg, nil)

	if calls := atomic.LoadInt64(&geminiCalls); calls != 1 {
		t.Fatalf("Stage 2 called %d times; want 1", calls)
	}
	if res.Status != store.StatusFailed {
		t.Errorf("Status = %s; want %s", res.Status, store.StatusFailed)
	}
	if res.Category != store.ErrTargetOther {
		t.Errorf("Category = %s; want %s", res.Category, store.ErrTargetOther)
	}
	if res.StatusCode != 200 {
		t.Errorf("StatusCode = %d; want 200", res.StatusCode)
	}
	if !res.TransportEvidenceKnown {
		t.Errorf("TransportEvidenceKnown = false; want true")
	}
	if !res.TransportOK {
		t.Errorf("TransportOK = false; want true (Gemini target other must not invalidate transport health)")
	}

	st := store.NewWithConfig("", store.DefaultScoringConfig())
	st.PutWithTransition(res)

	if len(st.Passing()) != 0 {
		t.Errorf("Store.Passing() = %v; want empty for unverified candidate", st.Passing())
	}
	netPassing := st.NetworkPassing()
	if len(netPassing) != 1 || netPassing[0] != cand.Link {
		t.Errorf("Store.NetworkPassing() = %v; want [%s]", netPassing, cand.Link)
	}
}

// TestTwoStage_StateH_TransportPass_GeminiTimeout verifies State H:
// Transport returns 204; Gemini endpoint hangs and times out.
// Outcome: StatusInconclusive, ErrTimeout, Known=true, TransportOK=true.
// Store: Excluded from Passing(), included in NetworkPassing().
func TestTwoStage_StateH_TransportPass_GeminiTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthSrv.Close()

	var geminiCalls int64
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&geminiCalls, 1)
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer geminiSrv.Close()

	cand := newDirectCandidate("state-h-gemini-timeout")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 100 * time.Millisecond,
		TargetURL:     geminiSrv.URL,
		Timeout:       200 * time.Millisecond,
		DialTimeout:   50 * time.Millisecond,
		MaxRetries:    0,
	}

	res := tester.Probe(ctx, cand, cfg, nil)

	if calls := atomic.LoadInt64(&geminiCalls); calls != 1 {
		t.Fatalf("Stage 2 called %d times; want 1", calls)
	}
	if res.Status != store.StatusInconclusive {
		t.Errorf("Status = %s; want %s", res.Status, store.StatusInconclusive)
	}
	if res.Category != store.ErrTimeout {
		t.Errorf("Category = %s; want %s", res.Category, store.ErrTimeout)
	}
	if !res.TransportEvidenceKnown {
		t.Errorf("TransportEvidenceKnown = false; want true")
	}
	if !res.TransportOK {
		t.Errorf("TransportOK = false; want true (Gemini timeout must not invalidate healthy transport evidence)")
	}

	st := store.NewWithConfig("", store.DefaultScoringConfig())
	st.PutWithTransition(res)

	if len(st.Passing()) != 0 {
		t.Errorf("Store.Passing() = %v; want empty for Gemini-timeout candidate", st.Passing())
	}
	netPassing := st.NetworkPassing()
	if len(netPassing) != 1 || netPassing[0] != cand.Link {
		t.Errorf("Store.NetworkPassing() = %v; want [%s]", netPassing, cand.Link)
	}
}

// ---------------------------------------------------------------------------
// Acceptance Criteria §9: Transport Protocol Invariants
// ---------------------------------------------------------------------------

// TestTransportProtocol_RedirectProhibition verifies that:
// 1. Transport server HTTP 302 redirect is NOT followed.
// 2. Transport check fails with ErrProxyError.
// 3. Gemini endpoint receives exactly 0 requests.
// 4. TransportEvidenceKnown=true, TransportOK=false.
// 5. Candidate is excluded from Store.Passing() and Store.NetworkPassing().
func TestTransportProtocol_RedirectProhibition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var healthCalls int64
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&healthCalls, 1)
		http.Redirect(w, r, "/captive-portal-login", http.StatusFound)
	}))
	defer healthSrv.Close()

	var geminiCalls int64
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&geminiCalls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer geminiSrv.Close()

	cand := newDirectCandidate("redirect-prohibition")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     geminiSrv.URL,
		Timeout:       2 * time.Second,
		DialTimeout:   1 * time.Second,
		MaxRetries:    0,
	}

	res := tester.Probe(ctx, cand, cfg, nil)

	if calls := atomic.LoadInt64(&healthCalls); calls != 1 {
		t.Fatalf("Health server called %d times; want 1", calls)
	}
	if calls := atomic.LoadInt64(&geminiCalls); calls != 0 {
		t.Fatalf("Gemini server called %d times upon redirect; want 0", calls)
	}
	if res.Status != store.StatusFailed {
		t.Errorf("Status = %s; want %s", res.Status, store.StatusFailed)
	}
	if res.Category != store.ErrProxyError {
		t.Errorf("Category = %s; want %s", res.Category, store.ErrProxyError)
	}
	if !res.TransportEvidenceKnown {
		t.Errorf("TransportEvidenceKnown = false; want true")
	}
	if res.TransportOK {
		t.Errorf("TransportOK = true; want false on 302 redirect")
	}

	st := store.NewWithConfig("", store.DefaultScoringConfig())
	st.PutWithTransition(res)

	if len(st.Passing()) != 0 {
		t.Errorf("Store.Passing() = %v; want empty", st.Passing())
	}
	if len(st.NetworkPassing()) != 0 {
		t.Errorf("Store.NetworkPassing() = %v; want empty", st.NetworkPassing())
	}
}

type trackingReadCloser struct {
	readCount int32
	closed    bool
}

func (t *trackingReadCloser) Read(p []byte) (int, error) {
	atomic.AddInt32(&t.readCount, 1)
	return 0, io.EOF
}

func (t *trackingReadCloser) Close() error {
	t.closed = true
	return nil
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// TestTransportProtocol_204ZeroBodyRead verifies that when the transport endpoint returns HTTP 204:
// 1. Zero bytes/body read operations are performed on the response body.
// 2. The response body is properly closed.
// 3. The probe registers verified transport success.
func TestTransportProtocol_204ZeroBodyRead(t *testing.T) {
	body := &trackingReadCloser{}
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       body,
		}, nil
	})

	tr := transport.Probe(context.Background(), nil, transport.Config{
		HealthURL: "https://www.gstatic.com/generate_204",
		Transport: rt,
	})

	if !tr.OK {
		t.Fatalf("expected transport.Probe OK=true, got false (err=%v)", tr.Error)
	}
	if tr.StatusCode != http.StatusNoContent {
		t.Errorf("expected StatusCode=204, got %d", tr.StatusCode)
	}
	if reads := atomic.LoadInt32(&body.readCount); reads != 0 {
		t.Errorf("expected exactly 0 Read calls on HTTP 204 response body, got %d", reads)
	}
	if !body.closed {
		t.Errorf("expected response body to be closed")
	}
}

// ---------------------------------------------------------------------------
// Protocol and Semantic Boundary Regressions (§5 / §7)
// ---------------------------------------------------------------------------

// TestBoundary_Stage1_403_ProducesProxyError_NeverTargetDenied verifies that:
// An HTTP 403 returned by the Stage 1 transport probe is classified as ErrProxyError
// (never ErrTargetDenied), TransportOK=false, and Gemini receives 0 requests.
func TestBoundary_Stage1_403_ProducesProxyError_NeverTargetDenied(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

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

	cand := newDirectCandidate("stage1-403")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     geminiSrv.URL,
		Timeout:       2 * time.Second,
		DialTimeout:   1 * time.Second,
		MaxRetries:    0,
	}

	res := tester.Probe(ctx, cand, cfg, nil)

	if calls := atomic.LoadInt64(&geminiCalls); calls != 0 {
		t.Fatalf("Gemini server called %d times on Stage 1 403; want 0", calls)
	}
	if res.Status != store.StatusFailed {
		t.Errorf("Status = %s; want %s", res.Status, store.StatusFailed)
	}
	if res.Category != store.ErrProxyError {
		t.Errorf("Category = %s; want %s (must NOT be ErrTargetDenied)", res.Category, store.ErrProxyError)
	}
	if res.StatusCode != 403 {
		t.Errorf("StatusCode = %d; want 403", res.StatusCode)
	}
	if !res.TransportEvidenceKnown {
		t.Errorf("TransportEvidenceKnown = false; want true")
	}
	if res.TransportOK {
		t.Errorf("TransportOK = true; want false")
	}

	st := store.NewWithConfig("", store.DefaultScoringConfig())
	st.PutWithTransition(res)

	if len(st.Passing()) != 0 {
		t.Errorf("Store.Passing() = %v; want empty", st.Passing())
	}
	if len(st.NetworkPassing()) != 0 {
		t.Errorf("Store.NetworkPassing() = %v; want empty", st.NetworkPassing())
	}
}

// TestBoundary_Stage2_403_ProducesTargetDenied_NeverProxyError verifies that:
// An HTTP 403 returned by Stage 2 (Gemini) is classified as ErrTargetDenied
// (never ErrProxyError) and TransportOK=true is preserved.
func TestBoundary_Stage2_403_ProducesTargetDenied_NeverProxyError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthSrv.Close()

	var geminiCalls int64
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&geminiCalls, 1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer geminiSrv.Close()

	cand := newDirectCandidate("stage2-403")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     geminiSrv.URL,
		Timeout:       2 * time.Second,
		DialTimeout:   1 * time.Second,
		MaxRetries:    0,
	}

	res := tester.Probe(ctx, cand, cfg, nil)

	if calls := atomic.LoadInt64(&geminiCalls); calls != 1 {
		t.Fatalf("Gemini server called %d times; want 1", calls)
	}
	if res.Status != store.StatusFailed {
		t.Errorf("Status = %s; want %s", res.Status, store.StatusFailed)
	}
	if res.Category != store.ErrTargetDenied {
		t.Errorf("Category = %s; want %s (must NOT be ErrProxyError)", res.Category, store.ErrTargetDenied)
	}
	if res.StatusCode != 403 {
		t.Errorf("StatusCode = %d; want 403", res.StatusCode)
	}
	if !res.TransportEvidenceKnown {
		t.Errorf("TransportEvidenceKnown = false; want true")
	}
	if !res.TransportOK {
		t.Errorf("TransportOK = false; want true")
	}

	st := store.NewWithConfig("", store.DefaultScoringConfig())
	st.PutWithTransition(res)

	if len(st.Passing()) != 0 {
		t.Errorf("Store.Passing() = %v; want empty", st.Passing())
	}
	netPassing := st.NetworkPassing()
	if len(netPassing) != 1 || netPassing[0] != cand.Link {
		t.Errorf("Store.NetworkPassing() = %v; want [%s]", netPassing, cand.Link)
	}
}

// TestBoundary_Stage1_RetryAfter_PropagatedToCandidateRetryScheduler verifies that:
// When Stage 1 returns HTTP 429 with Retry-After: 1, Retry-After is propagated to the
// candidate-level retry scheduler, triggering a second attempt that succeeds.
func TestBoundary_Stage1_RetryAfter_PropagatedToCandidateRetryScheduler(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var healthCalls int64
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt64(&healthCalls, 1)
		if call == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
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

	cand := newDirectCandidate("stage1-429-retry")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     geminiSrv.URL,
		Timeout:       2 * time.Second,
		DialTimeout:   1 * time.Second,
		MaxRetries:    1,
		RetryBackoff:  10 * time.Millisecond,
	}

	res := tester.Probe(ctx, cand, cfg, nil)

	if calls := atomic.LoadInt64(&healthCalls); calls != 2 {
		t.Fatalf("Health server called %d times; want 2 (retry on 429)", calls)
	}
	if calls := atomic.LoadInt64(&geminiCalls); calls != 1 {
		t.Fatalf("Gemini server called %d times; want 1 (after retry)", calls)
	}
	if res.Attempts != 2 {
		t.Errorf("Attempts = %d; want 2", res.Attempts)
	}
	if res.Status != store.StatusPassed {
		t.Errorf("Status = %s; want %s", res.Status, store.StatusPassed)
	}
	if !res.TransportEvidenceKnown || !res.TransportOK {
		t.Errorf("expected Known=true, TransportOK=true; got Known=%v, OK=%v", res.TransportEvidenceKnown, res.TransportOK)
	}
}

// TestBoundary_Stage1_503_RetryAfter_PropagatedToCandidateRetryScheduler verifies that:
// When Stage 1 returns HTTP 503 with Retry-After: 1, Retry-After is propagated to the
// candidate-level retry scheduler, triggering a second attempt that succeeds.
func TestBoundary_Stage1_503_RetryAfter_PropagatedToCandidateRetryScheduler(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var healthCalls int64
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt64(&healthCalls, 1)
		if call == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
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

	cand := newDirectCandidate("stage1-503-retry")
	cfg := &config.TestConfig{
		HealthURL:     healthSrv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
		TargetURL:     geminiSrv.URL,
		Timeout:       2 * time.Second,
		DialTimeout:   1 * time.Second,
		MaxRetries:    1,
		RetryBackoff:  10 * time.Millisecond,
	}

	res := tester.Probe(ctx, cand, cfg, nil)

	if calls := atomic.LoadInt64(&healthCalls); calls != 2 {
		t.Fatalf("Health server called %d times; want 2 (retry on 503)", calls)
	}
	if calls := atomic.LoadInt64(&geminiCalls); calls != 1 {
		t.Fatalf("Gemini server called %d times; want 1 (after retry)", calls)
	}
	if res.Attempts != 2 {
		t.Errorf("Attempts = %d; want 2", res.Attempts)
	}
	if res.Status != store.StatusPassed {
		t.Errorf("Status = %s; want %s", res.Status, store.StatusPassed)
	}
	if !res.TransportEvidenceKnown || !res.TransportOK {
		t.Errorf("expected Known=true, TransportOK=true; got Known=%v, OK=%v", res.TransportEvidenceKnown, res.TransportOK)
	}
}
