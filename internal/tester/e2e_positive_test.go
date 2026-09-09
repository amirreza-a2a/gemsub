package tester_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"

	"gemsub/internal/config"
	"gemsub/internal/parser"
	"gemsub/internal/store"
	"gemsub/internal/subserver"
	"gemsub/internal/tester"
)

func getFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitForServerReady(t *testing.T, targetURL string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(targetURL)
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for server at %s to be ready", targetURL)
}

// TestEndToEnd_PositivePath verifies the complete positive-path pipeline:
// Candidate Outbound -> sing-box Box in-process -> Dial -> HTTP -> Gemini Classifier -> StatusPassed -> Store -> /sub & /healthz
func TestEndToEnd_PositivePath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Start target HTTP server that simulates a genuine Gemini response
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "ESF")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<!doctype html><html><head><title>Google Gemini</title></head><body><div id=\"bardchatui\"><p>Welcome to Gemini</p></div></body></html>"))
	}))
	defer targetServer.Close()

	// 2. Build candidate using sing-box direct outbound to test in-process Box lifecycle
	candidateLink := "vmess://eyJhZGQiOiIxMjcuMC4wLjEiLCJwb3J0Ijo4MCwiaWQiOiJhYWFhYWFhYS1iYmJiLWNjY2MtZGRkZC1lZWVlZWVlZWVlZWUiLCJ2IjoiMiIsInBzIjoidGVzdC1jYW5kaWRhdGUifQ=="
	cand := parser.Candidate{
		Link: candidateLink,
		Outbound: option.Outbound{
			Type: "direct",
			Tag:  "probe",
		},
	}

	testCfg := &config.TestConfig{
		TargetURL:    targetServer.URL,
		BlockPhrases: []string{"isn't currently supported in your country", "not available in your region"},
		Timeout:      5 * time.Second,
		DialTimeout:  2 * time.Second,
		MaxRetries:   1,
	}

	// 3. PROBE: Run candidate through real in-process sing-box Box -> DialContext -> HTTP -> Gemini classifier
	probeResult := tester.Probe(ctx, cand, testCfg, nil)

	if probeResult.Status != store.StatusPassed {
		t.Fatalf("expected StatusPassed, got status=%s category=%s reason=%q",
			probeResult.Status, probeResult.Category, probeResult.Reason)
	}
	if probeResult.Category != store.ErrNone {
		t.Fatalf("expected ErrNone, got category=%s", probeResult.Category)
	}
	if probeResult.Attempts != 1 {
		t.Fatalf("expected 1 attempt on first-try success, got %d", probeResult.Attempts)
	}

	// 4. STORE: Put the result into Store, verify state transitions, servability, and persistence
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	st := store.New(stateFile, 2)

	st.PutWithTransition(probeResult)
	st.FinishCycle()
	if err := st.Save(); err != nil {
		t.Fatalf("save store: %v", err)
	}

	passing := st.Passing()
	if len(passing) != 1 || passing[0] != candidateLink {
		t.Fatalf("expected store to contain passing link %q, got: %v", candidateLink, passing)
	}

	stats := st.Stats()
	if stats.Passed != 1 || stats.Servable != 1 || stats.CycleCount != 1 {
		t.Fatalf("expected stats passed=1 servable=1 cycle_count=1, got %+v", stats)
	}

	// 5. SUBSCRIPTION SERVER: Verify /sub and /healthz output
	subListenPort := getFreePort(t)
	subListenAddr := fmt.Sprintf("127.0.0.1:%d", subListenPort)
	srv := subserver.New(&config.ServeConfig{
		Listen: subListenAddr,
		Path:   "/sub",
		Format: "base64",
	}, st)

	srvCtx, srvCancel := context.WithCancel(context.Background())
	defer srvCancel()

	go func() {
		_ = srv.Run(srvCtx)
	}()
	waitForServerReady(t, fmt.Sprintf("http://%s/healthz", subListenAddr))

	// Verify /healthz endpoint
	respHealth, err := http.Get(fmt.Sprintf("http://%s/healthz", subListenAddr))
	if err != nil {
		t.Fatalf("get /healthz: %v", err)
	}
	bodyHealth, _ := io.ReadAll(respHealth.Body)
	respHealth.Body.Close()

	if !strings.Contains(string(bodyHealth), "passed: 1") || !strings.Contains(string(bodyHealth), "servable: 1") {
		t.Fatalf("expected /healthz to report passed: 1 and servable: 1, got:\n%s", string(bodyHealth))
	}

	// Verify /sub base64 subscription output
	respSub, err := http.Get(fmt.Sprintf("http://%s/sub", subListenAddr))
	if err != nil {
		t.Fatalf("get /sub: %v", err)
	}
	bodySub, _ := io.ReadAll(respSub.Body)
	respSub.Body.Close()

	decoded, err := base64.StdEncoding.DecodeString(string(bodySub))
	if err != nil {
		t.Fatalf("decode /sub base64 body: %v", err)
	}
	if strings.TrimSpace(string(decoded)) != candidateLink {
		t.Fatalf("expected /sub to serve %q, got %q", candidateLink, string(decoded))
	}

	// 6. PERSISTENCE: Verify persistent reload retains the passing candidate
	reloadedStore := store.New(stateFile, 2)
	if err := reloadedStore.Load(); err != nil {
		t.Fatalf("reload store: %v", err)
	}
	if len(reloadedStore.Passing()) != 1 || reloadedStore.Passing()[0] != candidateLink {
		t.Fatalf("expected reloaded store to preserve passing candidate")
	}

	_ = os.Remove(stateFile)
}

// TestEndToEnd_LegacyConfigCompatibility demonstrates the complete compatibility chain:
//
//	legacy config normalization -> equivalent effective modern TestConfig -> same two-stage classification behavior
//
// It verifies:
//  1. A pure Phase 0 legacy configuration (using ONLY target_url and block_phrases, with NO
//     gemini block, health_url, or health_timeout) loads and normalizes correctly to canonical defaults.
//  2. Normalization yields an effective TestConfig identical to an explicit modern configuration.
//  3. Hermetic two-stage probe execution produces identical classification and transport evidence
//     for both legacy and modern configurations across both pass and rejection paths.
//  4. End-to-end Store servability, Subserver publication (/sub, /healthz), and persistence snapshot
//     reload function without behavioral regression.
func TestEndToEnd_LegacyConfigCompatibility(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Setup local mock servers
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthSrv.Close()

	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "ESF")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Google Gemini</title></head><body><div id="bardchatui"><p>Legacy config test</p></div></body></html>`))
	}))
	defer geminiSrv.Close()

	blockedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><html><body>Gemini isn't currently supported in your country</body></html>`))
	}))
	defer blockedSrv.Close()

	tmpDir := t.TempDir()
	legacyStateFile := filepath.Join(tmpDir, "legacy_state.json")
	modernStateFile := filepath.Join(tmpDir, "modern_state.json")
	subPort := getFreePort(t)
	subAddr := fmt.Sprintf("127.0.0.1:%d", subPort)

	// 2. Write pure legacy JSON configuration: ONLY target_url and block_phrases
	legacyJSON := fmt.Sprintf(`{
		"sources": ["http://127.0.0.1:1/dummy"],
		"fetch_interval": "1h",
		"state_file": %q,
		"test": {
			"target_url": %q,
			"block_phrases": ["isn't currently supported in your country"],
			"timeout": "5s",
			"concurrency": 2
		},
		"serve": {
			"listen": %q,
			"path": "/sub",
			"format": "base64"
		}
	}`, legacyStateFile, geminiSrv.URL, subAddr)

	legacyConfigPath := filepath.Join(tmpDir, "legacy_config.json")
	if err := os.WriteFile(legacyConfigPath, []byte(legacyJSON), 0o644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	// 3. Write explicit equivalent modern JSON configuration
	modernJSON := fmt.Sprintf(`{
		"sources": ["http://127.0.0.1:1/dummy"],
		"fetch_interval": "1h",
		"state_file": %q,
		"test": {
			"health_url": "https://www.gstatic.com/generate_204",
			"gemini": {
				"url": %q,
				"block_phrases": ["isn't currently supported in your country"]
			},
			"timeout": "5s",
			"concurrency": 2
		},
		"serve": {
			"listen": %q,
			"path": "/sub",
			"format": "base64"
		}
	}`, modernStateFile, geminiSrv.URL, subAddr)

	modernConfigPath := filepath.Join(tmpDir, "modern_config.json")
	if err := os.WriteFile(modernConfigPath, []byte(modernJSON), 0o644); err != nil {
		t.Fatalf("write modern config: %v", err)
	}

	// 4. Load both configs and verify normalization
	cfgLegacy, err := config.Load(legacyConfigPath)
	if err != nil {
		t.Fatalf("config.Load failed on legacy config: %v", err)
	}

	cfgModern, err := config.Load(modernConfigPath)
	if err != nil {
		t.Fatalf("config.Load failed on modern config: %v", err)
	}

	// Verify legacy normalization produced canonical transport endpoint and mapped Gemini settings
	if cfgLegacy.Test.HealthURL != "https://www.gstatic.com/generate_204" {
		t.Errorf("expected default HealthURL, got %q", cfgLegacy.Test.HealthURL)
	}
	if cfgLegacy.Test.HealthTimeout != 4*time.Second {
		t.Errorf("expected default HealthTimeout 4s, got %v", cfgLegacy.Test.HealthTimeout)
	}
	if cfgLegacy.Test.Gemini.URL != geminiSrv.URL {
		t.Errorf("expected Gemini.URL normalized from target_url %q, got %q", geminiSrv.URL, cfgLegacy.Test.Gemini.URL)
	}
	if len(cfgLegacy.Test.Gemini.BlockPhrases) != 1 || cfgLegacy.Test.Gemini.BlockPhrases[0] != "isn't currently supported in your country" {
		t.Errorf("expected Gemini.BlockPhrases normalized from legacy block_phrases, got %v", cfgLegacy.Test.Gemini.BlockPhrases)
	}
	if cfgLegacy.Test.TargetURL != cfgLegacy.Test.Gemini.URL {
		t.Errorf("expected TargetURL == Gemini.URL, got %q vs %q", cfgLegacy.Test.TargetURL, cfgLegacy.Test.Gemini.URL)
	}

	// Verify structural equivalence: legacy config normalized == modern config explicit
	if cfgLegacy.Test.HealthURL != cfgModern.Test.HealthURL {
		t.Errorf("HealthURL mismatch: legacy=%q, modern=%q", cfgLegacy.Test.HealthURL, cfgModern.Test.HealthURL)
	}
	if cfgLegacy.Test.HealthTimeout != cfgModern.Test.HealthTimeout {
		t.Errorf("HealthTimeout mismatch: legacy=%v, modern=%v", cfgLegacy.Test.HealthTimeout, cfgModern.Test.HealthTimeout)
	}
	if cfgLegacy.Test.Gemini.URL != cfgModern.Test.Gemini.URL {
		t.Errorf("Gemini.URL mismatch: legacy=%q, modern=%q", cfgLegacy.Test.Gemini.URL, cfgModern.Test.Gemini.URL)
	}
	if !slices.Equal(cfgLegacy.Test.Gemini.BlockPhrases, cfgModern.Test.Gemini.BlockPhrases) {
		t.Errorf("Gemini.BlockPhrases mismatch: legacy=%v, modern=%v", cfgLegacy.Test.Gemini.BlockPhrases, cfgModern.Test.Gemini.BlockPhrases)
	}
	if cfgLegacy.Test.Timeout != cfgModern.Test.Timeout {
		t.Errorf("Timeout mismatch: legacy=%v, modern=%v", cfgLegacy.Test.Timeout, cfgModern.Test.Timeout)
	}
	if cfgLegacy.Test.Concurrency != cfgModern.Test.Concurrency {
		t.Errorf("Concurrency mismatch: legacy=%d, modern=%d", cfgLegacy.Test.Concurrency, cfgModern.Test.Concurrency)
	}

	// 5. Hermetic Two-Stage Behavioral Equivalence
	// For hermetic in-process execution against local mock servers without external network dependencies,
	// configure both test configs to use the local mock health endpoint.
	hermeticHealthURL := healthSrv.URL + "/generate_204"

	testCfgLegacyHermetic := cfgLegacy.Test
	testCfgLegacyHermetic.HealthURL = hermeticHealthURL

	testCfgModernHermetic := cfgModern.Test
	testCfgModernHermetic.HealthURL = hermeticHealthURL

	candidateLink := "vmess://eyJhZGQiOiIxMjcuMC4wLjEiLCJwb3J0Ijo4MCwiaWQiOiJiYmJiYmJiYi1jY2NjLWRkZGQtZWVlZS1mZmZmZmZmZmZmZmYiLCJ2IjoiMiIsInBzIjoibGVnYWN5LWNhbmRpZGF0ZSJ9"
	cand := parser.Candidate{
		Link: candidateLink,
		Outbound: option.Outbound{
			Type: "direct",
			Tag:  "probe",
		},
	}

	// Probe under both configurations
	resLegacy := tester.Probe(ctx, cand, &testCfgLegacyHermetic, nil)
	resModern := tester.Probe(ctx, cand, &testCfgModernHermetic, nil)

	// Assert 100% behavioral equivalence on positive path
	if resLegacy.Status != store.StatusPassed || resModern.Status != store.StatusPassed {
		t.Fatalf("expected StatusPassed on both, got legacy=%s modern=%s", resLegacy.Status, resModern.Status)
	}
	if resLegacy.Category != store.ErrNone || resModern.Category != store.ErrNone {
		t.Fatalf("expected ErrNone on both, got legacy=%s modern=%s", resLegacy.Category, resModern.Category)
	}
	if !resLegacy.TransportEvidenceKnown || !resModern.TransportEvidenceKnown {
		t.Fatalf("expected TransportEvidenceKnown=true on both")
	}
	if !resLegacy.TransportOK || !resModern.TransportOK {
		t.Fatalf("expected TransportOK=true on both")
	}
	if resLegacy.Attempts != 1 || resModern.Attempts != 1 {
		t.Fatalf("expected 1 attempt on both, got legacy=%d modern=%d", resLegacy.Attempts, resModern.Attempts)
	}

	// Negative path behavioral equivalence: verify legacy block_phrases trigger ErrRegionBlocked
	blockedCand := parser.Candidate{
		Link: "vmess://blocked-node",
		Outbound: option.Outbound{
			Type: "direct",
			Tag:  "probe",
		},
	}
	testCfgLegacyBlocked := testCfgLegacyHermetic
	testCfgLegacyBlocked.TargetURL = blockedSrv.URL
	testCfgLegacyBlocked.Gemini.URL = blockedSrv.URL

	testCfgModernBlocked := testCfgModernHermetic
	testCfgModernBlocked.TargetURL = blockedSrv.URL
	testCfgModernBlocked.Gemini.URL = blockedSrv.URL

	resLegacyBlocked := tester.Probe(ctx, blockedCand, &testCfgLegacyBlocked, nil)
	resModernBlocked := tester.Probe(ctx, blockedCand, &testCfgModernBlocked, nil)

	if resLegacyBlocked.Status != store.StatusFailed || resModernBlocked.Status != store.StatusFailed {
		t.Errorf("expected StatusFailed for blocked, got legacy=%s modern=%s", resLegacyBlocked.Status, resModernBlocked.Status)
	}
	if resLegacyBlocked.Category != store.ErrRegionBlocked || resModernBlocked.Category != store.ErrRegionBlocked {
		t.Errorf("expected ErrRegionBlocked, got legacy=%s modern=%s", resLegacyBlocked.Category, resModernBlocked.Category)
	}
	if !resLegacyBlocked.TransportOK || !resModernBlocked.TransportOK {
		t.Errorf("expected TransportOK=true on blocked responses for both")
	}

	// 6. STORE: Record result, finish cycle, and save
	st := store.New(legacyStateFile, 2)
	st.PutWithTransition(resLegacy)
	st.FinishCycle()
	if err := st.Save(); err != nil {
		t.Fatalf("save store: %v", err)
	}

	passing := st.Passing()
	if len(passing) != 1 || passing[0] != candidateLink {
		t.Fatalf("expected store to contain passing link %q, got %v", candidateLink, passing)
	}
	netPassing := st.NetworkPassing()
	if len(netPassing) != 1 || netPassing[0] != candidateLink {
		t.Fatalf("expected store to contain network-passing link %q, got %v", candidateLink, netPassing)
	}

	// 7. SUBSERVER: Verify /sub and /healthz with deterministic polling
	srv := subserver.New(&cfgLegacy.Serve, st)
	srvCtx, srvCancel := context.WithCancel(context.Background())
	defer srvCancel()
	go func() { _ = srv.Run(srvCtx) }()
	waitForServerReady(t, fmt.Sprintf("http://%s/healthz", subAddr))

	respHealth, err := http.Get(fmt.Sprintf("http://%s/healthz", subAddr))
	if err != nil {
		t.Fatalf("get /healthz: %v", err)
	}
	bodyHealth, _ := io.ReadAll(respHealth.Body)
	respHealth.Body.Close()
	if !strings.Contains(string(bodyHealth), "passed: 1") || !strings.Contains(string(bodyHealth), "servable: 1") {
		t.Errorf("/healthz body expected passed: 1, servable: 1; got:\n%s", string(bodyHealth))
	}

	respSub, err := http.Get(fmt.Sprintf("http://%s/sub", subAddr))
	if err != nil {
		t.Fatalf("get /sub: %v", err)
	}
	bodySub, _ := io.ReadAll(respSub.Body)
	respSub.Body.Close()
	decoded, err := base64.StdEncoding.DecodeString(string(bodySub))
	if err != nil {
		t.Fatalf("decode /sub body: %v", err)
	}
	if strings.TrimSpace(string(decoded)) != candidateLink {
		t.Errorf("/sub served %q; want %q", string(decoded), candidateLink)
	}

	// 8. PERSISTENCE: Verify reload retains candidate and transport evidence
	reloadedStore := store.New(legacyStateFile, 2)
	if err := reloadedStore.Load(); err != nil {
		t.Fatalf("reload store: %v", err)
	}
	if len(reloadedStore.Passing()) != 1 || reloadedStore.Passing()[0] != candidateLink {
		t.Errorf("reloaded store passing mismatch: %v", reloadedStore.Passing())
	}
	if len(reloadedStore.NetworkPassing()) != 1 || reloadedStore.NetworkPassing()[0] != candidateLink {
		t.Errorf("reloaded store network passing mismatch: %v", reloadedStore.NetworkPassing())
	}
	rec, ok := reloadedStore.GetRecord(candidateLink)
	if !ok || !rec.Latest.TransportEvidenceKnown || !rec.Latest.TransportOK {
		t.Errorf("reloaded record transport evidence mismatch: %+v", rec)
	}
}
