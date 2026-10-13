package ruleengine_test

import (
	"fmt"
	"strings"
	"testing"

	"gemsub/internal/tester/ruleengine"
)

func TestEquals_NumericCoercion(t *testing.T) {
	// JSON unmarshaling numbers into any produces float64.
	// HTTP status codes in EvaluationContext are int.
	matcher := ruleengine.Equals(float64(200))

	if !matcher.Match(200) {
		t.Fatalf("expected Equals(float64(200)) to match int(200)")
	}
	if !matcher.Match(int64(200)) {
		t.Fatalf("expected Equals(float64(200)) to match int64(200)")
	}
	if !matcher.Match(float64(200)) {
		t.Fatalf("expected Equals(float64(200)) to match float64(200)")
	}
	if matcher.Match(404) {
		t.Fatalf("expected Equals(float64(200)) not to match int(404)")
	}

	intMatcher := ruleengine.Equals(200)
	if !intMatcher.Match(float64(200)) {
		t.Fatalf("expected Equals(int(200)) to match float64(200)")
	}
}

func TestEquals_StringsAndBooleans(t *testing.T) {
	strMatcher := ruleengine.Equals("ok")
	if !strMatcher.Match("ok") {
		t.Fatalf("expected Equals('ok') to match 'ok'")
	}
	if strMatcher.Match("OK") {
		t.Fatalf("expected Equals to be exact for strings")
	}

	boolMatcher := ruleengine.Equals(true)
	if !boolMatcher.Match(true) {
		t.Fatalf("expected Equals(true) to match true")
	}
	if boolMatcher.Match(false) {
		t.Fatalf("expected Equals(true) not to match false")
	}
}

func TestContainsAny_NormalizedNeedles(t *testing.T) {
	// Needle with repeated whitespace and HTML-entity apostrophe (&#39;)
	// as might be provided in block phrases or JSON configuration.
	needle := "can&#39;t   access   service"
	matcher := ruleengine.ContainsAny(needle)

	// Normalized body containing decoded apostrophe and collapsed whitespace
	body := "we apologize, you can't access service in your area."
	if !matcher.Match(body) {
		t.Fatalf("expected ContainsAny to match normalized needle against normalized body")
	}

	// Mixed case needle normalizes to lowercase and matches normalized body
	upperNeedle := "CAN'T   ACCESS   SERVICE"
	upperMatcher := ruleengine.ContainsAny(upperNeedle)
	if !upperMatcher.Match(body) {
		t.Fatalf("expected uppercase needle to normalize and match normalized body")
	}

	// Unrelated text should not match
	if matcher.Match("welcome to the dashboard") {
		t.Fatalf("expected ContainsAny not to match unrelated text")
	}
}

func TestConstructors_PanicOnInvalidArgs(t *testing.T) {
	assertPanic := func(name string, f func(), expectedSubstr string) {
		t.Helper()
		defer func() {
			r := recover()
			if r == nil {
				t.Fatalf("%s: expected panic, but function returned normally", name)
			}
			msg := fmt.Sprint(r)
			if !strings.Contains(msg, expectedSubstr) {
				t.Fatalf("%s: expected panic containing %q, got %q", name, expectedSubstr, msg)
			}
		}()
		f()
	}

	assertPanic("AllOf empty", func() {
		ruleengine.AllOf()
	}, "AllOf requires at least 1 condition")

	assertPanic("AllOf with nil", func() {
		ruleengine.AllOf(nil)
	}, "AllOf condition at index 0 is nil")

	assertPanic("AnyOf empty", func() {
		ruleengine.AnyOf()
	}, "AnyOf requires at least 1 condition")

	assertPanic("AnyOf with nil", func() {
		ruleengine.AnyOf(nil)
	}, "AnyOf condition at index 0 is nil")

	assertPanic("Not with nil", func() {
		ruleengine.Not(nil)
	}, "Not condition cannot be nil")

	assertPanic("ContainsAny empty", func() {
		ruleengine.ContainsAny()
	}, "ContainsAny requires at least 1 needle")

	assertPanic("ContainsAny whitespace needle", func() {
		ruleengine.ContainsAny("   ")
	}, "empty after normalization")

	assertPanic("InRange min > max", func() {
		ruleengine.InRange(300, 200)
	}, "InRange min (300) cannot be greater than max (200)")
}

func TestContainsAny_SliceMatching(t *testing.T) {
	matcher := ruleengine.ContainsAny("google.com")
	slice := []string{"cloudflare.com", "gemini.google.com", "example.org"}

	if !matcher.Match(slice) {
		t.Fatalf("expected ContainsAny to match slice containing needle substring")
	}

	if matcher.Match([]string{"bing.com", "yahoo.com"}) {
		t.Fatalf("expected ContainsAny not to match slice without needle")
	}
}

func TestRegex_CompilationAndMatching(t *testing.T) {
	matcher, err := ruleengine.Regex(`^app-(?:unavailable|restricted)`)
	if err != nil {
		t.Fatalf("unexpected error compiling regex: %v", err)
	}

	if !matcher.Match("app-unavailable-in-region") {
		t.Fatalf("expected regex to match prefix")
	}
	if matcher.Match("APP-UNAVAILABLE") {
		t.Fatalf("expected default regex to be case-sensitive")
	}

	// Case-insensitive flag
	ciMatcher, err := ruleengine.Regex(`(?i)^app-unavailable`)
	if err != nil {
		t.Fatalf("unexpected error compiling case-insensitive regex: %v", err)
	}
	if !ciMatcher.Match("APP-UNAVAILABLE-IN-REGION") {
		t.Fatalf("expected (?i) regex to match uppercase input")
	}

	// Invalid regex fails fast
	_, err = ruleengine.Regex(`(?P<invalid`)
	if err == nil {
		t.Fatalf("expected invalid regex to return error")
	}
}

func TestRegexExtractIn(t *testing.T) {
	// Pattern extracting 2-letter country code from WIZ_global_data
	pattern := `(?:"|\b)country_code(?:"|\b)\s*:\s*"([A-Z]{2})"`
	restrictedSet := []string{"IR", "CU", "SY", "KP"}

	matcher, err := ruleengine.RegexExtractIn(pattern, restrictedSet)
	if err != nil {
		t.Fatalf("unexpected error constructing RegexExtractIn: %v", err)
	}

	bodyMatch := `{"user": "anon", "country_code": "IR", "status": 1}`
	if !matcher.Match(bodyMatch) {
		t.Fatalf("expected RegexExtractIn to match restricted country 'IR'")
	}

	bodyNonMatch := `{"user": "anon", "country_code": "US", "status": 1}`
	if matcher.Match(bodyNonMatch) {
		t.Fatalf("expected RegexExtractIn not to match non-restricted country 'US'")
	}

	// Invalid pattern fails fast
	_, err = ruleengine.RegexExtractIn(`(unclosed`, restrictedSet)
	if err == nil {
		t.Fatalf("expected error on invalid regex pattern")
	}
}

func TestInRange(t *testing.T) {
	matcher := ruleengine.InRange(200, 299)

	if !matcher.Match(200) {
		t.Fatalf("expected InRange to match lower bound 200")
	}
	if !matcher.Match(299) {
		t.Fatalf("expected InRange to match upper bound 299")
	}
	if !matcher.Match(204) {
		t.Fatalf("expected InRange to match intermediate 204")
	}
	if !matcher.Match(float64(200)) {
		t.Fatalf("expected InRange to match float64(200)")
	}
	if matcher.Match(199) {
		t.Fatalf("expected InRange not to match 199")
	}
	if matcher.Match(300) {
		t.Fatalf("expected InRange not to match 300")
	}
}
