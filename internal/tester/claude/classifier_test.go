package claude_test

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gemsub/internal/store"
	"gemsub/internal/tester/claude"
)

func parseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

func TestClassifyResponse_Nil(t *testing.T) {
	res := claude.ClassifyResponse(nil, nil)
	if res.Status != store.StatusFailed {
		t.Errorf("expected StatusFailed for nil response, got %v", res.Status)
	}
	if res.Category != store.ErrTargetOther {
		t.Errorf("expected ErrTargetOther for nil response, got %v", res.Category)
	}
	if res.Reason != "nil response" {
		t.Errorf("expected 'nil response', got %q", res.Reason)
	}
}

func TestClassifyResponse_HTTP429(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"60"}},
	}
	res := claude.ClassifyResponse(resp, nil)

	if res.Status != store.StatusInconclusive {
		t.Errorf("expected StatusInconclusive, got %v", res.Status)
	}
	if res.Category != store.ErrTargetRateLimited {
		t.Errorf("expected ErrTargetRateLimited, got %v", res.Category)
	}
	if !res.Retryable {
		t.Errorf("expected Retryable to be true")
	}
	if res.RetryAfter == nil || *res.RetryAfter != 60*time.Second {
		t.Errorf("expected RetryAfter 60s, got %v", res.RetryAfter)
	}
}

func TestClassifyResponse_HTTP5xx(t *testing.T) {
	codes := []int{
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
		599,
	}

	for _, code := range codes {
		resp := &http.Response{StatusCode: code}
		res := claude.ClassifyResponse(resp, nil)
		if res.Status != store.StatusInconclusive {
			t.Errorf("code %d: expected StatusInconclusive, got %v", code, res.Status)
		}
		if res.Category != store.ErrTargetError {
			t.Errorf("code %d: expected ErrTargetError, got %v", code, res.Category)
		}
	}
}

func TestClassifyResponse_HTTP403(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
	}
	res := claude.ClassifyResponse(resp, nil)
	if res.Status != store.StatusFailed {
		t.Errorf("expected StatusFailed, got %v", res.Status)
	}
	if res.Category != store.ErrTargetDenied {
		t.Errorf("expected ErrTargetDenied, got %v", res.Category)
	}
	if res.Reason != "target HTTP 403 Forbidden" {
		t.Errorf("expected 'target HTTP 403 Forbidden', got %q", res.Reason)
	}
}

func TestClassifyResponse_CloudflarePrecedence(t *testing.T) {
	challenges := []string{
		"Attention Required! | Cloudflare",
		"cf-chl-bypass",
		"Just a moment...",
	}

	for _, chal := range challenges {
		// Even if the URL looks like region block, Cloudflare challenge MUST take precedence
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Request: &http.Request{
				URL: parseURL("https://claude.com/app-unavailable-in-region"),
			},
		}
		res := claude.ClassifyResponse(resp, []byte("<html><body>"+chal+"</body></html>"))
		if res.Status != store.StatusFailed {
			t.Errorf("chal %q: expected StatusFailed, got %v", chal, res.Status)
		}
		if res.Category != store.ErrTargetOther {
			t.Errorf("chal %q: expected ErrTargetOther (precedence over region block), got %v", chal, res.Category)
		}
		if res.Reason != "interception: Cloudflare challenge or captive portal page" {
			t.Errorf("chal %q: unexpected reason %q", chal, res.Reason)
		}
	}
}

func TestClassifyResponse_PrimaryRegionBlock(t *testing.T) {
	testCases := []struct {
		name string
		url  string
	}{
		{
			name: "root region block",
			url:  "https://claude.com/app-unavailable-in-region",
		},
		{
			name: "german localized",
			url:  "https://claude.com/de/app-unavailable-in-region",
		},
		{
			name: "french localized",
			url:  "https://claude.com/fr/app-unavailable-in-region",
		},
		{
			name: "japanese localized",
			url:  "https://claude.com/ja/app-unavailable-in-region",
		},
		{
			name: "uppercase path",
			url:  "https://claude.com/APP-UNAVAILABLE-IN-REGION",
		},
		{
			name: "with query parameters",
			url:  "https://claude.com/app-unavailable-in-region?utm_source=test&country=ir",
		},
		{
			name: "localized with query",
			url:  "https://claude.com/ja/app-unavailable-in-region?lang=ja",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Request: &http.Request{
					URL: parseURL(tc.url),
				},
			}
			res := claude.ClassifyResponse(resp, []byte("<html><body>Some generic body</body></html>"))
			if res.Status != store.StatusFailed {
				t.Errorf("expected StatusFailed, got %v", res.Status)
			}
			if res.Category != store.ErrRegionBlocked {
				t.Errorf("expected ErrRegionBlocked, got %v", res.Category)
			}
			if res.Reason != "claude region restriction: redirected to app-unavailable-in-region" {
				t.Errorf("unexpected reason %q", res.Reason)
			}
		})
	}
}

func TestClassifyResponse_SecondaryRegionBlockFallbacks(t *testing.T) {
	testCases := []struct {
		name string
		body string
	}{
		{
			name: "canonical link with href first",
			body: `<!DOCTYPE html><html><head><link href="https://claude.com/app-unavailable-in-region" rel="canonical"/></head></html>`,
		},
		{
			name: "canonical link with rel first",
			body: `<!DOCTYPE html><html><head><link rel="canonical" href="https://claude.com/de/app-unavailable-in-region"></head></html>`,
		},
		{
			name: "canonical link uppercase",
			body: `<!DOCTYPE html><html><head><LINK REL="CANONICAL" HREF="https://claude.com/APP-UNAVAILABLE-IN-REGION"></head></html>`,
		},
		{
			name: "title marker english",
			body: `<html><head><title>App unavailable in region | Claude by Anthropic</title></head></html>`,
		},
		{
			name: "title marker uppercase",
			body: `<html><head><TITLE>APP UNAVAILABLE IN REGION</TITLE></head></html>`,
		},
		{
			name: "anthropic supported countries link with corroborating unavailable text",
			body: `<html><body><p>Claude is only available in certain regions right now.</p><a href="https://www.anthropic.com/supported-countries">View supported countries</a></body></html>`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Request is nil, simulating no redirect info captured
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Request:    nil,
			}
			res := claude.ClassifyResponse(resp, []byte(tc.body))
			if res.Status != store.StatusFailed {
				t.Errorf("expected StatusFailed, got %v", res.Status)
			}
			if res.Category != store.ErrRegionBlocked {
				t.Errorf("expected ErrRegionBlocked, got %v", res.Category)
			}
			if res.Reason != "claude region restriction: marker detected in payload" {
				t.Errorf("unexpected reason %q", res.Reason)
			}
		})
	}
}

func TestClassifyResponse_NilRequestOrURL(t *testing.T) {
	// resp.Request == nil
	respNilReq := &http.Response{
		StatusCode: http.StatusOK,
		Request:    nil,
	}
	res1 := claude.ClassifyResponse(respNilReq, []byte("<html><body>clean page</body></html>"))
	if res1.Status != store.StatusPassed || res1.Category != store.ErrNone {
		t.Errorf("expected StatusPassed/ErrNone when clean and Request is nil, got %v/%v", res1.Status, res1.Category)
	}

	// resp.Request.URL == nil
	respNilURL := &http.Response{
		StatusCode: http.StatusOK,
		Request:    &http.Request{URL: nil},
	}
	res2 := claude.ClassifyResponse(respNilURL, []byte("<html><body>clean page</body></html>"))
	if res2.Status != store.StatusPassed || res2.Category != store.ErrNone {
		t.Errorf("expected StatusPassed/ErrNone when clean and URL is nil, got %v/%v", res2.Status, res2.Category)
	}
}

func TestClassifyResponse_RealFixture(t *testing.T) {
	// Look for response.html fixture
	paths := []string{
		"../../../examples/stage2_inspector/response.html",
		"../../examples/stage2_inspector/response.html",
		"../examples/stage2_inspector/response.html",
	}

	var data []byte
	var err error
	for _, p := range paths {
		data, err = os.ReadFile(filepath.Clean(p))
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Skipf("skipping test: real fixture response.html not present (gitignored): %v", err)
	}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Request: &http.Request{
			URL: parseURL("https://claude.com/app-unavailable-in-region"),
		},
	}

	res := claude.ClassifyResponse(resp, data)
	if res.Status != store.StatusFailed {
		t.Errorf("expected StatusFailed for real fixture, got %v", res.Status)
	}
	if res.Category != store.ErrRegionBlocked {
		t.Errorf("expected ErrRegionBlocked for real fixture, got %v", res.Category)
	}

	// Also test the real fixture WITHOUT Request (verifying secondary fallback signals work against real HTML)
	respNoReq := &http.Response{
		StatusCode: http.StatusOK,
		Request:    nil,
	}
	resFallback := claude.ClassifyResponse(respNoReq, data)
	if resFallback.Status != store.StatusFailed {
		t.Errorf("expected StatusFailed for real fixture fallback, got %v", resFallback.Status)
	}
	if resFallback.Category != store.ErrRegionBlocked {
		t.Errorf("expected ErrRegionBlocked for real fixture fallback, got %v", resFallback.Category)
	}
}

func TestClassifyResponse_Generic200OK(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Request: &http.Request{
			URL: parseURL("https://claude.ai/"),
		},
	}
	res := claude.ClassifyResponse(resp, []byte("<html><body>Normal Claude interactive page</body></html>"))
	if res.Status != store.StatusPassed {
		t.Errorf("expected StatusPassed, got %v", res.Status)
	}
	if res.Category != store.ErrNone {
		t.Errorf("expected ErrNone, got %v", res.Category)
	}
	if res.Reason != "ok" {
		t.Errorf("expected reason 'ok', got %q", res.Reason)
	}
}

func TestClassifyResponse_UnexpectedStatus(t *testing.T) {
	statuses := []int{http.StatusNotFound, http.StatusMovedPermanently, http.StatusBadRequest}
	for _, status := range statuses {
		resp := &http.Response{
			StatusCode: status,
			Request: &http.Request{
				URL: parseURL("https://claude.ai/somepath"),
			},
		}
		res := claude.ClassifyResponse(resp, nil)
		if res.Status != store.StatusFailed {
			t.Errorf("status %d: expected StatusFailed, got %v", status, res.Status)
		}
		if res.Category != store.ErrTargetOther {
			t.Errorf("status %d: expected ErrTargetOther, got %v", status, res.Category)
		}
	}
}

func TestClassifyResponse_SlugInQueryStringOnly(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Request: &http.Request{
			URL: parseURL("https://claude.ai/faq?ref=app-unavailable-in-region"),
		},
	}
	res := claude.ClassifyResponse(resp, []byte("<html><body><h1>Claude FAQ</h1><p>Welcome to the FAQ.</p></body></html>"))
	if res.Status != store.StatusPassed {
		t.Errorf("expected StatusPassed for slug in query string only, got %v", res.Status)
	}
	if res.Category != store.ErrNone {
		t.Errorf("expected ErrNone for slug in query string only, got %v", res.Category)
	}
	if res.Reason != "ok" {
		t.Errorf("expected reason 'ok', got %q", res.Reason)
	}
}

func TestClassifyResponse_OrdinaryPageWithSupportedCountriesLink(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Request: &http.Request{
			URL: parseURL("https://claude.ai/pricing"),
		},
	}
	body := []byte(`<html><body><h1>Pricing FAQ</h1><p>Claude Pro is available for purchase. For a list of countries, visit <a href="https://www.anthropic.com/supported-countries">supported countries</a>.</p></body></html>`)
	res := claude.ClassifyResponse(resp, body)
	if res.Status != store.StatusPassed {
		t.Errorf("expected StatusPassed for ordinary page with supported-countries link, got %v", res.Status)
	}
	if res.Category != store.ErrNone {
		t.Errorf("expected ErrNone for ordinary page with supported-countries link, got %v", res.Category)
	}
	if res.Reason != "ok" {
		t.Errorf("expected reason 'ok', got %q", res.Reason)
	}
}
