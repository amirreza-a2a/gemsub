package tester_test

import (
	"errors"
	"net/http"
	"testing"

	"golang.org/x/time/rate"

	"gemsub/internal/store"
	"gemsub/internal/tester"
)

func TestNormalizeText(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{
			input:    "Gemini isn\u2019t currently supported in your country.",
			expected: "gemini isn't currently supported in your country.",
		},
		{
			input:    "Gemini isn&rsquo;t currently supported in your country.",
			expected: "gemini isn't currently supported in your country.",
		},
		{
			input:    "Gemini&#8217;s   new   \t\n  feature",
			expected: "gemini's new feature",
		},
		{
			input:    "\u201cNot available in your region\u201d",
			expected: "\"not available in your region\"",
		},
	}

	for _, c := range cases {
		got := tester.NormalizeText(c.input)
		if got != c.expected {
			t.Errorf("NormalizeText(%q) = %q; want %q", c.input, got, c.expected)
		}
	}
}

func TestClassifyDialError(t *testing.T) {
	// Proxy/tunnel 429
	res429 := tester.ClassifyDialError(errors.New("request failed: unexpected HTTP response status: 429"))
	if res429.Category != store.ErrProxyRateLimited || !res429.Retryable || res429.Status != store.StatusInconclusive {
		t.Errorf("expected retryable proxy rate limited, got %+v", res429)
	}

	// Proxy/tunnel 503
	res503 := tester.ClassifyDialError(errors.New("request failed: unexpected HTTP response status: 503"))
	if res503.Category != store.ErrProxyError || !res503.Retryable || res503.Status != store.StatusInconclusive {
		t.Errorf("expected retryable proxy 503 error, got %+v", res503)
	}

	// Proxy 403
	res403 := tester.ClassifyDialError(errors.New("request failed: unexpected HTTP response status: 403"))
	if res403.Category != store.ErrProxyError || res403.Retryable || res403.Status != store.StatusFailed {
		t.Errorf("expected non-retryable proxy 403 error, got %+v", res403)
	}

	// Connection refused
	resRefused := tester.ClassifyDialError(errors.New("dial tcp: connection refused"))
	if resRefused.Category != store.ErrConnRefused || resRefused.Retryable || resRefused.Status != store.StatusFailed {
		t.Errorf("expected non-retryable conn refused, got %+v", resRefused)
	}

	// Timeout
	resTimeout := tester.ClassifyDialError(errors.New("dial tcp: i/o timeout"))
	if resTimeout.Category != store.ErrTimeout || resTimeout.Retryable || resTimeout.Status != store.StatusFailed {
		t.Errorf("expected non-retryable timeout, got %+v", resTimeout)
	}

	// Reality failure
	resReality := tester.ClassifyDialError(errors.New("reality verification failed"))
	if resReality.Category != store.ErrReality || resReality.Retryable || resReality.Status != store.StatusFailed {
		t.Errorf("expected non-retryable reality error, got %+v", resReality)
	}
}

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
			res := tester.ClassifyResponse(resp, nil, blockPhrases)
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

// --- Gemini Positive Validation Tests ---

func TestClassifyResponse_RealisticGeminiLanding_Passes(t *testing.T) {
	blockPhrases := []string{
		"isn\u2019t currently supported in your country",
		"not available in your country",
		"not available in your region",
	}

	// Realistic Gemini landing page: ESF server header + title + body brand signals
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

	res := tester.ClassifyResponse(resp, body, blockPhrases)
	if res.Status != store.StatusPassed {
		t.Errorf("expected realistic Gemini page to pass; got status=%s category=%s reason=%q",
			res.Status, res.Category, res.Reason)
	}
}

func TestClassifyResponse_GeminiWithGoogleCookieNoBrandInTitle_Passes(t *testing.T) {
	blockPhrases := []string{"not available in your country"}

	// Google server with cookie, brand in body but not in title
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Server":     []string{"GSE"},
			"Set-Cookie": []string{"NID=123; domain=.google.com; path=/; HttpOnly"},
		},
	}
	body := []byte(`<!doctype html><html><head><title>Some App</title></head>
<body><p>Welcome to Gemini</p></body></html>`)

	res := tester.ClassifyResponse(resp, body, blockPhrases)
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
	// The exact regional block message with Unicode right single quotation mark (U+2019)
	body := []byte("<!doctype html><html><head><title>Google Gemini</title></head>\n<body><p>Gemini isn\u2019t currently supported in your country. Stay tuned!</p></body></html>")

	res := tester.ClassifyResponse(resp, body, blockPhrases)
	if res.Status != store.StatusFailed || res.Category != store.ErrRegionBlocked {
		t.Errorf("expected regional block to fail with ErrRegionBlocked; got status=%s category=%s",
			res.Status, res.Category)
	}
}

func TestClassifyResponse_FalsePositive_HoroscopeSite_Fails(t *testing.T) {
	blockPhrases := []string{"not available in your country"}

	// Non-Google server, title contains "Gemini", body mentions "gemini" — should NOT pass
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Server": []string{"nginx/1.24.0"}},
	}
	body := []byte(`<html><head><title>Gemini Daily Horoscope</title></head>
<body><p>Your sign today is Gemini. The stars align.</p></body></html>`)

	res := tester.ClassifyResponse(resp, body, blockPhrases)
	if res.Status != store.StatusFailed {
		t.Errorf("expected false-positive horoscope page to FAIL; got status=%s category=%s reason=%q",
			res.Status, res.Category, res.Reason)
	}
}

func TestClassifyResponse_FalsePositive_CaptivePortal_Fails(t *testing.T) {
	blockPhrases := []string{"not available in your country"}

	// Captive portal with RouterOS and mentioning "Gemini Cafe"
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Server": []string{"RouterOS"}},
	}
	body := []byte(`<html><head><title>RouterOS Login</title></head>
<body><p>Welcome to Gemini Cafe WiFi. Please login.</p></body></html>`)

	res := tester.ClassifyResponse(resp, body, blockPhrases)
	if res.Status != store.StatusFailed {
		t.Errorf("expected captive portal to FAIL; got status=%s category=%s reason=%q",
			res.Status, res.Category, res.Reason)
	}
}

func TestClassifyResponse_StrippedHeaders_WithAppMarkers_Passes(t *testing.T) {
	blockPhrases := []string{"not available in your country"}

	// Proxy stripped Google headers, but body has title + distinctive app markers
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Server": []string{"nginx"}},
	}
	body := []byte(`<html><head><title>Google Gemini</title></head>
<body><div id="bardchatui">data</div></body></html>`)

	res := tester.ClassifyResponse(resp, body, blockPhrases)
	if res.Status != store.StatusPassed {
		t.Errorf("expected Gemini with stripped headers + app markers to pass; got status=%s reason=%q",
			res.Status, res.Reason)
	}
}

func TestClassifyResponse_TitleGeminiAlone_NoAppMarkers_Fails(t *testing.T) {
	blockPhrases := []string{"not available in your country"}

	// Title says "Gemini" but no Google origin and no app markers — should fail
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Server": []string{"Apache/2.4"}},
	}
	body := []byte(`<html><head><title>Gemini</title></head>
<body><p>Welcome to our site about the Gemini constellation.</p></body></html>`)

	res := tester.ClassifyResponse(resp, body, blockPhrases)
	if res.Status != store.StatusFailed {
		t.Errorf("expected title-only 'Gemini' without markers to FAIL; got status=%s reason=%q",
			res.Status, res.Reason)
	}
}

// --- Rate Limiting Burst Test ---

func TestRateLimiter_BurstControlled(t *testing.T) {
	// Deterministically verify that with burst=2 and a low refill rate (e.g. 1 token per 1000s),
	// exactly 2 tokens can be consumed immediately, and the 3rd attempt is rejected.
	const rateLimitBurst = 2
	limiter := rate.NewLimiter(rate.Limit(0.001), rateLimitBurst)

	// Consume first token (should succeed)
	if !limiter.Allow() {
		t.Errorf("expected 1st token to be allowed")
	}

	// Consume second token (should succeed)
	if !limiter.Allow() {
		t.Errorf("expected 2nd token to be allowed")
	}

	// Third attempt within burst window (must fail)
	if limiter.Allow() {
		t.Errorf("expected 3rd token to be rejected by burst limit of %d", rateLimitBurst)
	}

	// Fourth attempt (must fail)
	if limiter.Allow() {
		t.Errorf("expected 4th token to be rejected by burst limit of %d", rateLimitBurst)
	}
}
