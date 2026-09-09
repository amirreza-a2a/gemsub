package transport_test

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gemsub/internal/store"
	"gemsub/internal/tester/transport"
)

// directDialer returns a DialFunc connecting directly using net.Dialer.
func directDialer(t *testing.T) transport.DialFunc {
	t.Helper()
	var d net.Dialer
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return d.DialContext(ctx, network, addr)
	}
}

// roundTripperFunc wraps a function as an http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// 1. Genuine HTTPS 204 Success via httptest.NewTLSServer with TLS verification enabled against test CA
func TestProbe_HTTPS204Success_TLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	// Extract the test server's TLS configuration (containing its test CA root pool).
	// This exercises genuine TLS negotiation without disabling certificate verification globally.
	tlsConfig := srv.Client().Transport.(*http.Transport).TLSClientConfig

	res := transport.Probe(context.Background(), directDialer(t), transport.Config{
		HealthURL:       srv.URL + "/generate_204",
		HealthTimeout:   2 * time.Second,
		TLSClientConfig: tlsConfig,
	})

	if !res.OK {
		t.Fatalf("expected OK=true on genuine HTTPS 204, got false, err: %v", res.Error)
	}
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("expected status 204, got %d", res.StatusCode)
	}
	if res.Category != store.ErrNone {
		t.Errorf("expected ErrNone, got %v", res.Category)
	}
	if res.Latency <= 0 {
		t.Errorf("expected positive latency, got %v", res.Latency)
	}
}

// 2. Plain HTTP 204 Success
func TestProbe_HTTP204Success_PlainHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	res := transport.Probe(context.Background(), directDialer(t), transport.Config{
		HealthURL:     srv.URL + "/generate_204",
		HealthTimeout: 2 * time.Second,
	})

	if !res.OK {
		t.Fatalf("expected OK=true, got false, err: %v", res.Error)
	}
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("expected status 204, got %d", res.StatusCode)
	}
	if res.Category != store.ErrNone {
		t.Errorf("expected ErrNone, got %v", res.Category)
	}
	if res.Latency <= 0 {
		t.Errorf("expected positive latency, got %v", res.Latency)
	}
}

// 3. Redirect (301/302/307) rejection without following
func TestProbe_RedirectFailure(t *testing.T) {
	redirectCodes := []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect}
	for _, code := range redirectCodes {
		t.Run(http.StatusText(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "https://captive-portal.login/", code)
			}))
			defer srv.Close()

			res := transport.Probe(context.Background(), directDialer(t), transport.Config{
				HealthURL:     srv.URL + "/generate_204",
				HealthTimeout: 2 * time.Second,
			})

			if res.OK {
				t.Fatalf("expected OK=false on redirect %d, got true", code)
			}
			if res.StatusCode != code {
				t.Errorf("expected status %d, got %d", code, res.StatusCode)
			}
			if res.Category != store.ErrProxyError {
				t.Errorf("expected ErrProxyError on redirect, got %v", res.Category)
			}
		})
	}
}

// 4. HTTP 403 Forbidden failure
func TestProbe_HTTP403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	res := transport.Probe(context.Background(), directDialer(t), transport.Config{
		HealthURL:     srv.URL + "/generate_204",
		HealthTimeout: 2 * time.Second,
	})

	if res.OK {
		t.Fatal("expected OK=false on 403, got true")
	}
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", res.StatusCode)
	}
	if res.Category != store.ErrProxyError {
		t.Errorf("expected ErrProxyError, got %v", res.Category)
	}
}

// 5. HTTP 500 Internal Server Error failure
func TestProbe_HTTP500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	res := transport.Probe(context.Background(), directDialer(t), transport.Config{
		HealthURL:     srv.URL + "/generate_204",
		HealthTimeout: 2 * time.Second,
	})

	if res.OK {
		t.Fatal("expected OK=false on 500, got true")
	}
	if res.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", res.StatusCode)
	}
	if res.Category != store.ErrProxyError {
		t.Errorf("expected ErrProxyError, got %v", res.Category)
	}
}

// 6. HTTP 429 Rate Limited failure
func TestProbe_HTTP429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	res := transport.Probe(context.Background(), directDialer(t), transport.Config{
		HealthURL:     srv.URL + "/generate_204",
		HealthTimeout: 2 * time.Second,
	})

	if res.OK {
		t.Fatal("expected OK=false on 429, got true")
	}
	if res.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected status 429, got %d", res.StatusCode)
	}
	if res.Category != store.ErrProxyRateLimited {
		t.Errorf("expected ErrProxyRateLimited on 429, got %v", res.Category)
	}
	if res.RetryAfter != nil {
		t.Errorf("expected nil RetryAfter without header, got %v", res.RetryAfter)
	}
}

// 6b. HTTP 429 Rate Limited with Retry-After header
func TestProbe_HTTP429_RetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	res := transport.Probe(context.Background(), directDialer(t), transport.Config{
		HealthURL:     srv.URL + "/generate_204",
		HealthTimeout: 2 * time.Second,
	})

	if res.OK {
		t.Fatal("expected OK=false on 429, got true")
	}
	if res.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected status 429, got %d", res.StatusCode)
	}
	if res.Category != store.ErrProxyRateLimited {
		t.Errorf("expected ErrProxyRateLimited on 429, got %v", res.Category)
	}
	if res.RetryAfter == nil {
		t.Fatal("expected non-nil RetryAfter for 429 with header")
	}
	if *res.RetryAfter != 30*time.Second {
		t.Errorf("expected RetryAfter 30s, got %v", *res.RetryAfter)
	}
}

// 6c. HTTP 503 Service Unavailable with Retry-After header
func TestProbe_HTTP503_RetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "15")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	res := transport.Probe(context.Background(), directDialer(t), transport.Config{
		HealthURL:     srv.URL + "/generate_204",
		HealthTimeout: 2 * time.Second,
	})

	if res.OK {
		t.Fatal("expected OK=false on 503, got true")
	}
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", res.StatusCode)
	}
	if res.Category != store.ErrProxyError {
		t.Errorf("expected ErrProxyError on 503, got %v", res.Category)
	}
	if res.RetryAfter == nil {
		t.Fatal("expected non-nil RetryAfter for 503 with header")
	}
	if *res.RetryAfter != 15*time.Second {
		t.Errorf("expected RetryAfter 15s, got %v", *res.RetryAfter)
	}
}

// 7. Dial Failure (Connection Refused)
func TestProbe_DialConnectionRefused(t *testing.T) {
	dialFn := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, errors.New("dial tcp 127.0.0.1:1: getsockopt: connection refused")
	}

	res := transport.Probe(context.Background(), dialFn, transport.Config{
		HealthURL:     "http://127.0.0.1:1/generate_204",
		HealthTimeout: 2 * time.Second,
	})

	if res.OK {
		t.Fatal("expected OK=false on connection refused, got true")
	}
	if res.Category != store.ErrConnRefused {
		t.Errorf("expected ErrConnRefused, got %v", res.Category)
	}
}

// 8. TLS Failure: verifies both synthetic error mapping and real TLS certificate validation failure
func TestProbe_TLSFailure(t *testing.T) {
	t.Run("SyntheticError", func(t *testing.T) {
		dialFn := func(ctx context.Context, network, addr string) (net.Conn, error) {
			return nil, errors.New("tls: handshake failure: remote error: tls: internal error")
		}

		res := transport.Probe(context.Background(), dialFn, transport.Config{
			HealthURL:     "https://example.com/generate_204",
			HealthTimeout: 2 * time.Second,
		})

		if res.OK {
			t.Fatal("expected OK=false on TLS error, got true")
		}
		if res.Category != store.ErrTLS {
			t.Errorf("expected ErrTLS, got %v", res.Category)
		}
	})

	t.Run("UntrustedCertificateAgainstRealTLSListener", func(t *testing.T) {
		// NewTLSServer creates a TLS server with a self-signed certificate.
		// By passing TLSClientConfig: nil (default), the client verifies against system CAs and must fail.
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()

		res := transport.Probe(context.Background(), directDialer(t), transport.Config{
			HealthURL:       srv.URL + "/generate_204",
			HealthTimeout:   2 * time.Second,
			TLSClientConfig: nil, // Default system CAs — must reject self-signed test cert
		})

		if res.OK {
			t.Fatal("expected OK=false when connecting to untrusted TLS server with default CAs")
		}
		if res.Category != store.ErrTLS {
			t.Errorf("expected ErrTLS for untrusted certificate error, got %v (err: %v)", res.Category, res.Error)
		}
	})
}

// 9. Timeout Exceeded
func TestProbe_TimeoutExceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	start := time.Now()
	res := transport.Probe(context.Background(), directDialer(t), transport.Config{
		HealthURL:     srv.URL + "/generate_204",
		HealthTimeout: 50 * time.Millisecond, // Shorter than server sleep
	})
	elapsed := time.Since(start)

	if res.OK {
		t.Fatal("expected OK=false on timeout, got true")
	}
	if res.Category != store.ErrTimeout {
		t.Errorf("expected ErrTimeout, got %v", res.Category)
	}
	if elapsed > 150*time.Millisecond {
		t.Errorf("expected timeout to terminate within bound, took %v", elapsed)
	}
}

// 10. Parent Context Cancellation
func TestProbe_ParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	res := transport.Probe(ctx, directDialer(t), transport.Config{
		HealthURL:     "http://127.0.0.1:1234/generate_204",
		HealthTimeout: 2 * time.Second,
	})

	if res.OK {
		t.Fatal("expected OK=false on canceled context, got true")
	}
	if res.Category != store.ErrTimeout {
		t.Errorf("expected ErrTimeout on context cancel, got %v", res.Category)
	}
}

// 11. Single Request Assertion (No redirect follows)
func TestProbe_NoRedirectFollowing_SingleRequest(t *testing.T) {
	var requestCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		http.Redirect(w, r, "/target", http.StatusFound)
	}))
	defer srv.Close()

	res := transport.Probe(context.Background(), directDialer(t), transport.Config{
		HealthURL:     srv.URL + "/generate_204",
		HealthTimeout: 2 * time.Second,
	})

	if res.OK {
		t.Fatal("expected OK=false on redirect")
	}
	if count := atomic.LoadInt32(&requestCount); count != 1 {
		t.Errorf("expected exactly 1 request to server, got %d", count)
	}
}

// trackingBody records every Read and Close invocation on a response body.
type trackingBody struct {
	readCalls  atomic.Int32
	bytesRead  atomic.Int64
	closeCalls atomic.Int32
}

func (b *trackingBody) Read(p []byte) (int, error) {
	b.readCalls.Add(1)
	b.bytesRead.Add(int64(len(p)))
	return 0, io.EOF
}

func (b *trackingBody) Close() error {
	b.closeCalls.Add(1)
	return nil
}

// 12. Observable Zero-Byte Body Read + Close Assertion (P1-2)
func TestProbe_ObservableBodyNonConsumption(t *testing.T) {
	body := &trackingBody{}

	// Mock RoundTripper that injects our observable tracking body
	rt := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       body,
		}, nil
	})

	res := transport.Probe(context.Background(), nil, transport.Config{
		HealthURL: "https://www.gstatic.com/generate_204",
		Transport: rt,
	})

	if !res.OK {
		t.Fatalf("expected probe OK=true, got error: %v", res.Error)
	}
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", res.StatusCode)
	}

	// Assert that Read was NEVER called by the probe
	if reads := body.readCalls.Load(); reads != 0 {
		t.Errorf("expected 0 calls to Body.Read, got %d", reads)
	}
	if bytes := body.bytesRead.Load(); bytes != 0 {
		t.Errorf("expected 0 bytes read from body, got %d", bytes)
	}

	// Assert that Close was called exactly once
	if closes := body.closeCalls.Load(); closes != 1 {
		t.Errorf("expected exactly 1 call to Body.Close, got %d", closes)
	}
}

// Socket-level body non-consumption: verifies probe finishes immediately without consuming streaming payload
func TestProbe_SocketLevelBodyNonConsumption(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusNoContent)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Write payload bytes that must not be read by the probe
		_, _ = w.Write([]byte("large body payload that must not be read"))
	}))
	defer srv.Close()

	start := time.Now()
	res := transport.Probe(context.Background(), directDialer(t), transport.Config{
		HealthURL:     srv.URL + "/generate_204",
		HealthTimeout: 1 * time.Second,
	})

	if !res.OK {
		t.Fatalf("expected OK=true, got err: %v", res.Error)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Errorf("probe took longer than expected; might be reading body: %v", time.Since(start))
	}
}

// 13. Target-Agnostic Verification (Source Code AST inspection)
func TestProbe_TargetAgnostic(t *testing.T) {
	src, err := os.ReadFile("probe.go")
	if err != nil {
		t.Fatalf("failed to read probe.go: %v", err)
	}

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "probe.go", src, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("failed to parse probe.go imports: %v", err)
	}

	for _, imp := range node.Imports {
		path := imp.Path.Value
		if strings.Contains(path, "gemini") || strings.Contains(path, "claude") || strings.Contains(path, "google") {
			t.Errorf("probe.go contains target-specific import: %s", path)
		}
	}

	srcStr := string(src)
	forbiddenWords := []string{
		"WIZ_global_data",
		"LOCATION_REJECTED",
		"bardchatui",
		"block_phrases",
		"BlockPhrases",
		"Gemini",
		"Claude",
	}

	for _, word := range forbiddenWords {
		if strings.Contains(srcStr, word) {
			t.Errorf("probe.go contains target-specific keyword: %s", word)
		}
	}
}

// 14. Custom Endpoint 200 OK Success (/healthz)
func TestProbe_CustomEndpointSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"healthy"}`))
	}))
	defer srv.Close()

	res := transport.Probe(context.Background(), directDialer(t), transport.Config{
		HealthURL:     srv.URL + "/healthz", // Not generate_204
		HealthTimeout: 2 * time.Second,
	})

	if !res.OK {
		t.Fatalf("expected custom endpoint 200 OK to succeed, got: %v", res.Error)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", res.StatusCode)
	}
	if res.Category != store.ErrNone {
		t.Errorf("expected ErrNone, got %v", res.Category)
	}
}

// 15. Captive Portal Detection: generate_204 returning 200 OK fails
func TestProbe_Generate204Returning200Fails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // Captive portal returns 200 with HTML login
		_, _ = w.Write([]byte("<html>Captive Portal Login</html>"))
	}))
	defer srv.Close()

	res := transport.Probe(context.Background(), directDialer(t), transport.Config{
		HealthURL:     srv.URL + "/generate_204",
		HealthTimeout: 2 * time.Second,
	})

	if res.OK {
		t.Fatal("expected generate_204 returning 200 to fail (captive portal), got OK=true")
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", res.StatusCode)
	}
	if res.Category != store.ErrProxyError {
		t.Errorf("expected ErrProxyError, got %v", res.Category)
	}
}

// 16. Non-generate-204 endpoints with substring matches succeed on 200 OK
func TestProbe_NonGenerate204Substrings_SucceedOn200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	testPaths := []string{
		"/keygen_2048/health",
		"/api/generate_204_report",
		"/metrics?query=generate_204",
	}

	for _, p := range testPaths {
		t.Run(p, func(t *testing.T) {
			res := transport.Probe(context.Background(), directDialer(t), transport.Config{
				HealthURL:     srv.URL + p,
				HealthTimeout: 2 * time.Second,
			})

			if !res.OK {
				t.Fatalf("expected OK=true for generic endpoint %s returning 200, got false: %v", p, res.Error)
			}
			if res.StatusCode != http.StatusOK {
				t.Errorf("expected status 200, got %d", res.StatusCode)
			}
			if res.Category != store.ErrNone {
				t.Errorf("expected ErrNone, got %v", res.Category)
			}
		})
	}
}

// 17. Concurrency / race test
func TestProbe_ConcurrentExecution(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	const concurrency = 20
	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			res := transport.Probe(context.Background(), directDialer(t), transport.Config{
				HealthURL:     srv.URL + "/generate_204",
				HealthTimeout: 2 * time.Second,
			})
			if !res.OK {
				t.Errorf("concurrent probe failed: %v", res.Error)
			}
			if res.StatusCode != http.StatusNoContent {
				t.Errorf("expected status 204, got %d", res.StatusCode)
			}
		}()
	}

	wg.Wait()
}
