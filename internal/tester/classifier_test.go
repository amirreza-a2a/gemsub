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
	res429 := tester.ClassifyDialError(errors.New("request failed: unexpected HTTP response status: 429"))
	if res429.Category != store.ErrProxyRateLimited || !res429.Retryable || res429.Status != store.StatusInconclusive {
		t.Errorf("expected retryable proxy rate limited, got %+v", res429)
	}

	res503 := tester.ClassifyDialError(errors.New("request failed: unexpected HTTP response status: 503"))
	if res503.Category != store.ErrProxyError || !res503.Retryable || res503.Status != store.StatusInconclusive {
		t.Errorf("expected retryable proxy 503 error, got %+v", res503)
	}

	res403 := tester.ClassifyDialError(errors.New("request failed: unexpected HTTP response status: 403"))
	if res403.Category != store.ErrProxyError || res403.Retryable || res403.Status != store.StatusFailed {
		t.Errorf("expected non-retryable proxy 403 error, got %+v", res403)
	}

	resRefused := tester.ClassifyDialError(errors.New("dial tcp: connection refused"))
	if resRefused.Category != store.ErrConnRefused || resRefused.Retryable || resRefused.Status != store.StatusFailed {
		t.Errorf("expected non-retryable conn refused, got %+v", resRefused)
	}

	resTimeout := tester.ClassifyDialError(errors.New("dial tcp: i/o timeout"))
	if resTimeout.Category != store.ErrTimeout || resTimeout.Retryable || resTimeout.Status != store.StatusFailed {
		t.Errorf("expected non-retryable timeout, got %+v", resTimeout)
	}

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

func TestClassifyResponse_WIZGlobalData_GeoBlocking(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Server": []string{"ESF"}},
	}

	// RU restriction test case
	bodyBlockedRU := []byte(`<!doctype html><html><head><title>Google Gemini</title>
<script>window.WIZ_global_data = {"vXmutd":"%.@.\"RU\",\"ZZ\",\"2u255g\\u003d\\u003d\"]"};</script>
</head><body><div id="bardchatui"></div></body></html>`)

	resRU := tester.ClassifyResponse(resp, bodyBlockedRU, nil)
	if resRU.Status != store.StatusFailed || resRU.Category != store.ErrRegionBlocked {
		t.Errorf("expected blocked country RU to fail with ErrRegionBlocked, got status=%s category=%s",
			resRU.Status, resRU.Category)
	}

	// IR restriction test case
	bodyBlockedIR := []byte(`<!doctype html><html><head><title>Google Gemini</title>
<script>window.WIZ_global_data = {"vXmutd":"%.@.\"IR\",\"ZZ\",\"Kgm6wTkAEZgAAAAAA9EAUA\\u003d\\u003d\"]"};</script>
</head><body><div id="bardchatui"></div></body></html>`)

	resIR := tester.ClassifyResponse(resp, bodyBlockedIR, nil)
	if resIR.Status != store.StatusFailed || resIR.Category != store.ErrRegionBlocked {
		t.Errorf("expected blocked country IR to fail with ErrRegionBlocked, got status=%s category=%s",
			resIR.Status, resIR.Category)
	}

	// Explicit restriction flag test case
	bodyBlockedFlag := []byte(`<!doctype html><html><head><title>Google Gemini</title>
<script>window.WIZ_global_data = {"status":"LOCATION_REJECTED"};</script>
</head><body><div id="bardchatui"></div></body></html>`)

	resFlag := tester.ClassifyResponse(resp, bodyBlockedFlag, nil)
	if resFlag.Status != store.StatusFailed || resFlag.Category != store.ErrRegionBlocked {
		t.Errorf("expected LOCATION_REJECTED to fail with ErrRegionBlocked, got status=%s category=%s",
			resFlag.Status, resFlag.Category)
	}

	// Allowed country test case (US)
	bodyCleanUS := []byte(`<!doctype html><html><head><title>Google Gemini</title>
<script>window.WIZ_global_data = {"vXmutd":"%.@.\"US\",\"ZZ\",\"2u255g\\u003d\\u003d\"]"};</script>
</head><body><div id="bardchatui"></div></body></html>`)

	resUS := tester.ClassifyResponse(resp, bodyCleanUS, nil)
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

	res := tester.ClassifyResponse(resp, body, blockPhrases)
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
	body := []byte("<!doctype html><html><head><title>Google Gemini</title></head>\n<body><p>Gemini isn\u2019t currently supported in your country. Stay tuned!</p></body></html>")

	res := tester.ClassifyResponse(resp, body, blockPhrases)
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

	res := tester.ClassifyResponse(resp, body, blockPhrases)
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

	res := tester.ClassifyResponse(resp, body, blockPhrases)
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

	res := tester.ClassifyResponse(resp, body, blockPhrases)
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

	res := tester.ClassifyResponse(resp, body, blockPhrases)
	if res.Status != store.StatusFailed {
		t.Errorf("expected title-only 'Gemini' without markers to FAIL; got status=%s reason=%q",
			res.Status, res.Reason)
	}
}

func TestRateLimiter_BurstControlled(t *testing.T) {
	const rateLimitBurst = 2
	limiter := rate.NewLimiter(rate.Limit(0.001), rateLimitBurst)

	if !limiter.Allow() {
		t.Errorf("expected 1st token to be allowed")
	}
	if !limiter.Allow() {
		t.Errorf("expected 2nd token to be allowed")
	}
	if limiter.Allow() {
		t.Errorf("expected 3rd token to be rejected by burst limit of %d", rateLimitBurst)
	}
	if limiter.Allow() {
		t.Errorf("expected 4th token to be rejected by burst limit of %d", rateLimitBurst)
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

	res := tester.ClassifyResponse(resp, bodyWithAdminLink, nil)
	if res.Status != store.StatusPassed {
		t.Errorf("expected real Gemini page with admin fallback URL to pass, got status=%s category=%s reason=%q",
			res.Status, res.Category, res.Reason)
	}
}
