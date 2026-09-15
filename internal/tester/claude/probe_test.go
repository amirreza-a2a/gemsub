package claude_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"gemsub/internal/store"
	"gemsub/internal/tester/claude"
	"gemsub/internal/tester/gemini"
	"gemsub/internal/tester/transport"
)

func TestProbe_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<!DOCTYPE html><html><head><title>Claude</title></head><body>Welcome</body></html>"))
	}))
	defer ts.Close()

	dialer := &net.Dialer{}
	dialFn := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, addr)
	}

	res := claude.Probe(context.Background(), dialFn, claude.Config{
		URL:         ts.URL,
		Timeout:     2 * time.Second,
		DialTimeout: 1 * time.Second,
	})

	if res.Err != nil {
		t.Fatalf("unexpected err: %v", res.Err)
	}
	if res.Status != store.StatusPassed {
		t.Errorf("expected StatusPassed, got %s", res.Status)
	}
	if res.Category != store.ErrNone {
		t.Errorf("expected ErrNone, got %s", res.Category)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", res.StatusCode)
	}
	if res.Latency <= 0 {
		t.Errorf("expected positive latency, got %v", res.Latency)
	}
}

func TestProbe_RegionBlocked(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app-unavailable-in-region" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("<html><head><title>App unavailable in region</title></head></html>"))
			return
		}
		http.Redirect(w, r, "/app-unavailable-in-region", http.StatusFound)
	}))
	defer ts.Close()

	dialer := &net.Dialer{}
	dialFn := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, addr)
	}

	res := claude.Probe(context.Background(), dialFn, claude.Config{
		URL:         ts.URL,
		Timeout:     2 * time.Second,
		DialTimeout: 1 * time.Second,
	})

	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if res.Status != store.StatusFailed {
		t.Errorf("expected StatusFailed, got %s", res.Status)
	}
	if res.Category != store.ErrRegionBlocked {
		t.Errorf("expected ErrRegionBlocked, got %s", res.Category)
	}
}

func TestProbe_DialError(t *testing.T) {
	dialFn := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, fmt.Errorf("connection refused")
	}

	res := claude.Probe(context.Background(), dialFn, claude.Config{
		URL:         "http://127.0.0.1:54321",
		Timeout:     1 * time.Second,
		DialTimeout: 500 * time.Millisecond,
	})

	if res.Err == nil {
		t.Fatal("expected dial error, got nil")
	}
	if res.Status != store.StatusFailed {
		t.Errorf("expected StatusFailed, got %s", res.Status)
	}
	if res.Category != store.ErrProxyError {
		t.Errorf("expected ErrProxyError, got %s", res.Category)
	}
}

// TestProbe_ConcurrentDialFnSafety exercises concurrent dialing over a single shared dialFn closure
// and validates thread safety and absence of data races under -race.
func TestProbe_ConcurrentDialFnSafety(t *testing.T) {
	claudeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html><head><title>Claude</title></head><body>Hello</body></html>"))
	}))
	defer claudeServer.Close()

	geminiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html><head><title>Gemini</title></head><body><script>WIZ_global_data = {\"vXmutd\":\"\\\"US\\\"\"};</script></body></html>"))
	}))
	defer geminiServer.Close()

	// Wrap a dialer with a call counter to ensure concurrent invocations are safe
	var dialMu sync.Mutex
	dialCount := 0
	netDialer := &net.Dialer{}

	sharedDialFn := transport.DialFunc(func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialMu.Lock()
		dialCount++
		dialMu.Unlock()
		return netDialer.DialContext(ctx, network, addr)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const concurrencyIterations = 10
	var wg sync.WaitGroup
	wg.Add(concurrencyIterations * 2)

	claudeResults := make([]claude.ProbeResult, concurrencyIterations)
	geminiResults := make([]gemini.Result, concurrencyIterations)

	for i := 0; i < concurrencyIterations; i++ {
		idx := i
		// Concurrent Claude probe
		go func() {
			defer wg.Done()
			claudeResults[idx] = claude.Probe(ctx, sharedDialFn, claude.Config{
				URL:         claudeServer.URL,
				Timeout:     3 * time.Second,
				DialTimeout: 1 * time.Second,
			})
		}()

		// Concurrent Gemini probe
		go func() {
			defer wg.Done()
			geminiResults[idx] = gemini.Probe(ctx, sharedDialFn, gemini.Config{
				URL:          geminiServer.URL,
				BlockPhrases: []string{"not available"},
				Timeout:      3 * time.Second,
				DialTimeout:  1 * time.Second,
			})
		}()
	}

	wg.Wait()

	for i := 0; i < concurrencyIterations; i++ {
		if claudeResults[i].Err != nil {
			t.Errorf("iteration %d claude error: %v", i, claudeResults[i].Err)
		}
		if claudeResults[i].Status != store.StatusPassed {
			t.Errorf("iteration %d expected Claude StatusPassed, got %s", i, claudeResults[i].Status)
		}
		if geminiResults[i].Err != nil {
			t.Errorf("iteration %d gemini error: %v", i, geminiResults[i].Err)
		}
		if geminiResults[i].Status != store.StatusPassed {
			t.Errorf("iteration %d expected Gemini StatusPassed, got %s", i, geminiResults[i].Status)
		}
	}

	dialMu.Lock()
	totalDials := dialCount
	dialMu.Unlock()

	if totalDials < concurrencyIterations*2 {
		t.Errorf("expected at least %d dials, got %d", concurrencyIterations*2, totalDials)
	}
}
