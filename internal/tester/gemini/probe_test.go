package gemini_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gemsub/internal/store"
	"gemsub/internal/tester/gemini"
	"gemsub/internal/tester/transport"
)

// directDialer returns a DialFunc connecting directly using net.Dialer.
func directDialer() transport.DialFunc {
	var d net.Dialer
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return d.DialContext(ctx, network, addr)
	}
}

// --- ClassifyResponse tests (migrated from classifier_test.go) ---

func TestClassifyResponse_HttpStatusMatrix(t *testing.T) {
	blockPhrases := []string{"isn't currently supported"}

	tests := []struct {
		name       string
		statusCode int
		wantStatus store.Status
		wantCat    store.ErrorCategory
		wantRetry  bool
	}{
		{"500 Internal Server Error", 500, store.StatusInconclusive, store.ErrTargetError, false},
		{"502 Bad Gateway", 502, store.StatusInconclusive, store.ErrTargetError, false},
		{"503 Service Unavailable", 503, store.StatusInconclusive, store.ErrTargetError, true},
		{"504 Gateway Timeout", 504, store.StatusInconclusive, store.ErrTargetError, false},
		{"403 Forbidden", 403, store.StatusFailed, store.ErrTargetDenied, false},
		{"429 Too Many Requests", 429, store.StatusInconclusive, store.ErrTargetRateLimited, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tt.statusCode}
			res := gemini.ClassifyResponse(resp, nil, blockPhrases)
			if res.Status != tt.wantStatus {
				t.Errorf("status: got %s, want %s", res.Status, tt.wantStatus)
			}
			if res.Category != tt.wantCat {
				t.Errorf("category: got %s, want %s", res.Category, tt.wantCat)
			}
			if res.Retryable != tt.wantRetry {
				t.Errorf("retryable: got %v, want %v", res.Retryable, tt.wantRetry)
			}
		})
	}
}

func TestClassifyResponse_WIZGlobalData_GeoBlocking(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Server": []string{"ESF"}},
	}

	// RU restriction test case
	bodyBlockedRU := []byte(`<!doctype html><html><head><title>Google Gemini</title>
<script>window.WIZ_global_data = {"vXmutd":"%.@.\"RU\",\"ZZ\",\"2u255g\\u003d\\u003d\"]"};</script>
</head><body><div id="bardchatui"></div></body></html>`)

	resRU := gemini.ClassifyResponse(resp, bodyBlockedRU, nil)
	if resRU.Status != store.StatusFailed || resRU.Category != store.ErrRegionBlocked {
		t.Errorf("expected blocked country RU to fail with ErrRegionBlocked, got status=%s category=%s",
			resRU.Status, resRU.Category)
	}

	// IR restriction test case
	bodyBlockedIR := []byte(`<!doctype html><html><head><title>Google Gemini</title>
<script>window.WIZ_global_data = {"vXmutd":"%.@.\"IR\",\"ZZ\",\"Kgm6wTkAEZgAAAAAA9EAUA\\u003d\\u003d\"]"};</script>
</head><body><div id="bardchatui"></div></body></html>`)

	resIR := gemini.ClassifyResponse(resp, bodyBlockedIR, nil)
	if resIR.Status != store.StatusFailed || resIR.Category != store.ErrRegionBlocked {
		t.Errorf("expected blocked country IR to fail with ErrRegionBlocked, got status=%s category=%s",
			resIR.Status, resIR.Category)
	}

	// Explicit restriction flag test case
	bodyBlockedFlag := []byte(`<!doctype html><html><head><title>Google Gemini</title>
<script>window.WIZ_global_data = {"status":"LOCATION_REJECTED"};</script>
</head><body><div id="bardchatui"></div></body></html>`)

	resFlag := gemini.ClassifyResponse(resp, bodyBlockedFlag, nil)
	if resFlag.Status != store.StatusFailed || resFlag.Category != store.ErrRegionBlocked {
		t.Errorf("expected LOCATION_REJECTED to fail with ErrRegionBlocked, got status=%s category=%s",
			resFlag.Status, resFlag.Category)
	}

	// Allowed country test case (US)
	bodyCleanUS := []byte(`<!doctype html><html><head><title>Google Gemini</title>
<script>window.WIZ_global_data = {"vXmutd":"%.@.\"US\",\"ZZ\",\"2u255g\\u003d\\u003d\"]"};</script>
</head><body><div id="bardchatui"></div></body></html>`)

	resUS := gemini.ClassifyResponse(resp, bodyCleanUS, nil)
	if resUS.Status != store.StatusPassed {
		t.Errorf("expected clean country US to pass, got status=%s category=%s reason=%q",
			resUS.Status, resUS.Category, resUS.Reason)
	}
}

func TestClassifyResponse_RealisticGeminiLanding_Passes(t *testing.T) {
	blockPhrases := []string{
		"isn\u2019t currently supported in your country",
		"not available in your country",
		"not available in your region",
	}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Server": []string{"ESF"}},
	}
	body := []byte(`<!doctype html><html><head>
<meta charset="utf-8">
<title>Google Gemini</title>
</head><body>
<div id="WIZ_global_data"></div>
<div id="bardchatui">
<p>Welcome to Gemini. Start a conversation.</p>
</div>
</body></html>`)

	res := gemini.ClassifyResponse(resp, body, blockPhrases)
	if res.Status != store.StatusPassed {
		t.Errorf("expected realistic Gemini page to pass; got status=%s category=%s reason=%q",
			res.Status, res.Category, res.Reason)
	}
}

func TestClassifyResponse_GeminiWithGoogleCookieNoBrandInTitle_Passes(t *testing.T) {
	blockPhrases := []string{"not available in your country"}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Server":     []string{"GSE"},
			"Set-Cookie": []string{"NID=123; domain=.google.com; path=/; HttpOnly"},
		},
	}
	body := []byte(`<!doctype html><html><head><title>Some App</title></head>
<body><p>Welcome to Gemini</p></body></html>`)

	res := gemini.ClassifyResponse(resp, body, blockPhrases)
	if res.Status != store.StatusPassed {
		t.Errorf("expected Google server with brand signal to pass; got status=%s reason=%q",
			res.Status, res.Reason)
	}
}

func TestClassifyResponse_RegionalBlock_UnicodeApostrophe_Fails(t *testing.T) {
	blockPhrases := []string{
		"isn\u2019t currently supported in your country",
		"not available in your country",
	}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Server": []string{"ESF"}},
	}
	body := []byte("<!doctype html><html><head><title>Google Gemini</title></head>\n<body><p>Gemini isn\u2019t currently supported in your country. Stay tuned!</p></body></html>")

	res := gemini.ClassifyResponse(resp, body, blockPhrases)
	if res.Status != store.StatusFailed || res.Category != store.ErrRegionBlocked {
		t.Errorf("expected regional block to fail with ErrRegionBlocked; got status=%s category=%s",
			res.Status, res.Category)
	}
}

func TestClassifyResponse_FalsePositive_HoroscopeSite_Fails(t *testing.T) {
	blockPhrases := []string{"not available in your country"}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Server": []string{"nginx/1.24.0"}},
	}
	body := []byte(`<html><head><title>Gemini Daily Horoscope</title></head>
<body><p>Your sign today is Gemini. The stars align.</p></body></html>`)

	res := gemini.ClassifyResponse(resp, body, blockPhrases)
	if res.Status != store.StatusFailed {
		t.Errorf("expected false-positive horoscope page to FAIL; got status=%s category=%s reason=%q",
			res.Status, res.Category, res.Reason)
	}
}

func TestClassifyResponse_FalsePositive_CaptivePortal_Fails(t *testing.T) {
	blockPhrases := []string{"not available in your country"}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Server": []string{"RouterOS"}},
	}
	body := []byte(`<html><head><title>RouterOS Login</title></head>
<body><p>Welcome to Gemini Cafe WiFi. Please login.</p></body></html>`)

	res := gemini.ClassifyResponse(resp, body, blockPhrases)
	if res.Status != store.StatusFailed {
		t.Errorf("expected captive portal to FAIL; got status=%s category=%s reason=%q",
			res.Status, res.Category, res.Reason)
	}
}

func TestClassifyResponse_StrippedHeaders_WithAppMarkers_Passes(t *testing.T) {
	blockPhrases := []string{"not available in your country"}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Server": []string{"nginx"}},
	}
	body := []byte(`<html><head><title>Google Gemini</title></head>
<body><div id="bardchatui">data</div></body></html>`)

	res := gemini.ClassifyResponse(resp, body, blockPhrases)
	if res.Status != store.StatusPassed {
		t.Errorf("expected Gemini with stripped headers + app markers to pass; got status=%s reason=%q",
			res.Status, res.Reason)
	}
}

func TestClassifyResponse_TitleGeminiAlone_NoAppMarkers_Fails(t *testing.T) {
	blockPhrases := []string{"not available in your country"}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Server": []string{"Apache/2.4"}},
	}
	body := []byte(`<html><head><title>Gemini</title></head>
<body><p>Welcome to our site about the Gemini constellation.</p></body></html>`)

	res := gemini.ClassifyResponse(resp, body, blockPhrases)
	if res.Status != store.StatusFailed {
		t.Errorf("expected title-only 'Gemini' without markers to FAIL; got status=%s reason=%q",
			res.Status, res.Reason)
	}
}

func TestClassifyResponse_RealGeminiPageWithAdminUrl_Passes(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Server": []string{"ESF"}},
	}
	// Real Gemini responses contain the ServiceNotAllowed admin URL in WIZ_global_data.
	bodyWithAdminLink := []byte(`<!doctype html><html><head><title>Google Gemini</title>
<script>window.WIZ_global_data = {
	"vXmutd":"%.@.\"US\",\"ZZ\",\"Kgm6wTkAEZgAAAAAA9EAUA\\u003d\\u003d\"]",
	"adminUrl":"https://admin.google.com/ServiceNotAllowed?application=47208553126"
};</script>
</head><body><div id="bardchatui"></div></body></html>`)

	res := gemini.ClassifyResponse(resp, bodyWithAdminLink, nil)
	if res.Status != store.StatusPassed {
		t.Errorf("expected real Gemini page with admin fallback URL to pass, got status=%s category=%s reason=%q",
			res.Status, res.Category, res.Reason)
	}
}

func TestClassifyResponse_Large1MBPayload_MaintainsPrecision(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Server": []string{"ESF"}},
	}
	blockPhrases := []string{"isn\u2019t currently supported in your country"}

	var sb strings.Builder
	sb.WriteString("<!doctype html><html><head><title>Google Gemini</title><style>")
	for sb.Len() < 500*1024 {
		sb.WriteString("body { margin: 0; padding: 0; font-family: 'Google Sans'; }\n")
	}
	sb.WriteString("</style><script>window.WIZ_global_data = {\"vXmutd\":\"%.@.\\\"US\\\",\\\"ZZ\\\",\\\"2u255g\\\\u003d\\\\u003d\\\"\"]\";};</script></head><body><div id=\"bardchatui\">")
	for sb.Len() < 1<<20 {
		sb.WriteString("<p>Welcome to Google Gemini AI service.</p>\n")
	}
	sb.WriteString("</div></body></html>")
	clean1MB := []byte(sb.String())

	resClean := gemini.ClassifyResponse(resp, clean1MB, blockPhrases)
	if resClean.Status != store.StatusPassed {
		t.Errorf("expected clean 1MB Gemini payload to pass, got status=%s category=%s reason=%q",
			resClean.Status, resClean.Category, resClean.Reason)
	}

	blockedCountry1MB := []byte(strings.Replace(string(clean1MB), `\"US\"`, `\"IR\"`, 1))
	resBlockedCountry := gemini.ClassifyResponse(resp, blockedCountry1MB, blockPhrases)
	if resBlockedCountry.Status != store.StatusFailed || resBlockedCountry.Category != store.ErrRegionBlocked {
		t.Errorf("expected 1MB blocked country payload to fail with ErrRegionBlocked, got status=%s category=%s",
			resBlockedCountry.Status, resBlockedCountry.Category)
	}

	blockedPhrase1MB := []byte(strings.Replace(string(clean1MB), "Welcome to Google Gemini AI service.", "Gemini isn\u2019t currently supported in your country.", 1))
	resBlockedPhrase := gemini.ClassifyResponse(resp, blockedPhrase1MB, blockPhrases)
	if resBlockedPhrase.Status != store.StatusFailed || resBlockedPhrase.Category != store.ErrRegionBlocked {
		t.Errorf("expected 1MB regional block phrase payload to fail with ErrRegionBlocked, got status=%s category=%s",
			resBlockedPhrase.Status, resBlockedPhrase.Category)
	}
}

// TestProbe_InspectionBoundary_1MBTruncation verifies that payloads larger than 1 MiB
// are truncated at the 1 MiB boundary by both gemini.Probe (via io.LimitReader) and
// gemini.ClassifyResponse (via buffer truncation), ensuring that regional restriction
// markers placed strictly beyond 1 MiB are not observed.
func TestProbe_InspectionBoundary_1MBTruncation(t *testing.T) {
	blockPhrases := []string{"isn't currently supported in your country"}

	head := "<!doctype html><html><head><title>Google Gemini</title>" +
		`<script>window.WIZ_global_data = {"vXmutd":"%.@.\"US\",\"ZZ\",\"2u255g\\u003d\\u003d\"]"};</script></head>` +
		`<body><div id="bardchatui">`
	tail := "</div></body></html>"

	// 1. Build a payload > 1 MiB where the regional block phrase is placed strictly AFTER the 1 MiB boundary.
	const oneMiB = 1 << 20
	var sb strings.Builder
	sb.WriteString(head)
	for sb.Len() < oneMiB+64*1024 {
		sb.WriteString("<p>Clean content within first 1MB.</p>\n")
	}
	markerOffset := sb.Len()
	if markerOffset <= oneMiB {
		t.Fatalf("test invariant broken: marker offset %d must be > 1 MiB (%d)", markerOffset, oneMiB)
	}
	sb.WriteString("<p>isn't currently supported in your country</p>\n")
	sb.WriteString(tail)
	payloadPast1MB := []byte(sb.String())

	// Test via gemini.Probe through httptest.Server (exercises io.LimitReader):
	srvPast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "ESF")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payloadPast1MB)
	}))
	defer srvPast.Close()

	resPast := gemini.Probe(context.Background(), directDialer(), gemini.Config{
		URL:          srvPast.URL,
		BlockPhrases: blockPhrases,
		Timeout:      2 * time.Second,
	})
	if resPast.Status != store.StatusPassed {
		t.Errorf("expected probe to pass when block phrase is strictly beyond 1MB; got status=%s category=%s reason=%q",
			resPast.Status, resPast.Category, resPast.Reason)
	}

	// Also verify directly in ClassifyResponse truncation:
	resClassifyPast := gemini.ClassifyResponse(&http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Server": []string{"ESF"}},
	}, payloadPast1MB, blockPhrases)
	if resClassifyPast.Status != store.StatusPassed {
		t.Errorf("expected ClassifyResponse to pass when block phrase is strictly beyond 1MB; got status=%s category=%s reason=%q",
			resClassifyPast.Status, resClassifyPast.Category, resClassifyPast.Reason)
	}

	// 2. Contrasting case: Place restriction phrase WITHIN the first 1 MiB.
	var sbWithin strings.Builder
	sbWithin.WriteString(head)
	sbWithin.WriteString("<p>isn't currently supported in your country</p>\n")
	for sbWithin.Len() < oneMiB+64*1024 {
		sbWithin.WriteString("<p>Trailing filler.</p>\n")
	}
	sbWithin.WriteString(tail)
	payloadWithin1MB := []byte(sbWithin.String())

	srvWithin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "ESF")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payloadWithin1MB)
	}))
	defer srvWithin.Close()

	resWithin := gemini.Probe(context.Background(), directDialer(), gemini.Config{
		URL:          srvWithin.URL,
		BlockPhrases: blockPhrases,
		Timeout:      2 * time.Second,
	})
	if resWithin.Status != store.StatusFailed || resWithin.Category != store.ErrRegionBlocked {
		t.Errorf("expected probe to fail with ErrRegionBlocked when block phrase is within first 1MB; got status=%s category=%s",
			resWithin.Status, resWithin.Category)
	}

	resClassifyWithin := gemini.ClassifyResponse(&http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Server": []string{"ESF"}},
	}, payloadWithin1MB, blockPhrases)
	if resClassifyWithin.Status != store.StatusFailed || resClassifyWithin.Category != store.ErrRegionBlocked {
		t.Errorf("expected ClassifyResponse to fail with ErrRegionBlocked when block phrase is within first 1MB; got status=%s category=%s",
			resClassifyWithin.Status, resClassifyWithin.Category)
	}
}

func TestClassifyResponse_NilResponse(t *testing.T) {
	res := gemini.ClassifyResponse(nil, nil, nil)
	if res.Status != store.StatusFailed || res.Category != store.ErrTargetOther {
		t.Errorf("expected nil response to fail with ErrTargetOther, got status=%s category=%s",
			res.Status, res.Category)
	}
}

// --- Probe integration tests ---

func TestProbe_GeminiSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify browser headers are sent
		ua := r.Header.Get("User-Agent")
		if !strings.Contains(ua, "Chrome") {
			t.Errorf("expected Chrome User-Agent, got %q", ua)
		}
		if r.Header.Get("Sec-CH-UA") == "" {
			t.Error("expected Sec-CH-UA header")
		}

		w.Header().Set("Server", "ESF")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Google Gemini</title></head>
<body><div id="bardchatui"><p>Welcome to Gemini</p></div></body></html>`))
	}))
	defer srv.Close()

	res := gemini.Probe(context.Background(), directDialer(), gemini.Config{
		URL:          srv.URL,
		BlockPhrases: []string{"not available in your country"},
		Timeout:      2 * time.Second,
	})

	if res.Status != store.StatusPassed {
		t.Fatalf("expected StatusPassed, got status=%s category=%s reason=%q",
			res.Status, res.Category, res.Reason)
	}
	if res.Category != store.ErrNone {
		t.Errorf("expected ErrNone, got %s", res.Category)
	}
	if res.StatusCode != 200 {
		t.Errorf("expected 200, got %d", res.StatusCode)
	}
	if res.Latency <= 0 {
		t.Errorf("expected positive latency, got %v", res.Latency)
	}
}

func TestProbe_GeminiRegionBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "ESF")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Google Gemini</title></head>
<body><p>Gemini isn't currently supported in your country.</p></body></html>`))
	}))
	defer srv.Close()

	res := gemini.Probe(context.Background(), directDialer(), gemini.Config{
		URL:          srv.URL,
		BlockPhrases: []string{"isn't currently supported in your country"},
		Timeout:      2 * time.Second,
	})

	if res.Status != store.StatusFailed {
		t.Fatalf("expected StatusFailed, got %s", res.Status)
	}
	if res.Category != store.ErrRegionBlocked {
		t.Errorf("expected ErrRegionBlocked, got %s", res.Category)
	}
}

func TestProbe_Gemini403Denied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	res := gemini.Probe(context.Background(), directDialer(), gemini.Config{
		URL:     srv.URL,
		Timeout: 2 * time.Second,
	})

	if res.Status != store.StatusFailed {
		t.Fatalf("expected StatusFailed on 403, got %s", res.Status)
	}
	if res.Category != store.ErrTargetDenied {
		t.Errorf("expected ErrTargetDenied, got %s", res.Category)
	}
	if res.StatusCode != 403 {
		t.Errorf("expected 403, got %d", res.StatusCode)
	}
}

func TestProbe_Gemini429RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	res := gemini.Probe(context.Background(), directDialer(), gemini.Config{
		URL:     srv.URL,
		Timeout: 2 * time.Second,
	})

	if res.Status != store.StatusInconclusive {
		t.Fatalf("expected StatusInconclusive on 429, got %s", res.Status)
	}
	if res.Category != store.ErrTargetRateLimited {
		t.Errorf("expected ErrTargetRateLimited, got %s", res.Category)
	}
	if !res.Retryable {
		t.Error("expected 429 to be retryable")
	}
	if res.RetryAfter == nil {
		t.Fatal("expected non-nil RetryAfter")
	}
	if *res.RetryAfter != 5*time.Second {
		t.Errorf("expected RetryAfter = 5s, got %v", *res.RetryAfter)
	}
}

func TestProbe_DialFailure(t *testing.T) {
	dialFn := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("connection refused")}
	}

	res := gemini.Probe(context.Background(), dialFn, gemini.Config{
		URL:     "http://127.0.0.1:1/",
		Timeout: 2 * time.Second,
	})

	if res.Status == store.StatusPassed {
		t.Fatal("expected probe to fail on dial error")
	}
	if res.Status != store.StatusFailed {
		t.Errorf("expected StatusFailed, got %s", res.Status)
	}
	if res.Err == nil {
		t.Fatal("expected non-nil Err on dial failure")
	}
	if !strings.Contains(res.Err.Error(), "connection refused") {
		t.Errorf("expected connection refused error, got %v", res.Err)
	}
}

func TestProbe_TargetAgnostic(t *testing.T) {
	// Verify gemini package does NOT import target-specific transport code
	// by checking that a transport failure results in a generic proxy error,
	// not a Gemini-specific classification.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html><body>Not Gemini</body></html>"))
	}))
	defer srv.Close()

	res := gemini.Probe(context.Background(), directDialer(), gemini.Config{
		URL:     srv.URL,
		Timeout: 2 * time.Second,
	})

	if res.Status != store.StatusFailed {
		t.Fatalf("expected non-Gemini page to fail, got %s", res.Status)
	}
	if res.Category != store.ErrTargetOther {
		t.Errorf("expected ErrTargetOther for non-Gemini page, got %s", res.Category)
	}
}

// --- Retry-After parsing and classification tests ---

func TestParseRetryAfter(t *testing.T) {
	t.Run("valid integer seconds", func(t *testing.T) {
		d := gemini.ParseRetryAfter("120")
		if d == nil || *d != 120*time.Second {
			t.Fatalf("expected 120s, got %v", d)
		}
	})

	t.Run("integer with whitespace", func(t *testing.T) {
		d := gemini.ParseRetryAfter("   5 \t ")
		if d == nil || *d != 5*time.Second {
			t.Fatalf("expected 5s, got %v", d)
		}
	})

	t.Run("empty string", func(t *testing.T) {
		if d := gemini.ParseRetryAfter(""); d != nil {
			t.Fatalf("expected nil, got %v", d)
		}
		if d := gemini.ParseRetryAfter("   "); d != nil {
			t.Fatalf("expected nil for whitespace, got %v", d)
		}
	})

	t.Run("negative seconds", func(t *testing.T) {
		if d := gemini.ParseRetryAfter("-10"); d != nil {
			t.Fatalf("expected nil for negative duration, got %v", d)
		}
	})

	t.Run("invalid string", func(t *testing.T) {
		if d := gemini.ParseRetryAfter("invalid-seconds"); d != nil {
			t.Fatalf("expected nil, got %v", d)
		}
	})

	t.Run("HTTP-date in future", func(t *testing.T) {
		future := time.Now().Add(60 * time.Second).UTC()
		d := gemini.ParseRetryAfter(future.Format(http.TimeFormat))
		if d == nil {
			t.Fatal("expected non-nil for valid future HTTP-date")
		}
		if *d < 55*time.Second || *d > 65*time.Second {
			t.Fatalf("expected ~60s, got %v", *d)
		}
	})

	t.Run("HTTP-date in past", func(t *testing.T) {
		past := time.Now().Add(-60 * time.Second).UTC()
		d := gemini.ParseRetryAfter(past.Format(http.TimeFormat))
		if d == nil {
			t.Fatal("expected non-nil 0s for past HTTP-date")
		}
		if *d != 0 {
			t.Fatalf("expected 0s for past date, got %v", *d)
		}
	})
}

func TestClassifyResponse_RetryAfter(t *testing.T) {
	t.Run("429 with Retry-After header", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{"15"}},
		}
		res := gemini.ClassifyResponse(resp, nil, nil)
		if !res.Retryable {
			t.Error("expected 429 to be retryable")
		}
		if res.RetryAfter == nil || *res.RetryAfter != 15*time.Second {
			t.Fatalf("expected RetryAfter = 15s, got %v", res.RetryAfter)
		}
	})

	t.Run("503 with Retry-After header", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     http.Header{"Retry-After": []string{"30"}},
		}
		res := gemini.ClassifyResponse(resp, nil, nil)
		if !res.Retryable {
			t.Error("expected 503 to be retryable")
		}
		if res.RetryAfter == nil || *res.RetryAfter != 30*time.Second {
			t.Fatalf("expected RetryAfter = 30s, got %v", res.RetryAfter)
		}
	})

	t.Run("403 with Retry-After header ignored", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     http.Header{"Retry-After": []string{"30"}},
		}
		res := gemini.ClassifyResponse(resp, nil, nil)
		if res.Retryable {
			t.Error("expected 403 not to be retryable")
		}
		if res.RetryAfter != nil {
			t.Fatalf("expected nil RetryAfter on non-retryable 403, got %v", res.RetryAfter)
		}
	})

	t.Run("200 with Retry-After header ignored", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Server":      []string{"ESF"},
				"Retry-After": []string{"30"},
			},
		}
		body := []byte(`<!doctype html><html><head><title>Google Gemini</title></head><body><div id="bardchatui"></div></body></html>`)
		res := gemini.ClassifyResponse(resp, body, nil)
		if res.RetryAfter != nil {
			t.Fatalf("expected nil RetryAfter on 200 OK, got %v", res.RetryAfter)
		}
	})
}

// --- DialTimeout / TLSHandshakeTimeout regression tests ---

// TestProbe_DialTimeoutBoundsTLSHandshake verifies that gemini.Config.DialTimeout
// independently bounds the TLS handshake, NOT the broader application Timeout.
//
// Pre-Ticket-19, executeAttempt() set TLSHandshakeTimeout = cfg.DialTimeout.
// The Ticket 19 extraction must preserve this: a short DialTimeout should cause
// a TLS timeout even when the overall application Timeout is generous.
func TestProbe_DialTimeoutBoundsTLSHandshake(t *testing.T) {
	// Create a TCP listener that accepts connections but never completes TLS.
	// This simulates a server that stalls during the TLS handshake.
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
			// Hold the connection open without doing TLS handshake
			go func(c net.Conn) {
				defer c.Close()
				time.Sleep(5 * time.Second)
			}(conn)
		}
	}()

	start := time.Now()
	res := gemini.Probe(context.Background(), directDialer(), gemini.Config{
		URL:         "https://" + l.Addr().String() + "/",
		Timeout:     5 * time.Second,        // Generous overall timeout
		DialTimeout: 200 * time.Millisecond, // Tight TLS handshake timeout
	})
	elapsed := time.Since(start)

	// The probe must fail (TLS handshake never completes)
	if res.Status == store.StatusPassed {
		t.Fatal("expected probe to fail when TLS handshake stalls")
	}

	// The probe should fail within DialTimeout (~200ms), NOT wait for the
	// full application Timeout (5s). Allow generous margin for CI jitter.
	if elapsed > 2*time.Second {
		t.Fatalf("REGRESSION: DialTimeout not honored; elapsed=%v (expected <2s, DialTimeout=200ms, Timeout=5s)", elapsed)
	}

	// Verify the error is reported (so transport classification works)
	if res.Err == nil {
		t.Error("expected non-nil Err on TLS handshake stall")
	}
}

// TestProbe_DialTimeoutFallsBackToTimeout verifies that when DialTimeout is
// zero/unset, TLSHandshakeTimeout falls back to the application Timeout.
func TestProbe_DialTimeoutFallsBackToTimeout(t *testing.T) {
	// A quick server that responds immediately with a Gemini page over plain HTTP
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "ESF")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Google Gemini</title></head><body><div id="bardchatui"></div></body></html>`))
	}))
	defer srv.Close()

	// DialTimeout = 0 means "unset" — should fall back to Timeout
	res := gemini.Probe(context.Background(), directDialer(), gemini.Config{
		URL:         srv.URL,
		Timeout:     2 * time.Second,
		DialTimeout: 0, // unset
	})

	if res.Status != store.StatusPassed {
		t.Fatalf("expected success when DialTimeout is unset, got status=%s reason=%q", res.Status, res.Reason)
	}
}

// TestProbe_ClampLatency_ZeroFallback verifies that clampLatency enforces strictly positive
// latency (falling back to time.Nanosecond) when platform clock resolution evaluates to <= 0.
func TestProbe_ClampLatency_ZeroFallback(t *testing.T) {
	tests := []struct {
		name     string
		input    time.Duration
		expected time.Duration
	}{
		{"zero duration falls back to 1ns", 0, time.Nanosecond},
		{"negative duration falls back to 1ns", -5 * time.Millisecond, time.Nanosecond},
		{"1ns preserved", 1 * time.Nanosecond, 1 * time.Nanosecond},
		{"positive duration preserved", 25 * time.Millisecond, 25 * time.Millisecond},
		{"sub-millisecond positive duration preserved", 500 * time.Microsecond, 500 * time.Microsecond},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := gemini.ClampLatencyForTest(tc.input)
			if got != tc.expected {
				t.Errorf("ClampLatencyForTest(%v) = %v; want %v", tc.input, got, tc.expected)
			}
			if got <= 0 {
				t.Errorf("ClampLatencyForTest(%v) returned non-positive duration %v", tc.input, got)
			}
		})
	}
}

// --- Benchmark ---

func BenchmarkClassifyResponse_1MB(b *testing.B) {
	snippet := "<div class=\"content\">Gemini isn\u2019t supported &amp; &#8217;available&#8217; in region \u201cXYZ\u201d.\n\t  Lots   of    spaces   and\tnewlines.\n</div>\n"
	var sb strings.Builder
	sb.WriteString("<!doctype html><html><head><title>Google Gemini</title></head><body><div id=\"bardchatui\">")
	for sb.Len() < 1<<20 {
		sb.WriteString(snippet)
	}
	sb.WriteString("</div></body></html>")
	payload := []byte(sb.String())

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Server": []string{"ESF"}},
	}
	blockPhrases := []string{"isn't currently supported in your country"}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = gemini.ClassifyResponse(resp, payload, blockPhrases)
	}
}
