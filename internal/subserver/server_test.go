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
