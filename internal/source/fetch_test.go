package source

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchAll_Success(t *testing.T) {
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("vless://user1@1.1.1.1:443\nvmess://user2@2.2.2.2:443\n"))
	}))
	defer srv1.Close()

	subContent := "vless://user1@1.1.1.1:443\ntrojan://user3@3.3.3.3:443\n"
	b64Sub := base64.StdEncoding.EncodeToString([]byte(subContent))
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(b64Sub))
	}))
	defer srv2.Close()

	ctx := context.Background()
	links, errs := FetchAll(ctx, []string{srv1.URL, srv2.URL})

	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
	if len(links) != 3 {
		t.Fatalf("expected 3 deduplicated links, got %d: %v", len(links), links)
	}
	expected := []string{
		"vless://user1@1.1.1.1:443",
		"vmess://user2@2.2.2.2:443",
		"trojan://user3@3.3.3.3:443",
	}
	for i, exp := range expected {
		if links[i] != exp {
			t.Errorf("link[%d]: expected %q, got %q", i, exp, links[i])
		}
	}
}

func TestFetchAll_PreCancelledContext(t *testing.T) {
	var requestCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("vless://user@127.0.0.1:443\n"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	links, errs := FetchAll(ctx, []string{srv.URL})
	duration := time.Since(start)

	if duration > 100*time.Millisecond {
		t.Errorf("expected fast exit, took %v", duration)
	}
	if len(links) != 0 {
		t.Errorf("expected 0 links, got %d", len(links))
	}
	if len(errs) == 0 {
		t.Fatalf("expected errors on cancelled context, got none")
	}
	if !errors.Is(errs[0], context.Canceled) {
		t.Errorf("expected context.Canceled error, got %v", errs[0])
	}
	if atomic.LoadInt32(&requestCount) != 0 {
		t.Errorf("expected 0 requests to server, got %d", requestCount)
	}
}

func TestFetchAll_ContextCancelled_InFlight(t *testing.T) {
	blockCh := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blockCh
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("vless://user@127.0.0.1:443\n"))
	}))
	defer func() {
		close(blockCh)
		srv.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	links, errs := FetchAll(ctx, []string{srv.URL})
	duration := time.Since(start)

	if duration > 500*time.Millisecond {
		t.Errorf("expected fast exit upon cancellation, took %v", duration)
	}
	if len(links) != 0 {
		t.Errorf("expected 0 links, got %d", len(links))
	}
	if len(errs) == 0 {
		t.Fatalf("expected errors on cancelled context, got none")
	}
	if !errors.Is(errs[0], context.Canceled) {
		t.Errorf("expected context.Canceled error, got %v", errs[0])
	}
}

func TestFetchAll_ContextCancelled_RetryBackoff(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		// Wait for first attempt to complete, then cancel context during the 1-second backoff sleep
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	links, errs := FetchAll(ctx, []string{srv.URL})
	duration := time.Since(start)

	// Normal retry sleep is 1s; with cancellation it should abort well under 500ms
	if duration > 500*time.Millisecond {
		t.Errorf("expected retry backoff to abort immediately, took %v", duration)
	}
	if len(links) != 0 {
		t.Errorf("expected 0 links, got %d", len(links))
	}
	if len(errs) == 0 {
		t.Fatalf("expected error on cancelled context, got none")
	}
	if !errors.Is(errs[0], context.Canceled) {
		t.Errorf("expected context.Canceled error, got %v", errs[0])
	}
	// Verify it didn't do subsequent attempts
	if count := atomic.LoadInt32(&attempts); count != 1 {
		t.Errorf("expected exactly 1 attempt before cancellation, got %d", count)
	}
}

func TestFetchOne_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := fetchOne(ctx, srv.URL)
	duration := time.Since(start)

	if duration > 500*time.Millisecond {
		t.Errorf("expected fetchOne to abort quickly on context timeout, took %v", duration)
	}
	if err == nil {
		t.Fatalf("expected error from cancelled fetchOne, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("expected deadline exceeded or canceled error, got %v", err)
	}
}
