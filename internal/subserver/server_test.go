package subserver_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gemsub/internal/config"
	"gemsub/internal/store"
	"gemsub/internal/subserver"
)

func getFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestServer_NormalStartupAndServingAndGracefulShutdown(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)
	st.PutWithTransition(store.Result{
		Link:   "vless://node1@example.com:443",
		Status: store.StatusPassed,
		Reason: "ok",
	})
	st.FinishCycle()

	port := getFreePort(t)
	listenAddr := fmt.Sprintf("127.0.0.1:%d", port)

	cfg := &config.ServeConfig{
		Listen: listenAddr,
		Path:   "/sub",
		Format: "base64",
	}

	srv := subserver.New(cfg, st)

	ctx, cancel := context.WithCancel(context.Background())
	serverErrCh := make(chan error, 1)

	go func() {
		serverErrCh <- srv.Run(ctx)
	}()

	// Wait briefly for server to bind and start listening
	var respHealth *http.Response
	var err error
	for i := 0; i < 50; i++ {
		respHealth, err = http.Get(fmt.Sprintf("http://%s/healthz", listenAddr))
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("failed to connect to /healthz: %v", err)
	}
	defer respHealth.Body.Close()

	if respHealth.StatusCode != http.StatusOK {
		t.Errorf("expected /healthz 200 OK, got %d", respHealth.StatusCode)
	}
	bodyHealth, _ := io.ReadAll(respHealth.Body)
	if !strings.Contains(string(bodyHealth), "passed: 1") {
		t.Errorf("expected health body to contain 'passed: 1', got: %s", string(bodyHealth))
	}

	// Test /sub endpoint
	respSub, err := http.Get(fmt.Sprintf("http://%s/sub", listenAddr))
	if err != nil {
		t.Fatalf("failed to connect to /sub: %v", err)
	}
	defer respSub.Body.Close()

	if respSub.StatusCode != http.StatusOK {
		t.Errorf("expected /sub 200 OK, got %d", respSub.StatusCode)
	}
	bodySub, _ := io.ReadAll(respSub.Body)
	decoded, err := base64.StdEncoding.DecodeString(string(bodySub))
	if err != nil {
		t.Fatalf("failed to decode base64 body: %v", err)
	}
	if strings.TrimSpace(string(decoded)) != "vless://node1@example.com:443" {
		t.Errorf("expected link 'vless://node1@example.com:443', got %q", string(decoded))
	}

	// Trigger cancellation
	cancel()

	select {
	case runErr := <-serverErrCh:
		if runErr != nil {
			t.Errorf("expected Run(ctx) to return nil on graceful shutdown, got %v", runErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run(ctx) did not exit within timeout after context cancellation")
	}

	// Verify listener is closed
	conn, dialErr := net.DialTimeout("tcp", listenAddr, 100*time.Millisecond)
	if dialErr == nil {
		conn.Close()
		t.Errorf("expected port %s to be closed after shutdown, but connection succeeded", listenAddr)
	}
}

func TestServer_ContextAlreadyCancelled(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	port := getFreePort(t)
	listenAddr := fmt.Sprintf("127.0.0.1:%d", port)

	cfg := &config.ServeConfig{
		Listen: listenAddr,
		Path:   "/sub",
		Format: "plain",
	}

	srv := subserver.New(cfg, st)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel before Run

	done := make(chan error, 1)
	go func() {
		done <- srv.Run(ctx)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("expected nil on pre-cancelled context shutdown, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run(ctx) hung on pre-cancelled context")
	}
}

func TestServer_BindFailureReturnsPromptlyWithoutLeaking(t *testing.T) {
	// Occupy a port with an existing listener
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on port: %v", err)
	}
	defer l.Close()

	port := l.Addr().(*net.TCPAddr).Port
	listenAddr := fmt.Sprintf("127.0.0.1:%d", port)

	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	cfg := &config.ServeConfig{
		Listen: listenAddr,
		Path:   "/sub",
		Format: "plain",
	}

	srv := subserver.New(cfg, st)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- srv.Run(ctx)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected bind error when port is in use, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run(ctx) hung when port was already occupied")
	}
}

func startTestServer(t *testing.T, cfg *config.ServeConfig, st *store.Store) (string, func()) {
	t.Helper()
	port := getFreePort(t)
	cfg.Listen = fmt.Sprintf("127.0.0.1:%d", port)
	srv := subserver.New(cfg, st)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Run(ctx)
	}()

	// Wait for server to bind
	var connected bool
	for i := 0; i < 50; i++ {
		resp, err := http.Get(fmt.Sprintf("http://%s/healthz", cfg.Listen))
		if err == nil {
			resp.Body.Close()
			connected = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !connected {
		cancel()
		t.Fatalf("test server failed to bind on %s within timeout", cfg.Listen)
	}

	cleanup := func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
			t.Fatal("server did not shutdown within 2s")
		}
		// Confirm port released
		conn, dialErr := net.DialTimeout("tcp", cfg.Listen, 50*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			t.Fatalf("port %s was not released after server shutdown", cfg.Listen)
		}
	}
	return cfg.Listen, cleanup
}

func populateMixedStore(t *testing.T) (*store.Store, map[string]string) {
	t.Helper()
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)

	nodes := map[string]string{
		"pass":        "vless://pass@1.1.1.1:443",
		"blocked":     "vmess://blocked@2.2.2.2:443",
		"denied":      "trojan://denied@3.3.3.3:443",
		"fail":        "vless://fail@4.4.4.4:443",
		"unsupported": "ss://unsupported@5.5.5.5:8388",
	}

	// 1. Stage 1 Transport OK + Stage 2 Gemini PASS -> both generic and gemini
	st.PutWithTransition(store.Result{
		Link:                   nodes["pass"],
		Status:                 store.StatusPassed,
		TransportEvidenceKnown: true,
		TransportOK:            true,
	})

	// 2. Stage 1 Transport OK + Stage 2 Gemini RegionBlocked -> generic only
	st.PutWithTransition(store.Result{
		Link:                   nodes["blocked"],
		Status:                 store.StatusFailed,
		Category:               store.ErrRegionBlocked,
		TransportEvidenceKnown: true,
		TransportOK:            true,
	})

	// 3. Stage 1 Transport OK + Stage 2 Gemini TargetDenied -> generic only
	st.PutWithTransition(store.Result{
		Link:                   nodes["denied"],
		Status:                 store.StatusFailed,
		Category:               store.ErrTargetDenied,
		TransportEvidenceKnown: true,
		TransportOK:            true,
	})

	// 4. Stage 1 Transport Failure -> NEITHER generic nor gemini
	st.PutWithTransition(store.Result{
		Link:                   nodes["fail"],
		Status:                 store.StatusFailed,
		Category:               store.ErrTimeout,
		TransportEvidenceKnown: true,
		TransportOK:            false,
	})

	// 5. Stage 1 Transport OK + Gemini PASS with unsupported scheme ss:// -> both generic and gemini
	st.PutWithTransition(store.Result{
		Link:                   nodes["unsupported"],
		Status:                 store.StatusPassed,
		TransportEvidenceKnown: true,
		TransportOK:            true,
	})

	st.FinishCycle()
	return st, nodes
}

func fetchBody(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s failed: %v", url, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func decodeBody(t *testing.T, rawBody string, isBase64 bool) string {
	t.Helper()
	if !isBase64 {
		return rawBody
	}
	if strings.TrimSpace(rawBody) == "" {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rawBody))
	if err != nil {
		t.Fatalf("base64 decode failed for %q: %v", rawBody, err)
	}
	return string(decoded)
}

func TestServer_DualProjections_RoutingAndPrecedence(t *testing.T) {
	st, nodes := populateMixedStore(t)
	cfg := &config.ServeConfig{
		Path:   "/sub",
		Format: "raw",
	}
	addr, cleanup := startTestServer(t, cfg, st)
	defer cleanup()

	tests := []struct {
		name           string
		pathAndQuery   string
		expectedStatus int
		wantNodes      []string
		doNotWantNodes []string
	}{
		{
			name:           "Default /sub serves Gemini projection (backward compat)",
			pathAndQuery:   "/sub",
			expectedStatus: http.StatusOK,
			wantNodes:      []string{nodes["pass"], nodes["unsupported"]},
			doNotWantNodes: []string{nodes["blocked"], nodes["denied"], nodes["fail"]},
		},
		{
			name:           "Trailing slash /sub/ serves Gemini projection",
			pathAndQuery:   "/sub/",
			expectedStatus: http.StatusOK,
			wantNodes:      []string{nodes["pass"], nodes["unsupported"]},
			doNotWantNodes: []string{nodes["blocked"], nodes["denied"], nodes["fail"]},
		},
		{
			name:           "/sub?projection=gemini serves Gemini projection",
			pathAndQuery:   "/sub?projection=gemini",
			expectedStatus: http.StatusOK,
			wantNodes:      []string{nodes["pass"], nodes["unsupported"]},
			doNotWantNodes: []string{nodes["blocked"], nodes["denied"], nodes["fail"]},
		},
		{
			name:           "/sub?projection=generic serves Generic projection",
			pathAndQuery:   "/sub?projection=generic",
			expectedStatus: http.StatusOK,
			wantNodes:      []string{nodes["pass"], nodes["blocked"], nodes["denied"], nodes["unsupported"]},
			doNotWantNodes: []string{nodes["fail"]},
		},
		{
			name:           "Dedicated route /sub/gemini serves Gemini projection",
			pathAndQuery:   "/sub/gemini",
			expectedStatus: http.StatusOK,
			wantNodes:      []string{nodes["pass"], nodes["unsupported"]},
			doNotWantNodes: []string{nodes["blocked"], nodes["denied"], nodes["fail"]},
		},
		{
			name:           "Dedicated route /sub/gemini/ serves Gemini projection",
			pathAndQuery:   "/sub/gemini/",
			expectedStatus: http.StatusOK,
			wantNodes:      []string{nodes["pass"], nodes["unsupported"]},
			doNotWantNodes: []string{nodes["blocked"], nodes["denied"], nodes["fail"]},
		},
		{
			name:           "Dedicated route /sub/generic serves Generic projection",
			pathAndQuery:   "/sub/generic",
			expectedStatus: http.StatusOK,
			wantNodes:      []string{nodes["pass"], nodes["blocked"], nodes["denied"], nodes["unsupported"]},
			doNotWantNodes: []string{nodes["fail"]},
		},
		{
			name:           "Dedicated route /sub/generic/ serves Generic projection",
			pathAndQuery:   "/sub/generic/",
			expectedStatus: http.StatusOK,
			wantNodes:      []string{nodes["pass"], nodes["blocked"], nodes["denied"], nodes["unsupported"]},
			doNotWantNodes: []string{nodes["fail"]},
		},
		{
			name:           "Precedence: /sub/generic?projection=gemini -> generic",
			pathAndQuery:   "/sub/generic?projection=gemini",
			expectedStatus: http.StatusOK,
			wantNodes:      []string{nodes["pass"], nodes["blocked"], nodes["denied"], nodes["unsupported"]},
			doNotWantNodes: []string{nodes["fail"]},
		},
		{
			name:           "Precedence: /sub/gemini?projection=generic -> gemini",
			pathAndQuery:   "/sub/gemini?projection=generic",
			expectedStatus: http.StatusOK,
			wantNodes:      []string{nodes["pass"], nodes["unsupported"]},
			doNotWantNodes: []string{nodes["blocked"], nodes["denied"], nodes["fail"]},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, body := fetchBody(t, fmt.Sprintf("http://%s%s", addr, tc.pathAndQuery))
			if status != tc.expectedStatus {
				t.Fatalf("expected status %d, got %d for %s (body: %s)", tc.expectedStatus, status, tc.pathAndQuery, body)
			}
			for _, want := range tc.wantNodes {
				if !strings.Contains(body, want) {
					t.Errorf("expected body to contain %q, got:\n%s", want, body)
				}
			}
			for _, notWant := range tc.doNotWantNodes {
				if strings.Contains(body, notWant) {
					t.Errorf("expected body NOT to contain %q, got:\n%s", notWant, body)
				}
			}
		})
	}
}

func TestServer_ProtocolFiltering(t *testing.T) {
	st, nodes := populateMixedStore(t)
	cfg := &config.ServeConfig{
		Path:   "/sub",
		Format: "raw",
	}
	addr, cleanup := startTestServer(t, cfg, st)
	defer cleanup()

	tests := []struct {
		name           string
		pathAndQuery   string
		expectedStatus int
		wantNodes      []string
		doNotWantNodes []string
	}{
		{
			name:           "Generic protocol=vless",
			pathAndQuery:   "/sub/generic?protocol=vless",
			expectedStatus: http.StatusOK,
			wantNodes:      []string{nodes["pass"]},
			doNotWantNodes: []string{nodes["blocked"], nodes["denied"], nodes["unsupported"], nodes["fail"]},
		},
		{
			name:           "Generic proto=vless alias",
			pathAndQuery:   "/sub/generic?proto=vless",
			expectedStatus: http.StatusOK,
			wantNodes:      []string{nodes["pass"]},
			doNotWantNodes: []string{nodes["blocked"], nodes["denied"], nodes["unsupported"], nodes["fail"]},
		},
		{
			name:           "Generic protocol=vmess",
			pathAndQuery:   "/sub/generic?protocol=vmess",
			expectedStatus: http.StatusOK,
			wantNodes:      []string{nodes["blocked"]},
			doNotWantNodes: []string{nodes["pass"], nodes["denied"], nodes["unsupported"], nodes["fail"]},
		},
		{
			name:           "Generic protocol=trojan",
			pathAndQuery:   "/sub/generic?protocol=trojan",
			expectedStatus: http.StatusOK,
			wantNodes:      []string{nodes["denied"]},
			doNotWantNodes: []string{nodes["pass"], nodes["blocked"], nodes["unsupported"], nodes["fail"]},
		},
		{
			name:           "Gemini protocol=vmess (no match -> 200 empty)",
			pathAndQuery:   "/sub/gemini?protocol=vmess",
			expectedStatus: http.StatusOK,
			wantNodes:      nil,
			doNotWantNodes: []string{nodes["pass"], nodes["blocked"], nodes["denied"], nodes["unsupported"], nodes["fail"]},
		},
		{
			name:           "Query composition: /sub?projection=generic&protocol=vless",
			pathAndQuery:   "/sub?projection=generic&protocol=vless",
			expectedStatus: http.StatusOK,
			wantNodes:      []string{nodes["pass"]},
			doNotWantNodes: []string{nodes["blocked"], nodes["denied"], nodes["unsupported"], nodes["fail"]},
		},
		{
			name:           "Query composition: /sub?projection=gemini&protocol=vless",
			pathAndQuery:   "/sub?projection=gemini&protocol=vless",
			expectedStatus: http.StatusOK,
			wantNodes:      []string{nodes["pass"]},
			doNotWantNodes: []string{nodes["blocked"], nodes["denied"], nodes["unsupported"], nodes["fail"]},
		},
		{
			name:           "Identical protocol and proto aliases",
			pathAndQuery:   "/sub/generic?protocol=vless&proto=vless",
			expectedStatus: http.StatusOK,
			wantNodes:      []string{nodes["pass"]},
			doNotWantNodes: []string{nodes["blocked"], nodes["denied"], nodes["unsupported"]},
		},
		{
			name:           "Conflicting protocol and proto aliases -> 400",
			pathAndQuery:   "/sub/generic?protocol=vless&proto=vmess",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "Invalid protocol -> 400",
			pathAndQuery:   "/sub/generic?protocol=shadowsocks",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "Empty protocol query param -> no filter",
			pathAndQuery:   "/sub/generic?protocol=",
			expectedStatus: http.StatusOK,
			wantNodes:      []string{nodes["pass"], nodes["blocked"], nodes["denied"], nodes["unsupported"]},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, body := fetchBody(t, fmt.Sprintf("http://%s%s", addr, tc.pathAndQuery))
			if status != tc.expectedStatus {
				t.Fatalf("expected status %d, got %d for %s (body: %s)", tc.expectedStatus, status, tc.pathAndQuery, body)
			}
			if tc.expectedStatus == http.StatusOK {
				for _, want := range tc.wantNodes {
					if !strings.Contains(body, want) {
						t.Errorf("expected body to contain %q, got:\n%s", want, body)
					}
				}
				for _, notWant := range tc.doNotWantNodes {
					if strings.Contains(body, notWant) {
						t.Errorf("expected body NOT to contain %q, got:\n%s", notWant, body)
					}
				}
				if len(tc.wantNodes) == 0 && strings.TrimSpace(body) != "" {
					t.Errorf("expected empty body for no-match filter, got:\n%s", body)
				}
			}
		})
	}
}

func TestServer_InvalidParameters(t *testing.T) {
	st, _ := populateMixedStore(t)
	cfg := &config.ServeConfig{Path: "/sub", Format: "raw"}
	addr, cleanup := startTestServer(t, cfg, st)
	defer cleanup()

	invalidURLs := []string{
		"/sub?projection=invalid",
		"/sub/generic?projection=invalid",
		"/sub/gemini?projection=bad",
		"/sub?format=invalid",
		"/sub?protocol=invalid",
		"/sub?proto=invalid",
	}

	for _, u := range invalidURLs {
		t.Run(u, func(t *testing.T) {
			status, _ := fetchBody(t, fmt.Sprintf("http://%s%s", addr, u))
			if status != http.StatusBadRequest {
				t.Fatalf("expected 400 Bad Request for %s, got %d", u, status)
			}
		})
	}
}

func TestServer_FormatHandling_AndConfigImmutability(t *testing.T) {
	st, nodes := populateMixedStore(t)

	// Case 1: Configured format is "raw"
	cfgRaw := &config.ServeConfig{Path: "/sub", Format: "raw"}
	addr1, cleanup1 := startTestServer(t, cfgRaw, st)
	defer cleanup1()

	// Default request returns raw text
	_, bodyRawDefault := fetchBody(t, fmt.Sprintf("http://%s/sub", addr1))
	if !strings.Contains(bodyRawDefault, nodes["pass"]) {
		t.Errorf("expected raw link in default body, got: %s", bodyRawDefault)
	}

	// Request with ?format=base64 overrides locally
	_, bodyB64Override := fetchBody(t, fmt.Sprintf("http://%s/sub?format=base64", addr1))
	decodedOverride := decodeBody(t, bodyB64Override, true)
	if !strings.Contains(decodedOverride, nodes["pass"]) {
		t.Errorf("expected decoded base64 body to contain link, got: %s", decodedOverride)
	}

	// Subsequent default request must remain raw text (server config was not mutated)
	_, bodyRawSubsequent := fetchBody(t, fmt.Sprintf("http://%s/sub", addr1))
	if !strings.Contains(bodyRawSubsequent, nodes["pass"]) {
		t.Errorf("expected raw link on subsequent request (config must not mutate), got: %s", bodyRawSubsequent)
	}
	if cfgRaw.Format != "raw" {
		t.Errorf("cfg.Format was mutated to %s", cfgRaw.Format)
	}

	// Case 2: Configured format is "base64"
	cfgB64 := &config.ServeConfig{Path: "/sub", Format: "base64"}
	addr2, cleanup2 := startTestServer(t, cfgB64, st)
	defer cleanup2()

	// Default request returns base64
	_, bodyB64Default := fetchBody(t, fmt.Sprintf("http://%s/sub", addr2))
	decodedB64Default := decodeBody(t, bodyB64Default, true)
	if !strings.Contains(decodedB64Default, nodes["pass"]) {
		t.Errorf("expected decoded base64 in default body, got: %s", decodedB64Default)
	}

	// Request with ?format=raw returns plain text
	_, bodyRawOverride := fetchBody(t, fmt.Sprintf("http://%s/sub?format=raw", addr2))
	if !strings.Contains(bodyRawOverride, nodes["pass"]) {
		t.Errorf("expected raw link in override body, got: %s", bodyRawOverride)
	}

	// Subsequent default request remains base64
	_, bodyB64Subsequent := fetchBody(t, fmt.Sprintf("http://%s/sub", addr2))
	decodedB64Subsequent := decodeBody(t, bodyB64Subsequent, true)
	if !strings.Contains(decodedB64Subsequent, nodes["pass"]) {
		t.Errorf("expected base64 in subsequent body, got: %s", decodedB64Subsequent)
	}
	if cfgB64.Format != "base64" {
		t.Errorf("cfg.Format was mutated to %s", cfgB64.Format)
	}
}

func TestServer_Healthz_Observability(t *testing.T) {
	st, _ := populateMixedStore(t)
	cfg := &config.ServeConfig{Path: "/sub", Format: "raw"}
	addr, cleanup := startTestServer(t, cfg, st)
	defer cleanup()

	status, body := fetchBody(t, fmt.Sprintf("http://%s/healthz", addr))
	if status != http.StatusOK {
		t.Fatalf("expected /healthz 200 OK, got %d", status)
	}

	stats := st.Stats()
	if stats.Servable != 2 || stats.GenericServable != 4 {
		t.Fatalf("unexpected store stats: %+v", stats)
	}

	expectedLines := []string{
		"ok",
		"passed: 2",
		"failed: 3",
		"inconclusive: 0",
		"servable: 2",
		"generic_servable: 4",
		"cycles: 1",
	}

	for _, expected := range expectedLines {
		if !strings.Contains(body, expected) {
			t.Errorf("expected /healthz to contain line %q, got:\n%s", expected, body)
		}
	}
}

func TestServer_EmptyProjections(t *testing.T) {
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)

	// Test raw format on empty store
	cfgRaw := &config.ServeConfig{Path: "/sub", Format: "raw"}
	addrRaw, cleanupRaw := startTestServer(t, cfgRaw, st)
	defer cleanupRaw()

	for _, endpoint := range []string{"/sub", "/sub/generic", "/sub/gemini"} {
		status, body := fetchBody(t, fmt.Sprintf("http://%s%s", addrRaw, endpoint))
		if status != http.StatusOK {
			t.Errorf("expected 200 OK for %s on empty store, got %d", endpoint, status)
		}
		if strings.TrimSpace(body) != "" {
			t.Errorf("expected empty body for %s, got %q", endpoint, body)
		}
	}

	// Test base64 format on empty store
	cfgB64 := &config.ServeConfig{Path: "/sub", Format: "base64"}
	addrB64, cleanupB64 := startTestServer(t, cfgB64, st)
	defer cleanupB64()

	for _, endpoint := range []string{"/sub", "/sub/generic", "/sub/gemini"} {
		status, body := fetchBody(t, fmt.Sprintf("http://%s%s", addrB64, endpoint))
		if status != http.StatusOK {
			t.Errorf("expected 200 OK for %s on empty store, got %d", endpoint, status)
		}
		if strings.TrimSpace(body) != "" {
			t.Errorf("expected empty body for %s, got %q", endpoint, body)
		}
	}
}

func TestServer_PathSegmentRouting(t *testing.T) {
	st, _ := populateMixedStore(t)
	cfg := &config.ServeConfig{Path: "/sub", Format: "raw"}
	addr, cleanup := startTestServer(t, cfg, st)
	defer cleanup()

	// Matched routes -> 200
	validRoutes := []string{
		"/sub",
		"/sub/",
		"/sub/generic",
		"/sub/generic/",
		"/sub/gemini",
		"/sub/gemini/",
	}
	for _, r := range validRoutes {
		status, _ := fetchBody(t, fmt.Sprintf("http://%s%s", addr, r))
		if status != http.StatusOK {
			t.Errorf("expected 200 OK for valid route %s, got %d", r, status)
		}
	}

	// Unrelated routes -> 404
	invalidRoutes := []string{
		"/submarine",
		"/sub/unrelated",
		"/sub/generic/extra",
		"/other",
	}
	for _, r := range invalidRoutes {
		status, _ := fetchBody(t, fmt.Sprintf("http://%s%s", addr, r))
		if status != http.StatusNotFound {
			t.Errorf("expected 404 NotFound for route %s, got %d", r, status)
		}
	}
}
