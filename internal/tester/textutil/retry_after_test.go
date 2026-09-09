package textutil_test

import (
	"net/http"
	"testing"
	"time"

	"gemsub/internal/tester/textutil"
)

func TestParseRetryAfter_Seconds(t *testing.T) {
	d := textutil.ParseRetryAfter("120")
	if d == nil {
		t.Fatal("expected non-nil duration for 120 seconds")
	}
	if *d != 120*time.Second {
		t.Errorf("expected 120s, got %v", *d)
	}

	d = textutil.ParseRetryAfter("   5 \t ")
	if d == nil {
		t.Fatal("expected non-nil duration for trimmed '5'")
	}
	if *d != 5*time.Second {
		t.Errorf("expected 5s, got %v", *d)
	}

	if d := textutil.ParseRetryAfter("0"); d == nil || *d != 0 {
		t.Errorf("expected 0s for '0', got %v", d)
	}
}

func TestParseRetryAfter_InvalidOrEmpty(t *testing.T) {
	if d := textutil.ParseRetryAfter(""); d != nil {
		t.Errorf("expected nil for empty string, got %v", d)
	}
	if d := textutil.ParseRetryAfter("   "); d != nil {
		t.Errorf("expected nil for whitespace string, got %v", d)
	}
	if d := textutil.ParseRetryAfter("-10"); d != nil {
		t.Errorf("expected nil for negative seconds, got %v", d)
	}
	if d := textutil.ParseRetryAfter("invalid-seconds"); d != nil {
		t.Errorf("expected nil for non-numeric string, got %v", d)
	}
}

func TestParseRetryAfter_HTTPDate(t *testing.T) {
	future := time.Now().Add(60 * time.Second).UTC()
	d := textutil.ParseRetryAfter(future.Format(http.TimeFormat))
	if d == nil {
		t.Fatal("expected non-nil duration for future HTTP date")
	}
	if *d < 55*time.Second || *d > 65*time.Second {
		t.Errorf("expected approx 60s, got %v", *d)
	}

	past := time.Now().Add(-60 * time.Second).UTC()
	d = textutil.ParseRetryAfter(past.Format(http.TimeFormat))
	if d == nil {
		t.Fatal("expected non-nil duration for past HTTP date")
	}
	if *d != 0 {
		t.Errorf("expected clamped 0s for past HTTP date, got %v", *d)
	}
}
