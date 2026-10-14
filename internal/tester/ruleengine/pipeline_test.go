package ruleengine_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"gemsub/internal/store"
	"gemsub/internal/tester/classifiercore"
	"gemsub/internal/tester/ruleengine"
)

func TestBuildEvaluationContext(t *testing.T) {
	reqURL, _ := url.Parse("https://example.com/app/test")
	httpReq := &http.Request{URL: reqURL}
	resp := &http.Response{
		StatusCode: 200,
		Header: http.Header{
			"Server":      []string{"GSE"},
			"Retry-After": []string{"60"},
		},
		Request: httpReq,
	}
	resp.Header.Add("Set-Cookie", "SID=abc; Domain=google.com")

	body := []byte(`<!DOCTYPE html><html><head>
<title>Gemini Application</title>
<link rel="canonical" href="https://example.com/canonical" />
</head><body><div id="content">Hello Gemini</div></body></html>`)

	ctx := ruleengine.BuildEvaluationContext(resp, body, 15*time.Millisecond)
	if ctx == nil {
		t.Fatal("expected non-nil EvaluationContext")
	}

	if ctx.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", ctx.StatusCode)
	}
	if ctx.FinalURL == nil || ctx.FinalURL.String() != "https://example.com/app/test" {
		t.Errorf("FinalURL = %v, want https://example.com/app/test", ctx.FinalURL)
	}
	if ctx.Headers.Get("Server") != "GSE" {
		t.Errorf("Headers.Get(Server) = %q, want GSE", ctx.Headers.Get("Server"))
	}
	if !ctx.TitleFound || ctx.Title != "Gemini Application" {
		t.Errorf("Title = %q (found %v), want 'Gemini Application'", ctx.Title, ctx.TitleFound)
	}
	if !ctx.CanonicalLinkFound || ctx.CanonicalLink != "https://example.com/canonical" {
		t.Errorf("CanonicalLink = %q (found %v), want 'https://example.com/canonical'", ctx.CanonicalLink, ctx.CanonicalLinkFound)
	}
	if ctx.RetryAfter == nil || *ctx.RetryAfter != 60*time.Second {
		t.Errorf("RetryAfter = %v, want 60s", ctx.RetryAfter)
	}
	if ctx.MeasuredLatency != 15*time.Millisecond {
		t.Errorf("MeasuredLatency = %v, want 15ms", ctx.MeasuredLatency)
	}
	if !strings.Contains(ctx.NormalizedBody, "hello gemini") {
		t.Errorf("NormalizedBody does not contain 'hello gemini': %q", ctx.NormalizedBody)
	}

	t.Run("body capped at 1MB", func(t *testing.T) {
		largeBody := make([]byte, 2*1024*1024)
		for i := range largeBody {
			largeBody[i] = 'a'
		}
		c := ruleengine.BuildEvaluationContext(resp, largeBody, 10*time.Millisecond)
		if len(c.RawBody) != classifiercore.MaxInspectBytes {
			t.Errorf("len(RawBody) = %d, want %d", len(c.RawBody), classifiercore.MaxInspectBytes)
		}
		if len(c.NormalizedBody) != classifiercore.MaxInspectBytes {
			t.Errorf("len(NormalizedBody) = %d, want %d", len(c.NormalizedBody), classifiercore.MaxInspectBytes)
		}
	})

	t.Run("latency clamped to strictly positive", func(t *testing.T) {
		c := ruleengine.BuildEvaluationContext(resp, body, 0)
		if c.MeasuredLatency <= 0 {
			t.Errorf("MeasuredLatency = %v, want > 0", c.MeasuredLatency)
		}
	})

	t.Run("nil response handled gracefully", func(t *testing.T) {
		c := ruleengine.BuildEvaluationContext(nil, body, 5*time.Millisecond)
		if c.StatusCode != 0 {
			t.Errorf("StatusCode = %d, want 0", c.StatusCode)
		}
		if c.Title != "Gemini Application" {
			t.Errorf("Title = %q, want 'Gemini Application'", c.Title)
		}
	})
}

func TestRuleSetValidation(t *testing.T) {
	t.Run("valid rule set with inferred phases", func(t *testing.T) {
		rs, err := ruleengine.NewRuleSet(ruleengine.RuleSetConfig{
			Name: "test",
			Mode: ruleengine.ModeStrict,
			Rules: []ruleengine.Rule{
				{
					Condition: ruleengine.NewLeafCondition(ruleengine.NormalizedBodySignal(), ruleengine.ContainsAny("region blocked")),
					Verdict:   ruleengine.Verdict{Status: store.StatusFailed, Category: store.ErrRegionBlocked, Reason: "blocked"},
				},
				{
					Condition: ruleengine.NewLeafCondition(ruleengine.NormalizedBodySignal(), ruleengine.ContainsAny("verified")),
					Verdict:   ruleengine.Verdict{Status: store.StatusPassed, Category: store.ErrNone, Reason: "ok"},
				},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(rs.Rejections) != 1 {
			t.Errorf("Rejections count = %d, want 1", len(rs.Rejections))
		}
		if len(rs.Verifications) != 1 {
			t.Errorf("Verifications count = %d, want 1", len(rs.Verifications))
		}
		if len(rs.AcceptedStatusCodes) != 1 || rs.AcceptedStatusCodes[0] != 200 {
			t.Errorf("AcceptedStatusCodes = %v, want [200]", rs.AcceptedStatusCodes)
		}
	})

	t.Run("explicit phases partition correctly", func(t *testing.T) {
		rs, err := ruleengine.NewRuleSet(ruleengine.RuleSetConfig{
			Name: "test",
			Mode: ruleengine.ModeStrict,
			Rules: []ruleengine.Rule{
				{
					Phase:     "interception",
					Condition: ruleengine.NewLeafCondition(ruleengine.NormalizedBodySignal(), ruleengine.ContainsAny("portal")),
					Verdict:   ruleengine.Verdict{Status: store.StatusFailed, Category: store.ErrTargetOther, Reason: "portal"},
				},
				{
					Phase:     "rejection",
					Condition: ruleengine.NewLeafCondition(ruleengine.NormalizedBodySignal(), ruleengine.ContainsAny("blocked")),
					Verdict:   ruleengine.Verdict{Status: store.StatusFailed, Category: store.ErrRegionBlocked, Reason: "blocked"},
				},
				{
					Phase:     "verification",
					Condition: ruleengine.NewLeafCondition(ruleengine.NormalizedBodySignal(), ruleengine.ContainsAny("ok")),
					Verdict:   ruleengine.Verdict{Status: store.StatusPassed, Category: store.ErrNone, Reason: "ok"},
				},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(rs.Interceptions) != 1 {
			t.Errorf("Interceptions count = %d, want 1", len(rs.Interceptions))
		}
		if len(rs.Rejections) != 1 {
			t.Errorf("Rejections count = %d, want 1", len(rs.Rejections))
		}
		if len(rs.Verifications) != 1 {
			t.Errorf("Verifications count = %d, want 1", len(rs.Verifications))
		}
	})

	t.Run("reject passed verdict with non-none category", func(t *testing.T) {
		_, err := ruleengine.NewRuleSet(ruleengine.RuleSetConfig{
			Name: "test",
			Mode: ruleengine.ModeStrict,
			Rules: []ruleengine.Rule{
				{
					Condition: ruleengine.NewLeafCondition(ruleengine.TitleSignal(), ruleengine.Equals("ok")),
					Verdict:   ruleengine.Verdict{Status: store.StatusPassed, Category: store.ErrRegionBlocked, Reason: "ok"},
				},
			},
		})
		if err == nil {
			t.Fatal("expected error for passed verdict with non-none category, got nil")
		}
		if !strings.Contains(err.Error(), "passed") {
			t.Errorf("error = %q, want mention of passed", err.Error())
		}
	})

	t.Run("reject failed verdict without category", func(t *testing.T) {
		_, err := ruleengine.NewRuleSet(ruleengine.RuleSetConfig{
			Name: "test",
			Mode: ruleengine.ModeStrict,
			Rules: []ruleengine.Rule{
				{
					Condition: ruleengine.NewLeafCondition(ruleengine.TitleSignal(), ruleengine.Equals("fail")),
					Verdict:   ruleengine.Verdict{Status: store.StatusFailed, Category: "", Reason: "fail"},
				},
			},
		})
		if err == nil {
			t.Fatal("expected error for failed verdict without category, got nil")
		}
	})

	t.Run("reject inconclusive verdict without category", func(t *testing.T) {
		_, err := ruleengine.NewRuleSet(ruleengine.RuleSetConfig{
			Name: "test",
			Mode: ruleengine.ModeStrict,
			Rules: []ruleengine.Rule{
				{
					Condition: ruleengine.NewLeafCondition(ruleengine.TitleSignal(), ruleengine.Equals("maybe")),
					Verdict:   ruleengine.Verdict{Status: store.StatusInconclusive, Category: store.ErrNone, Reason: "maybe"},
				},
			},
		})
		if err == nil {
			t.Fatal("expected error for inconclusive verdict with ErrNone, got nil")
		}
	})

	t.Run("reject passed verdict in rejection phase", func(t *testing.T) {
		_, err := ruleengine.NewRuleSet(ruleengine.RuleSetConfig{
			Name: "test",
			Mode: ruleengine.ModeStrict,
			Rules: []ruleengine.Rule{
				{
					Phase:     "rejection",
					Condition: ruleengine.NewLeafCondition(ruleengine.TitleSignal(), ruleengine.Equals("ok")),
					Verdict:   ruleengine.Verdict{Status: store.StatusPassed, Category: store.ErrNone, Reason: "ok"},
				},
			},
		})
		if err == nil {
			t.Fatal("expected contradiction error, got nil")
		}
		if !strings.Contains(err.Error(), "contradiction") && !strings.Contains(err.Error(), "rejection") {
			t.Errorf("error = %q, want contradiction in rejection", err.Error())
		}
	})

	t.Run("reject passed verdict in interception phase", func(t *testing.T) {
		_, err := ruleengine.NewRuleSet(ruleengine.RuleSetConfig{
			Name: "test",
			Mode: ruleengine.ModeStrict,
			Rules: []ruleengine.Rule{
				{
					Phase:     "interception",
					Condition: ruleengine.NewLeafCondition(ruleengine.TitleSignal(), ruleengine.Equals("ok")),
					Verdict:   ruleengine.Verdict{Status: store.StatusPassed, Category: store.ErrNone, Reason: "ok"},
				},
			},
		})
		if err == nil {
			t.Fatal("expected contradiction error, got nil")
		}
	})

	t.Run("reject failed verdict in verification phase", func(t *testing.T) {
		_, err := ruleengine.NewRuleSet(ruleengine.RuleSetConfig{
			Name: "test",
			Mode: ruleengine.ModeStrict,
			Rules: []ruleengine.Rule{
				{
					Phase:     "verification",
					Condition: ruleengine.NewLeafCondition(ruleengine.TitleSignal(), ruleengine.Equals("fail")),
					Verdict:   ruleengine.Verdict{Status: store.StatusFailed, Category: store.ErrTargetOther, Reason: "fail"},
				},
			},
		})
		if err == nil {
			t.Fatal("expected contradiction error, got nil")
		}
	})

	t.Run("reject unknown phase", func(t *testing.T) {
		_, err := ruleengine.NewRuleSet(ruleengine.RuleSetConfig{
			Name: "test",
			Mode: ruleengine.ModeStrict,
			Rules: []ruleengine.Rule{
				{
					Phase:     "unknown_phase",
					Condition: ruleengine.NewLeafCondition(ruleengine.TitleSignal(), ruleengine.Equals("test")),
					Verdict:   ruleengine.Verdict{Status: store.StatusPassed, Category: store.ErrNone, Reason: "ok"},
				},
			},
		})
		if err == nil {
			t.Fatal("expected error for unknown phase, got nil")
		}
	})

	t.Run("reject ContainsAny on SignalRawBody", func(t *testing.T) {
		_, err := ruleengine.NewRuleSet(ruleengine.RuleSetConfig{
			Name: "test",
			Mode: ruleengine.ModeStrict,
			Rules: []ruleengine.Rule{
				{
					Condition: ruleengine.NewLeafCondition(ruleengine.RawBodySignal(), ruleengine.ContainsAny("needle")),
					Verdict:   ruleengine.Verdict{Status: store.StatusFailed, Category: store.ErrRegionBlocked, Reason: "blocked"},
				},
			},
		})
		if err == nil {
			t.Fatal("expected error when ContainsAny is paired with raw_body, got nil")
		}
		if !strings.Contains(err.Error(), "raw_body") || !strings.Contains(err.Error(), "normalized_body") {
			t.Errorf("error %q should mention raw_body and suggest normalized_body", err.Error())
		}
	})

	t.Run("reject ContainsAny on SignalRawBody nested in AllOf", func(t *testing.T) {
		_, err := ruleengine.NewRuleSet(ruleengine.RuleSetConfig{
			Name: "test",
			Mode: ruleengine.ModeStrict,
			Rules: []ruleengine.Rule{
				{
					Condition: ruleengine.AllOf(
						ruleengine.NewLeafCondition(ruleengine.HTTPStatusSignal(), ruleengine.Equals(200)),
						ruleengine.NewLeafCondition(ruleengine.RawBodySignal(), ruleengine.ContainsAny("needle")),
					),
					Verdict: ruleengine.Verdict{Status: store.StatusFailed, Category: store.ErrRegionBlocked, Reason: "blocked"},
				},
			},
		})
		if err == nil {
			t.Fatal("expected error when ContainsAny is paired with raw_body inside AllOf, got nil")
		}
	})

	t.Run("unmarshal JSON rule set", func(t *testing.T) {
		rawJSON := `{
			"name": "gemini",
			"mode": "strict",
			"accepted_status_codes": [200],
			"strict_mode_fallback_reason": "gemini strict fallback",
			"rules": [
				{
					"signal": "http_status",
					"match": {"equals": 200},
					"verdict": {"status": "passed", "reason": "ok"}
				}
			]
		}`
		var rs ruleengine.RuleSet
		if err := json.Unmarshal([]byte(rawJSON), &rs); err != nil {
			t.Fatalf("failed to unmarshal JSON: %v", err)
		}
		if rs.Name != "gemini" {
			t.Errorf("Name = %q, want gemini", rs.Name)
		}
		if rs.Mode != ruleengine.ModeStrict {
			t.Errorf("Mode = %q, want strict", rs.Mode)
		}
		if rs.EffectiveStrictFallbackReason() != "gemini strict fallback" {
			t.Errorf("fallback = %q, want 'gemini strict fallback'", rs.EffectiveStrictFallbackReason())
		}
		if len(rs.Verifications) != 1 {
			t.Fatalf("Verifications count = %d, want 1", len(rs.Verifications))
		}
	})
}

func TestEvaluate_Phase1_CommonHTTPStatus(t *testing.T) {
	rs, err := ruleengine.NewRuleSet(ruleengine.RuleSetConfig{
		Name: "test",
		Mode: ruleengine.ModeStrict,
	})
	if err != nil {
		t.Fatalf("NewRuleSet error: %v", err)
	}

	tests := []struct {
		code         int
		header       http.Header
		wantStatus   store.Status
		wantCat      store.ErrorCategory
		wantRetry    bool
		wantRetryDur *time.Duration
	}{
		{
			code:       403,
			wantStatus: store.StatusFailed,
			wantCat:    store.ErrTargetDenied,
			wantRetry:  false,
		},
		{
			code:         429,
			header:       http.Header{"Retry-After": []string{"120"}},
			wantStatus:   store.StatusInconclusive,
			wantCat:      store.ErrTargetRateLimited,
			wantRetry:    true,
			wantRetryDur: func() *time.Duration { d := 120 * time.Second; return &d }(),
		},
		{
			code:       503,
			wantStatus: store.StatusInconclusive,
			wantCat:    store.ErrTargetError,
			wantRetry:  true,
		},
		{
			code:       500,
			wantStatus: store.StatusInconclusive,
			wantCat:    store.ErrTargetError,
			wantRetry:  false,
		},
		{
			code:       502,
			wantStatus: store.StatusInconclusive,
			wantCat:    store.ErrTargetError,
			wantRetry:  false,
		},
		{
			code:       504,
			wantStatus: store.StatusInconclusive,
			wantCat:    store.ErrTargetError,
			wantRetry:  false,
		},
	}

	for _, tc := range tests {
		t.Run(http.StatusText(tc.code), func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tc.code,
				Header:     tc.header,
			}
			ctx := ruleengine.BuildEvaluationContext(resp, []byte("body"), 10*time.Millisecond)
			res := ruleengine.Evaluate(ctx, rs)

			if res.Status != tc.wantStatus {
				t.Errorf("Status = %v, want %v", res.Status, tc.wantStatus)
			}
			if res.Category != tc.wantCat {
				t.Errorf("Category = %v, want %v", res.Category, tc.wantCat)
			}
			if res.StatusCode != tc.code {
				t.Errorf("StatusCode = %d, want %d", res.StatusCode, tc.code)
			}
			if res.Retryable != tc.wantRetry {
				t.Errorf("Retryable = %v, want %v", res.Retryable, tc.wantRetry)
			}
			if tc.wantRetryDur != nil {
				if res.RetryAfter == nil || *res.RetryAfter != *tc.wantRetryDur {
					t.Errorf("RetryAfter = %v, want %v", res.RetryAfter, *tc.wantRetryDur)
				}
			}
			if res.Latency <= 0 {
				t.Errorf("Latency = %v, want > 0", res.Latency)
			}
		})
	}
}

func TestEvaluate_Phase2_AcceptedStatusGate(t *testing.T) {
	rs, err := ruleengine.NewRuleSet(ruleengine.RuleSetConfig{
		Name: "test",
		Mode: ruleengine.ModeStrict,
		Rules: []ruleengine.Rule{
			{
				Condition: ruleengine.NewLeafCondition(ruleengine.NormalizedBodySignal(), ruleengine.ContainsAny("unavailable in region")),
				Verdict:   ruleengine.Verdict{Status: store.StatusFailed, Category: store.ErrRegionBlocked, Reason: "region restriction"},
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRuleSet error: %v", err)
	}

	// HTTP 451 with a body containing region-block phrase:
	// Accepted-status gate must emit "unexpected HTTP status 451" with ErrTargetOther,
	// NOT ErrRegionBlocked from Phase 4 rejection rules!
	resp := &http.Response{
		StatusCode: 451,
		Header:     http.Header{},
	}
	body := []byte("<html><body>app unavailable in region</body></html>")
	ctx := ruleengine.BuildEvaluationContext(resp, body, 10*time.Millisecond)
	res := ruleengine.Evaluate(ctx, rs)

	if res.Status != store.StatusFailed {
		t.Errorf("Status = %v, want StatusFailed", res.Status)
	}
	if res.Category != store.ErrTargetOther {
		t.Errorf("Category = %v, want ErrTargetOther (unexpected status, NOT ErrRegionBlocked)", res.Category)
	}
	if res.StatusCode != 451 {
		t.Errorf("StatusCode = %d, want 451", res.StatusCode)
	}
	if res.Reason != "unexpected HTTP status 451" {
		t.Errorf("Reason = %q, want 'unexpected HTTP status 451'", res.Reason)
	}
	if res.Retryable {
		t.Errorf("Retryable = true, want false")
	}
}

func TestEvaluate_Phase3_InterceptionPreemption(t *testing.T) {
	rs, err := ruleengine.NewRuleSet(ruleengine.RuleSetConfig{
		Name: "test",
		Mode: ruleengine.ModeStrict,
		Rules: []ruleengine.Rule{
			{
				Phase:     "rejection",
				Condition: ruleengine.NewLeafCondition(ruleengine.NormalizedBodySignal(), ruleengine.ContainsAny("not available in your region")),
				Verdict:   ruleengine.Verdict{Status: store.StatusFailed, Category: store.ErrRegionBlocked, Reason: "region restriction"},
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRuleSet error: %v", err)
	}

	// Response body matches BOTH an interception phrase ("attention required! | cloudflare")
	// AND a regional block phrase ("not available in your region").
	// Interception must win, returning ErrTargetOther.
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
	}
	body := []byte("<html><title>Attention Required! | Cloudflare</title><body>Attention Required! | Cloudflare: not available in your region</body></html>")
	ctx := ruleengine.BuildEvaluationContext(resp, body, 10*time.Millisecond)
	res := ruleengine.Evaluate(ctx, rs)

	if res.Status != store.StatusFailed {
		t.Errorf("Status = %v, want StatusFailed", res.Status)
	}
	if res.Category != store.ErrTargetOther {
		t.Errorf("Category = %v, want ErrTargetOther (interception preemption, NOT ErrRegionBlocked)", res.Category)
	}
	if res.Reason != classifiercore.InterceptionReason {
		t.Errorf("Reason = %q, want %q", res.Reason, classifiercore.InterceptionReason)
	}
}

func TestEvaluate_Phase4_NegativeRejections(t *testing.T) {
	rs, err := ruleengine.NewRuleSet(ruleengine.RuleSetConfig{
		Name: "test",
		Mode: ruleengine.ModeStrict,
		Rules: []ruleengine.Rule{
			{
				Condition: ruleengine.NewLeafCondition(ruleengine.NormalizedBodySignal(), ruleengine.ContainsAny("not available in your region")),
				Verdict:   ruleengine.Verdict{Status: store.StatusFailed, Category: store.ErrRegionBlocked, Reason: "region restriction"},
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRuleSet error: %v", err)
	}

	resp := &http.Response{StatusCode: 200, Header: http.Header{}}
	body := []byte("<html><body>This service is not available in your region.</body></html>")
	ctx := ruleengine.BuildEvaluationContext(resp, body, 10*time.Millisecond)
	res := ruleengine.Evaluate(ctx, rs)

	if res.Status != store.StatusFailed {
		t.Errorf("Status = %v, want StatusFailed", res.Status)
	}
	if res.Category != store.ErrRegionBlocked {
		t.Errorf("Category = %v, want ErrRegionBlocked", res.Category)
	}
	if res.Reason != "region restriction" {
		t.Errorf("Reason = %q, want 'region restriction'", res.Reason)
	}
}

func TestEvaluate_Phase5_PositiveVerification_StrictMode(t *testing.T) {
	rs, err := ruleengine.NewRuleSet(ruleengine.RuleSetConfig{
		Name:                     "gemini",
		Mode:                     ruleengine.ModeStrict,
		StrictModeFallbackReason: "possible interception / false positive: positive verification signals absent",
		Rules: []ruleengine.Rule{
			{
				Condition: ruleengine.NewLeafCondition(ruleengine.TitleSignal(), ruleengine.ContainsAny("gemini")),
				Verdict:   ruleengine.Verdict{Status: store.StatusPassed, Category: store.ErrNone, Reason: "ok"},
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRuleSet error: %v", err)
	}

	t.Run("verification matched", func(t *testing.T) {
		resp := &http.Response{StatusCode: 200, Header: http.Header{}}
		body := []byte("<html><head><title>Google Gemini</title></head><body>Welcome</body></html>")
		ctx := ruleengine.BuildEvaluationContext(resp, body, 10*time.Millisecond)
		res := ruleengine.Evaluate(ctx, rs)

		if res.Status != store.StatusPassed {
			t.Errorf("Status = %v, want StatusPassed", res.Status)
		}
		if res.Category != store.ErrNone {
			t.Errorf("Category = %v, want ErrNone", res.Category)
		}
		if res.Reason != "ok" {
			t.Errorf("Reason = %q, want 'ok'", res.Reason)
		}
	})

	t.Run("verification missing yields strict fallback", func(t *testing.T) {
		resp := &http.Response{StatusCode: 200, Header: http.Header{}}
		body := []byte("<html><head><title>Some Random Page</title></head><body>Nothing here</body></html>")
		ctx := ruleengine.BuildEvaluationContext(resp, body, 10*time.Millisecond)
		res := ruleengine.Evaluate(ctx, rs)

		if res.Status != store.StatusFailed {
			t.Errorf("Status = %v, want StatusFailed", res.Status)
		}
		if res.Category != store.ErrTargetOther {
			t.Errorf("Category = %v, want ErrTargetOther", res.Category)
		}
		if res.Reason != "possible interception / false positive: positive verification signals absent" {
			t.Errorf("Reason = %q, want strict fallback reason", res.Reason)
		}
	})
}

func TestEvaluate_Phase5_PermissiveMode(t *testing.T) {
	rs, err := ruleengine.NewRuleSet(ruleengine.RuleSetConfig{
		Name: "test-permissive",
		Mode: ruleengine.ModePermissive,
		Rules: []ruleengine.Rule{
			{
				Condition: ruleengine.NewLeafCondition(ruleengine.NormalizedBodySignal(), ruleengine.ContainsAny("blocked")),
				Verdict:   ruleengine.Verdict{Status: store.StatusFailed, Category: store.ErrRegionBlocked, Reason: "blocked"},
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRuleSet error: %v", err)
	}

	resp := &http.Response{StatusCode: 200, Header: http.Header{}}
	body := []byte("<html><head><title>Random Page</title></head><body>No negative markers</body></html>")
	ctx := ruleengine.BuildEvaluationContext(resp, body, 10*time.Millisecond)
	res := ruleengine.Evaluate(ctx, rs)

	if res.Status != store.StatusPassed {
		t.Errorf("Status = %v, want StatusPassed in permissive mode", res.Status)
	}
	if res.Category != store.ErrNone {
		t.Errorf("Category = %v, want ErrNone", res.Category)
	}
	if res.Reason != "ok" {
		t.Errorf("Reason = %q, want 'ok'", res.Reason)
	}
}

func TestEvaluate_LatencyClamping(t *testing.T) {
	rs, err := ruleengine.NewRuleSet(ruleengine.RuleSetConfig{
		Name: "test",
		Mode: ruleengine.ModePermissive,
	})
	if err != nil {
		t.Fatalf("NewRuleSet error: %v", err)
	}

	resp := &http.Response{StatusCode: 200, Header: http.Header{}}
	// Pass zero latency:
	ctx := ruleengine.BuildEvaluationContext(resp, []byte("ok"), 0)
	res := ruleengine.Evaluate(ctx, rs)

	if res.Latency <= 0 {
		t.Errorf("res.Latency = %v, want > 0 (strictly positive clamped)", res.Latency)
	}
}

func BenchmarkEvaluate_1MBBody(b *testing.B) {
	// Build Gemini's full verification rule set shape
	originProof := ruleengine.AnyOf(
		ruleengine.NewLeafCondition(ruleengine.HeaderSignal("Server"), ruleengine.MustRegex(`(?i)ESF|GSE|SFFE|GWS`)),
		ruleengine.NewLeafCondition(ruleengine.CookieDomainSignal(), ruleengine.ContainsAny("google.com", "gemini.google.com")),
	)
	titleOrBrand := ruleengine.AnyOf(
		ruleengine.NewLeafCondition(ruleengine.TitleSignal(), ruleengine.ContainsAny("gemini")),
		ruleengine.NewLeafCondition(ruleengine.NormalizedBodySignal(), ruleengine.ContainsAny("gemini")),
	)
	appMarkers := ruleengine.AllOf(
		ruleengine.NewLeafCondition(ruleengine.TitleSignal(), ruleengine.ContainsAny("gemini")),
		ruleengine.NewLeafCondition(ruleengine.NormalizedBodySignal(), ruleengine.ContainsAny("bardchatui", "wiz_global_data", "google gemini")),
	)
	geminiPositiveVerification := ruleengine.AnyOf(
		ruleengine.AllOf(originProof, titleOrBrand),
		appMarkers,
	)

	rs, err := ruleengine.NewRuleSet(ruleengine.RuleSetConfig{
		Name:                     "gemini",
		Mode:                     ruleengine.ModeStrict,
		StrictModeFallbackReason: "possible interception / false positive: positive verification signals absent",
		Rules: []ruleengine.Rule{
			// Negative checks
			{
				Phase:     "rejection",
				Condition: ruleengine.NewLeafCondition(ruleengine.RawBodySignal(), ruleengine.MustRegex(`"2003":\s*"([A-Z]{2})"`)),
				Verdict:   ruleengine.Verdict{Status: store.StatusFailed, Category: store.ErrRegionBlocked, Reason: "restricted country"},
			},
			{
				Phase:     "rejection",
				Condition: ruleengine.NewLeafCondition(ruleengine.RawBodySignal(), ruleengine.MustRegex(`LOCATION_REJECTED|no_access`)),
				Verdict:   ruleengine.Verdict{Status: store.StatusFailed, Category: store.ErrRegionBlocked, Reason: "restriction flag"},
			},
			{
				Phase: "rejection",
				Condition: ruleengine.NewLeafCondition(ruleengine.NormalizedBodySignal(), ruleengine.ContainsAny(
					"gemini isn't available", "not available in your country", "supported languages and countries",
				)),
				Verdict: ruleengine.Verdict{Status: store.StatusFailed, Category: store.ErrRegionBlocked, Reason: "region restriction message"},
			},
			// Positive verification
			{
				Phase:     "verification",
				Condition: geminiPositiveVerification,
				Verdict:   ruleengine.Verdict{Status: store.StatusPassed, Category: store.ErrNone, Reason: "ok"},
			},
		},
	})
	if err != nil {
		b.Fatalf("NewRuleSet error: %v", err)
	}

	// 1MB payload simulating a successful Gemini response
	bodyBuilder := strings.Builder{}
	bodyBuilder.Grow(1024 * 1024)
	bodyBuilder.WriteString("<!DOCTYPE html><html><head><title>Google Gemini</title></head><body>")
	for bodyBuilder.Len() < 1024*1024-50 {
		bodyBuilder.WriteString("<div class=\"content\">Google Gemini AI Assistant response stream padding data...</div>\n")
	}
	bodyBuilder.WriteString("</body></html>")
	bodyBytes := []byte(bodyBuilder.String())

	resp := &http.Response{
		StatusCode: 200,
		Header: http.Header{
			"Server": []string{"GSE"},
		},
	}
	ctx := ruleengine.BuildEvaluationContext(resp, bodyBytes, 25*time.Millisecond)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		res := ruleengine.Evaluate(ctx, rs)
		if res.Status != store.StatusPassed {
			b.Fatalf("expected StatusPassed, got %v (%s)", res.Status, res.Reason)
		}
	}
}
