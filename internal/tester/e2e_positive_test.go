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
	time.Sleep(50 * time.Millisecond)

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
