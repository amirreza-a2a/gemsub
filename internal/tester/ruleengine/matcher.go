package ruleengine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"gemsub/internal/tester/textutil"
)

// Matcher evaluates whether an extracted signal value satisfies a predicate.
type Matcher interface {
	Match(val any) bool
}

// =============================================================================
// Equals Matcher
// =============================================================================

// Equals returns a Matcher that performs scalar equality with numeric coercion.
// For example, JSON-decoded float64(200) equals int(200).
func Equals(expected any) Matcher {
	return &equalsMatcher{expected: expected}
}

type equalsMatcher struct {
	expected any
}

func (m *equalsMatcher) Match(val any) bool {
	if val == nil && m.expected == nil {
		return true
	}
	if val == nil || m.expected == nil {
		return false
	}

	// Try numeric coercion first
	valNum, valIsNum := toFloat64(val)
	expNum, expIsNum := toFloat64(m.expected)
	if valIsNum && expIsNum {
		return valNum == expNum
	}

	// Fallback to exact type-checked equality
	return reflect.DeepEqual(val, m.expected)
}

func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	default:
		return 0, false
	}
}

// =============================================================================
// ContainsAny Matcher
// =============================================================================

// ContainsAny returns a Matcher that checks if a signal contains any of the specified needles.
// All needle arguments are normalized using textutil.NormalizeText at construction time,
// collapsing whitespace and unescaping HTML entities (mirroring gemini/probe.go:220-222).
// Panics if values is empty or if any needle is empty after normalization.
func ContainsAny(values ...string) Matcher {
	if len(values) == 0 {
		panic("ruleengine: ContainsAny requires at least 1 needle")
	}
	needles := make([]string, 0, len(values))
	for i, v := range values {
		norm := textutil.NormalizeText(v)
		if norm == "" {
			panic(fmt.Sprintf("ruleengine: needle %q (index %d) is empty after normalization", v, i))
		}
		needles = append(needles, norm)
	}
	return &containsAnyMatcher{needles: needles}
}

type containsAnyMatcher struct {
	needles []string
}

func (m *containsAnyMatcher) Match(val any) bool {
	if len(m.needles) == 0 || val == nil {
		return false
	}

	switch v := val.(type) {
	case string:
		for _, needle := range m.needles {
			if strings.Contains(v, needle) {
				return true
			}
		}
		return false
	case []string:
		for _, item := range v {
			for _, needle := range m.needles {
				if strings.Contains(item, needle) {
					return true
				}
			}
		}
		return false
	case []byte:
		for _, needle := range m.needles {
			if bytes.Contains(v, []byte(needle)) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// =============================================================================
// Regex Matcher
// =============================================================================

// Regex compiles a regular expression once at construction time.
// It is case-sensitive by default, and supports (?i) for case-insensitivity.
func Regex(pattern string) (Matcher, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern %q: %w", pattern, err)
	}
	return &regexMatcher{pattern: pattern, re: re}, nil
}

// MustRegex constructs a Regex Matcher or panics if compilation fails.
func MustRegex(pattern string) Matcher {
	m, err := Regex(pattern)
	if err != nil {
		panic(err)
	}
	return m
}

type regexMatcher struct {
	pattern string
	re      *regexp.Regexp
}

func (m *regexMatcher) Match(val any) bool {
	if val == nil || m.re == nil {
		return false
	}
	switch v := val.(type) {
	case string:
		return m.re.MatchString(v)
	case []byte:
		return m.re.Match(v)
	default:
		return false
	}
}

// =============================================================================
// RegexExtractIn Matcher
// =============================================================================

// RegexExtractIn compiles a regex once, extracts capture group 1, and checks
// if the extracted substring exists within the provided set.
func RegexExtractIn(pattern string, set []string) (Matcher, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex extract pattern %q: %w", pattern, err)
	}
	setMap := make(map[string]struct{}, len(set))
	for _, s := range set {
		setMap[s] = struct{}{}
	}
	return &regexExtractInMatcher{pattern: pattern, re: re, set: setMap}, nil
}

// MustRegexExtractIn constructs a RegexExtractIn Matcher or panics on failure.
func MustRegexExtractIn(pattern string, set []string) Matcher {
	m, err := RegexExtractIn(pattern, set)
	if err != nil {
		panic(err)
	}
	return m
}

type regexExtractInMatcher struct {
	pattern string
	re      *regexp.Regexp
	set     map[string]struct{}
}

func (m *regexExtractInMatcher) Match(val any) bool {
	if val == nil || m.re == nil {
		return false
	}
	var extracted string
	switch v := val.(type) {
	case string:
		matches := m.re.FindStringSubmatch(v)
		if len(matches) <= 1 {
			return false
		}
		extracted = matches[1]
	case []byte:
		matches := m.re.FindSubmatch(v)
		if len(matches) <= 1 {
			return false
		}
		extracted = string(matches[1])
	default:
		return false
	}

	_, found := m.set[extracted]
	return found
}

// =============================================================================
// InRange Matcher
// =============================================================================

// InRange returns a Matcher that checks if a numeric signal falls inclusively
// within [min, max]. Panics if min > max.
func InRange(min, max int) Matcher {
	if min > max {
		panic(fmt.Sprintf("ruleengine: InRange min (%d) cannot be greater than max (%d)", min, max))
	}
	return &inRangeMatcher{min: min, max: max}
}

type inRangeMatcher struct {
	min int
	max int
}

func (m *inRangeMatcher) Match(val any) bool {
	if val == nil {
		return false
	}
	num, ok := toFloat64(val)
	if !ok {
		return false
	}
	return num >= float64(m.min) && num <= float64(m.max)
}

// =============================================================================
// JSON Unmarshaling for Matcher
// =============================================================================

var validOperatorKeys = map[string]struct{}{
	"equals":           {},
	"contains_any":     {},
	"regex":            {},
	"regex_extract_in": {},
	"in_range":         {},
}

// UnmarshalMatcher unmarshals a JSON match block, strictly enforcing that
// exactly one valid operator key is present. Zero, multiple, or unknown keys are rejected.
func UnmarshalMatcher(raw json.RawMessage) (Matcher, error) {
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		return nil, fmt.Errorf("invalid match object: %w", err)
	}

	if len(rawMap) == 0 {
		return nil, fmt.Errorf("match block must contain exactly one operator key, found 0")
	}
	if len(rawMap) > 1 {
		keys := make([]string, 0, len(rawMap))
		for k := range rawMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("match block must contain exactly one operator key, found %d: %v", len(rawMap), keys)
	}

	var opKey string
	var opVal json.RawMessage
	for k, v := range rawMap {
		opKey = k
		opVal = v
	}

	if _, valid := validOperatorKeys[opKey]; !valid {
		return nil, fmt.Errorf("unknown match operator key %q", opKey)
	}

	switch opKey {
	case "equals":
		var exp any
		if err := json.Unmarshal(opVal, &exp); err != nil {
			return nil, fmt.Errorf("invalid equals value: %w", err)
		}
		return Equals(exp), nil

	case "contains_any":
		var needles []string
		if err := json.Unmarshal(opVal, &needles); err != nil {
			return nil, fmt.Errorf("invalid contains_any value (expected array of strings): %w", err)
		}
		if len(needles) == 0 {
			return nil, fmt.Errorf("match operator \"contains_any\" array cannot be empty")
		}
		for i, rawNeedle := range needles {
			norm := textutil.NormalizeText(rawNeedle)
			if norm == "" {
				return nil, fmt.Errorf("match operator \"contains_any\" needle[%d] %q is empty after normalization", i, rawNeedle)
			}
		}
		return ContainsAny(needles...), nil

	case "regex":
		var pattern string
		if err := json.Unmarshal(opVal, &pattern); err != nil {
			return nil, fmt.Errorf("invalid regex value (expected string): %w", err)
		}
		return Regex(pattern)

	case "regex_extract_in":
		var spec struct {
			Pattern string   `json:"pattern"`
			Set     []string `json:"set"`
		}
		if err := json.Unmarshal(opVal, &spec); err != nil {
			return nil, fmt.Errorf("invalid regex_extract_in object: %w", err)
		}
		return RegexExtractIn(spec.Pattern, spec.Set)

	case "in_range":
		// Support both [min, max] slice and {"min": ..., "max": ...} object
		var slice []int
		if err := json.Unmarshal(opVal, &slice); err == nil {
			if len(slice) != 2 {
				return nil, fmt.Errorf("in_range array must have exactly 2 elements [min, max]")
			}
			if slice[0] > slice[1] {
				return nil, fmt.Errorf("match operator \"in_range\" min (%d) cannot be greater than max (%d)", slice[0], slice[1])
			}
			return InRange(slice[0], slice[1]), nil
		}
		var obj struct {
			Min int `json:"min"`
			Max int `json:"max"`
		}
		if err := json.Unmarshal(opVal, &obj); err != nil {
			return nil, fmt.Errorf("invalid in_range value (expected [min, max] or {\"min\": int, \"max\": int}): %w", err)
		}
		if obj.Min > obj.Max {
			return nil, fmt.Errorf("match operator \"in_range\" min (%d) cannot be greater than max (%d)", obj.Min, obj.Max)
		}
		return InRange(obj.Min, obj.Max), nil

	default:
		return nil, fmt.Errorf("unknown match operator key %q", opKey)
	}
}
