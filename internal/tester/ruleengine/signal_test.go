package ruleengine_test

import (
	"net/http"
	"net/url"
	"testing"

	"gemsub/internal/tester/ruleengine"
)

func TestSignalFinalURLPath_QueryStringOnly(t *testing.T) {
	// Exact URL from claude/classifier_test.go TestClassifyResponse_SlugInQueryStringOnly
	targetURL, err := url.Parse("https://claude.ai/faq?ref=app-unavailable-in-region")
	if err != nil {
		t.Fatalf("failed to parse test URL: %v", err)
	}

	ctx := &ruleengine.EvaluationContext{
		StatusCode: http.StatusOK,
		FinalURL:   targetURL,
	}

	sig := ruleengine.FinalURLPathSignal()
	extracted := sig.Extract(ctx)

	pathStr, ok := extracted.(string)
	if !ok {
		t.Fatalf("expected string from FinalURLPathSignal, got %T", extracted)
	}

	if pathStr != "/faq" {
		t.Fatalf("expected signal value '/faq', got %q", pathStr)
	}

	// Must not match regional rejection slug
	matcher := ruleengine.ContainsAny("app-unavailable-in-region")
	if matcher.Match(pathStr) {
		t.Fatalf("expected path signal not to match slug 'app-unavailable-in-region', but it matched")
	}

	// Equals matcher matches "/faq"
	if !ruleengine.Equals("/faq").Match(pathStr) {
		t.Fatalf("expected Equals('/faq') to match path signal")
	}
}

func TestSignalFinalURLPath_SlugInPath(t *testing.T) {
	targetURL, err := url.Parse("https://claude.ai/app-unavailable-in-region")
	if err != nil {
		t.Fatalf("failed to parse test URL: %v", err)
	}

	ctx := &ruleengine.EvaluationContext{
		StatusCode: http.StatusOK,
		FinalURL:   targetURL,
	}

	sig := ruleengine.FinalURLPathSignal()
	pathStr := sig.Extract(ctx).(string)

	if pathStr != "/app-unavailable-in-region" {
		t.Fatalf("expected '/app-unavailable-in-region', got %q", pathStr)
	}

	matcher := ruleengine.ContainsAny("app-unavailable-in-region")
	if !matcher.Match(pathStr) {
		t.Fatalf("expected path signal to match slug in path")
	}
}

func TestSignalCanonicalLink(t *testing.T) {
	ctx := &ruleengine.EvaluationContext{
		CanonicalLink:      "https://claude.ai/app-unavailable-in-region",
		CanonicalLinkFound: true,
	}

	sig := ruleengine.CanonicalLinkSignal()
	extracted := sig.Extract(ctx)

	linkStr, ok := extracted.(string)
	if !ok {
		t.Fatalf("expected string, got %T", extracted)
	}

	if linkStr != "https://claude.ai/app-unavailable-in-region" {
		t.Fatalf("expected 'https://claude.ai/app-unavailable-in-region', got %q", linkStr)
	}

	matcher := ruleengine.ContainsAny("app-unavailable-in-region")
	if !matcher.Match(linkStr) {
		t.Fatalf("expected canonical link to match regional block slug")
	}
}

func TestSignalCookieDomain(t *testing.T) {
	ctx := &ruleengine.EvaluationContext{
		Cookies: []*http.Cookie{
			{Name: "session", Domain: "EXAMPLE.COM"},
			{Name: "pref", Domain: "Gemini.Google.Com"},
		},
	}

	sig := ruleengine.CookieDomainSignal()
	extracted := sig.Extract(ctx)

	domains, ok := extracted.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", extracted)
	}

	if len(domains) != 2 || domains[0] != "example.com" || domains[1] != "gemini.google.com" {
		t.Fatalf("expected normalized lower-case domains, got %v", domains)
	}

	matcher := ruleengine.ContainsAny("google.com")
	if !matcher.Match(domains) {
		t.Fatalf("expected ContainsAny('google.com') to match domain slice")
	}
}

func TestSignalHeader(t *testing.T) {
	ctx := &ruleengine.EvaluationContext{
		Headers: http.Header{
			"Server":       []string{"GWS/2.1"},
			"Content-Type": []string{"text/html; charset=utf-8"},
		},
	}

	sig := ruleengine.HeaderSignal("Server")
	val := sig.Extract(ctx)

	if val != "gws/2.1" {
		t.Fatalf("expected 'gws/2.1', got %v", val)
	}

	// Case-insensitive header name lookup via MIME
	sigLower := ruleengine.HeaderSignal("server")
	valLower := sigLower.Extract(ctx)
	if valLower != "gws/2.1" {
		t.Fatalf("expected 'gws/2.1' via lower-case header key lookup, got %v", valLower)
	}
}

func TestSignalHTTPStatus(t *testing.T) {
	ctx := &ruleengine.EvaluationContext{
		StatusCode: 200,
	}

	sig := ruleengine.HTTPStatusSignal()
	val := sig.Extract(ctx)

	if val != 200 {
		t.Fatalf("expected 200, got %v", val)
	}
}
