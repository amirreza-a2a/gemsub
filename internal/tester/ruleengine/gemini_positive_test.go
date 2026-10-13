package ruleengine_test

import (
	"net/http"
	"strings"
	"testing"

	"gemsub/internal/tester/ruleengine"
)

// geminiReferencePositiveProof directly replicates the procedural logic of
// gemini/probe.go:259-284 for exact parity verification.
func geminiReferencePositiveProof(ctx *ruleengine.EvaluationContext) bool {
	serverHeader := ""
	if ctx != nil && ctx.Headers != nil {
		serverHeader = ctx.Headers.Get("Server")
	}
	isGoogleServer := strings.Contains(strings.ToUpper(serverHeader), "ESF") ||
		strings.Contains(strings.ToUpper(serverHeader), "GSE") ||
		strings.Contains(strings.ToUpper(serverHeader), "SFFE") ||
		strings.Contains(strings.ToUpper(serverHeader), "GWS")

	hasGoogleCookie := false
	if ctx != nil {
		for _, cookie := range ctx.Cookies {
			if cookie != nil && (strings.Contains(cookie.Domain, "google.com") || strings.Contains(cookie.Domain, "gemini.google.com")) {
				hasGoogleCookie = true
				break
			}
		}
	}
	originSignal := isGoogleServer || hasGoogleCookie

	titleSignal := false
	if ctx != nil && strings.Contains(strings.ToLower(ctx.Title), "gemini") {
		titleSignal = true
	}

	brandSignal := false
	if ctx != nil && strings.Contains(ctx.NormalizedBody, "gemini") {
		brandSignal = true
	}

	appMarkerSignal := false
	if ctx != nil {
		norm := ctx.NormalizedBody
		appMarkerSignal = strings.Contains(norm, "bardchatui") ||
			strings.Contains(norm, "wiz_global_data") ||
			strings.Contains(norm, "google gemini")
	}

	if originSignal && (titleSignal || brandSignal) {
		return true
	} else if titleSignal && appMarkerSignal {
		return true
	}
	return false
}

// buildGeminiPositiveVerificationCondition constructs Gemini's exact positive verification
// rule expression using Condition combinators:
// ((Server header in {ESF,GSE,SFFE,GWS} || cookie domain contains "google.com") &&
//
//	(title contains "gemini" || normalized body contains "gemini")) ||
//
// (title contains "gemini" && body contains any of {"bardchatui", "wiz_global_data", "google gemini"})
func buildGeminiPositiveVerificationCondition() ruleengine.Condition {
	originSignal := ruleengine.AnyOf(
		ruleengine.NewLeafCondition(
			ruleengine.HeaderSignal("Server"),
			ruleengine.ContainsAny("ESF", "GSE", "SFFE", "GWS"),
		),
		ruleengine.NewLeafCondition(
			ruleengine.CookieDomainSignal(),
			ruleengine.ContainsAny("google.com"),
		),
	)

	brandSignal := ruleengine.AnyOf(
		ruleengine.NewLeafCondition(
			ruleengine.TitleSignal(),
			ruleengine.ContainsAny("gemini"),
		),
		ruleengine.NewLeafCondition(
			ruleengine.NormalizedBodySignal(),
			ruleengine.ContainsAny("gemini"),
		),
	)

	titleSignal := ruleengine.NewLeafCondition(
		ruleengine.TitleSignal(),
		ruleengine.ContainsAny("gemini"),
	)

	appMarkerSignal := ruleengine.NewLeafCondition(
		ruleengine.NormalizedBodySignal(),
		ruleengine.ContainsAny("bardchatui", "wiz_global_data", "google gemini"),
	)

	return ruleengine.AnyOf(
		ruleengine.AllOf(originSignal, brandSignal),
		ruleengine.AllOf(titleSignal, appMarkerSignal),
	)
}

func TestGeminiPositiveVerification_AllBranches(t *testing.T) {
	cond := buildGeminiPositiveVerificationCondition()

	tests := []struct {
		name     string
		ctx      *ruleengine.EvaluationContext
		expected bool
	}{
		{
			name: "Branch 1a: Origin via Server header (ESF) + Brand via Title",
			ctx: &ruleengine.EvaluationContext{
				Headers:        http.Header{"Server": []string{"ESF"}},
				Title:          "Gemini - Google",
				TitleFound:     true,
				NormalizedBody: "welcome to our service",
			},
			expected: true,
		},
		{
			name: "Branch 1b: Origin via Server header (gws/2.1 case-insensitive) + Brand via Normalized Body",
			ctx: &ruleengine.EvaluationContext{
				Headers:        http.Header{"Server": []string{"gws/2.1"}},
				Title:          "Sign in",
				TitleFound:     true,
				NormalizedBody: "chat with google gemini today",
			},
			expected: true,
		},
		{
			name: "Branch 1c: Origin via Cookie Domain + Brand via Normalized Body",
			ctx: &ruleengine.EvaluationContext{
				Cookies: []*http.Cookie{
					{Name: "SID", Domain: ".google.com"},
				},
				Title:          "Welcome",
				TitleFound:     true,
				NormalizedBody: "powered by gemini ai",
			},
			expected: true,
		},
		{
			name: "Branch 1d: Origin via sub-cookie domain (gemini.google.com) + Brand via Title",
			ctx: &ruleengine.EvaluationContext{
				Cookies: []*http.Cookie{
					{Name: "HSID", Domain: "gemini.google.com"},
				},
				Title:          "Google Gemini",
				TitleFound:     true,
				NormalizedBody: "hello",
			},
			expected: true,
		},
		{
			name: "Branch 2a: No origin + Title contains gemini + AppMarker (bardchatui)",
			ctx: &ruleengine.EvaluationContext{
				Title:          "Gemini",
				TitleFound:     true,
				NormalizedBody: "var app = window.bardchatui || {}",
			},
			expected: true,
		},
		{
			name: "Branch 2b: No origin + Title contains gemini + AppMarker (wiz_global_data)",
			ctx: &ruleengine.EvaluationContext{
				Title:          "Gemini App",
				TitleFound:     true,
				NormalizedBody: "window.wiz_global_data = {key: 123}",
			},
			expected: true,
		},
		{
			name: "Branch 2c: No origin + Title contains gemini + AppMarker (google gemini)",
			ctx: &ruleengine.EvaluationContext{
				Title:          "Gemini Portal",
				TitleFound:     true,
				NormalizedBody: "the new google gemini is here",
			},
			expected: true,
		},
		{
			name: "Negative: Origin alone without Title or Brand",
			ctx: &ruleengine.EvaluationContext{
				Headers:        http.Header{"Server": []string{"ESF"}},
				Title:          "Unknown Portal",
				TitleFound:     true,
				NormalizedBody: "unrelated content",
			},
			expected: false,
		},
		{
			name: "Negative: Origin + AppMarker without Title or Brand",
			ctx: &ruleengine.EvaluationContext{
				Headers:        http.Header{"Server": []string{"GSE"}},
				Title:          "Sign In",
				TitleFound:     true,
				NormalizedBody: "contains wiz_global_data and other markers",
			},
			expected: false,
		},
		{
			name: "Negative: Title contains gemini without Origin or AppMarker",
			ctx: &ruleengine.EvaluationContext{
				Headers:        http.Header{"Server": []string{"Apache"}},
				Title:          "Gemini Fansite",
				TitleFound:     true,
				NormalizedBody: "this is an unofficial fansite",
			},
			expected: false,
		},
		{
			name: "Negative: AppMarker without Title or Origin",
			ctx: &ruleengine.EvaluationContext{
				Headers:        http.Header{"Server": []string{"Cloudflare"}},
				Title:          "Proxy Intercepted",
				TitleFound:     true,
				NormalizedBody: "mirror with wiz_global_data",
			},
			expected: false,
		},
		{
			name: "Negative: Brand in body without Title or Origin",
			ctx: &ruleengine.EvaluationContext{
				Title:          "Blog Post",
				TitleFound:     true,
				NormalizedBody: "article about gemini",
			},
			expected: false,
		},
		{
			name:     "Negative: Completely empty context",
			ctx:      &ruleengine.EvaluationContext{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref := geminiReferencePositiveProof(tt.ctx)
			if ref != tt.expected {
				t.Fatalf("reference test inconsistency: reference returned %v, expected %v", ref, tt.expected)
			}

			got := cond.Evaluate(tt.ctx)
			if got != ref {
				t.Fatalf("Evaluate() = %v does not match gemini/probe.go reference = %v", got, ref)
			}
			if got != tt.expected {
				t.Fatalf("Evaluate() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func BenchmarkGeminiPositiveVerification_1MBBody(b *testing.B) {
	cond := buildGeminiPositiveVerificationCondition()

	// Construct a ~1MB normalized body containing the target markers near the end
	oneMB := 1 << 20
	base := "Lorem ipsum dolor sit amet, consectetur adipiscing elit. "
	repeats := (oneMB - 200) / len(base)
	bodyBuilder := strings.Builder{}
	bodyBuilder.Grow(oneMB)
	for i := 0; i < repeats; i++ {
		bodyBuilder.WriteString(base)
	}
	bodyBuilder.WriteString(" window.wiz_global_data = {}; chat with google gemini today ")
	normalizedBody := bodyBuilder.String()

	ctx := &ruleengine.EvaluationContext{
		Headers:        http.Header{"Server": []string{"ESF"}},
		Title:          "Sign In",
		TitleFound:     true,
		NormalizedBody: normalizedBody,
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !cond.Evaluate(ctx) {
			b.Fatal("expected positive verification to pass")
		}
	}
}
