package scheduler

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
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"gemsub/internal/config"
	"gemsub/internal/parser"
	"gemsub/internal/publisher"
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

type mockGitRunner struct {
	calls   [][]string
	runFunc func(ctx context.Context, dir string, args ...string) (string, error)
}

func (m *mockGitRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	m.calls = append(m.calls, args)
	if m.runFunc != nil {
		return m.runFunc(ctx, dir, args...)
	}
	return "", nil
}

// TestScheduler_TwoStageMixedPoolIntegration verifies the complete lifecycle of a mixed candidate pool:
// 1. Fetch from source
// 2. Parse candidates
// 3. Two-stage classification (Pass, RegionBlocked, TargetDenied, ServerError, TransportFailed)
// 4. Store state transitions and tier-based ranking
// 5. Publisher output to git repo
// 6. Subserver serving (/sub and /healthz)
// 7. Store snapshot persistence and reload with zero data loss
func TestScheduler_TwoStageMixedPoolIntegration(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.json")
	st := store.New(statePath, 2)

	// 5 distinct candidate share links
	const (
		candPass    = "vless://11111111-1111-1111-1111-111111111111@127.0.0.1:81?type=tcp#state-b-pass"
		candBlocked = "vless://22222222-2222-2222-2222-222222222222@127.0.0.1:82?type=tcp#state-c-blocked"
		candDenied  = "vless://33333333-3333-3333-3333-333333333333@127.0.0.1:83?type=tcp#state-d-denied"
		candError   = "vless://44444444-4444-4444-4444-444444444444@127.0.0.1:84?type=tcp#state-e-error"
		candFailed  = "vless://55555555-5555-5555-5555-555555555555@127.0.0.1:85?type=tcp#state-a-failed"
	)

	// Source server delivering the 5 candidate links
	sourcePayload := strings.Join([]string{candPass, candBlocked, candDenied, candError, candFailed}, "\n") + "\n"
	sourceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sourcePayload))
	}))
	defer sourceSrv.Close()

	pubRepoDir := filepath.Join(tmpDir, "repo")
	if err := os.MkdirAll(pubRepoDir, 0755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	cfg := &config.Config{
		Sources:          []string{sourceSrv.URL},
		FetchIntervalRaw: "1h",
		Test: config.TestConfig{
			TargetURL:    "https://gemini.google.com/",
			BlockPhrases: []string{"blocked"},
			TimeoutRaw:   "5s",
			Concurrency:  5,
		},
		Publishing: config.PublishingConfig{
			Enabled:    true,
			Repository: pubRepoDir,
			Branch:     "main",
			RemoteURL:  "git@github.com:example/gemsub-subscriptions.git",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("cfg.Validate: %v", err)
	}

	var publishCount int32
	mockGit := &mockGitRunner{
		runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 0 {
				switch args[0] {
				case "rev-parse":
					return "true\n", nil
				case "config":
					return "git@github.com:example/gemsub-subscriptions.git\n", nil
				case "add":
					atomic.AddInt32(&publishCount, 1)
					return "", nil
				case "diff":
					return "all.txt\n", nil
				case "commit", "push":
					return "", nil
				}
			}
			return "", nil
		},
	}

	// Mock probe runner simulating the two-stage orchestrator classification outcomes
	mockRunner := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter *rate.Limiter) store.Result {
		now := time.Now()
		switch {
		case strings.Contains(cand.Link, "state-b-pass"):
			return store.Result{
				Link:                   cand.Link,
				Status:                 store.StatusPassed,
				Category:               store.ErrNone,
				StatusCode:             200,
				Reason:                 "ok",
				TestedAt:               now,
				Latency:                50 * time.Millisecond,
				TransportEvidenceKnown: true,
				TransportOK:            true,
				TransportLatency:       20 * time.Millisecond,
			}
		case strings.Contains(cand.Link, "state-c-blocked"):
			return store.Result{
				Link:                   cand.Link,
				Status:                 store.StatusFailed,
				Category:               store.ErrRegionBlocked,
				Reason:                 "Gemini isn't supported in your country",
				TestedAt:               now,
				Latency:                60 * time.Millisecond,
				TransportEvidenceKnown: true,
				TransportOK:            true,
				TransportLatency:       25 * time.Millisecond,
			}
		case strings.Contains(cand.Link, "state-d-denied"):
			return store.Result{
				Link:                   cand.Link,
				Status:                 store.StatusFailed,
				Category:               store.ErrTargetDenied,
				StatusCode:             403,
				Reason:                 "target HTTP 403 Forbidden",
				TestedAt:               now,
				Latency:                70 * time.Millisecond,
				TransportEvidenceKnown: true,
				TransportOK:            true,
				TransportLatency:       30 * time.Millisecond,
			}
		case strings.Contains(cand.Link, "state-e-error"):
			return store.Result{
				Link:                   cand.Link,
				Status:                 store.StatusInconclusive,
				Category:               store.ErrTargetError,
				StatusCode:             503,
				Reason:                 "target HTTP 503 Service Unavailable",
				TestedAt:               now,
				Latency:                80 * time.Millisecond,
				TransportEvidenceKnown: true,
				TransportOK:            true,
				TransportLatency:       35 * time.Millisecond,
			}
		case strings.Contains(cand.Link, "state-a-failed"):
			return store.Result{
				Link:                   cand.Link,
				Status:                 store.StatusFailed,
				Category:               store.ErrConnRefused,
				Reason:                 "connection refused",
				TestedAt:               now,
				Latency:                10 * time.Millisecond,
				TransportEvidenceKnown: true,
				TransportOK:            false,
				TransportLatency:       10 * time.Millisecond,
			}
		default:
			return store.Result{
				Link:     cand.Link,
				Status:   store.StatusFailed,
				Category: store.ErrProxyError,
				TestedAt: now,
			}
		}
	}

	pub := publisher.NewWithGit(&cfg.Publishing, st, mockGit)
	sched := New(cfg, st)
	sched.SetPublisher(pub)

	ctx := context.Background()
	// Directly execute unexported runCycleWithRunner within package scheduler
	sched.runCycleWithRunner(ctx, tester.ProbeRunner(mockRunner))

	// 1. Verify Store.Passing() contains ONLY candPass
	passing := st.Passing()
	if len(passing) != 1 || passing[0] != candPass {
		t.Fatalf("Store.Passing() = %v; want exclusively [%s]", passing, candPass)
	}

	// 2. Verify Store.PassingRanked() contains ONLY candPass
	passingRanked := st.PassingRanked()
	if len(passingRanked) != 1 || passingRanked[0] != candPass {
		t.Fatalf("Store.PassingRanked() = %v; want exclusively [%s]", passingRanked, candPass)
	}

	// 3. Verify Store.NetworkPassing() contains all 4 transport-healthy candidates and excludes candFailed
	netPassing := st.NetworkPassing()
	if len(netPassing) != 4 {
		t.Fatalf("Store.NetworkPassing() count = %d, want 4. Got: %v", len(netPassing), netPassing)
	}
	netPassingMap := make(map[string]bool)
	for _, link := range netPassing {
		netPassingMap[link] = true
	}
	if !netPassingMap[candPass] || !netPassingMap[candBlocked] || !netPassingMap[candDenied] || !netPassingMap[candError] {
		t.Errorf("Store.NetworkPassing() missing expected candidates: %v", netPassing)
	}
	if netPassingMap[candFailed] {
		t.Errorf("Store.NetworkPassing() must NOT contain transport-failed candidate %s", candFailed)
	}

	// 4. Verify Store.NetworkPassingRanked() ranks Gemini-passing candPass first (Tier 1 > Tier 2)
	netRanked := st.NetworkPassingRanked()
	if len(netRanked) != 4 {
		t.Fatalf("Store.NetworkPassingRanked() count = %d, want 4", len(netRanked))
	}
	if netRanked[0] != candPass {
		t.Errorf("Store.NetworkPassingRanked()[0] = %s; want Tier 1 Gemini-servable candPass=%s", netRanked[0], candPass)
	}

	// 5. Verify Publisher output in git repository
	if atomic.LoadInt32(&publishCount) == 0 {
		t.Fatalf("expected publisher git add to be called")
	}
	pubFile := filepath.Join(pubRepoDir, "all.txt")
	pubContentBytes, err := os.ReadFile(pubFile)
	if err != nil {
		t.Fatalf("read published all.txt: %v", err)
	}
	pubContent := string(pubContentBytes)
	if !strings.Contains(pubContent, candPass) {
		t.Errorf("expected published all.txt to contain %s, got:\n%s", candPass, pubContent)
	}
	if strings.Contains(pubContent, candBlocked) || strings.Contains(pubContent, candDenied) ||
		strings.Contains(pubContent, candError) || strings.Contains(pubContent, candFailed) {
		t.Errorf("published all.txt must NOT contain non-servable candidates. Got:\n%s", pubContent)
	}

	// 6. Verify Subserver (/sub, /healthz) with deterministic polling
	subPort := getFreePort(t)
	subAddr := fmt.Sprintf("127.0.0.1:%d", subPort)
	srv := subserver.New(&config.ServeConfig{
		Listen: subAddr,
		Path:   "/sub",
		Format: "base64",
	}, st)

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
	decodedStr := strings.TrimSpace(string(decoded))
	if decodedStr != candPass {
		t.Errorf("/sub decoded = %q, want exclusively %q", decodedStr, candPass)
	}

	// 7. Verify Persistence Snapshot & Reload (Zero Data Loss)
	reloadedStore := store.New(statePath, 2)
	if err := reloadedStore.Load(); err != nil {
		t.Fatalf("reload store from %s: %v", statePath, err)
	}

	if len(reloadedStore.Passing()) != 1 || reloadedStore.Passing()[0] != candPass {
		t.Errorf("reloadedStore.Passing() = %v, want [%s]", reloadedStore.Passing(), candPass)
	}
	reloadedNetPassing := reloadedStore.NetworkPassing()
	if len(reloadedNetPassing) != 4 {
		t.Fatalf("reloadedStore.NetworkPassing() count = %d, want 4", len(reloadedNetPassing))
	}

	// Check explicit transport evidence on individual reloaded records
	recB, ok := reloadedStore.GetRecord(candPass)
	if !ok || !recB.Latest.TransportEvidenceKnown || !recB.Latest.TransportOK || recB.Latest.TransportLatency != 20*time.Millisecond {
		t.Errorf("reloaded candPass transport evidence mismatch: %+v", recB)
	}
	recC, ok := reloadedStore.GetRecord(candBlocked)
	if !ok || !recC.Latest.TransportEvidenceKnown || !recC.Latest.TransportOK || recC.Latest.TransportLatency != 25*time.Millisecond {
		t.Errorf("reloaded candBlocked transport evidence mismatch: %+v", recC)
	}
	recD, ok := reloadedStore.GetRecord(candDenied)
	if !ok || !recD.Latest.TransportEvidenceKnown || !recD.Latest.TransportOK || recD.Latest.TransportLatency != 30*time.Millisecond {
		t.Errorf("reloaded candDenied transport evidence mismatch: %+v", recD)
	}
	recE, ok := reloadedStore.GetRecord(candError)
	if !ok || !recE.Latest.TransportEvidenceKnown || !recE.Latest.TransportOK || recE.Latest.TransportLatency != 35*time.Millisecond {
		t.Errorf("reloaded candError transport evidence mismatch: %+v", recE)
	}
	recA, ok := reloadedStore.GetRecord(candFailed)
	if !ok || !recA.Latest.TransportEvidenceKnown || recA.Latest.TransportOK || recA.Latest.TransportLatency != 10*time.Millisecond {
		t.Errorf("reloaded candFailed transport evidence mismatch: %+v", recA)
	}
}
