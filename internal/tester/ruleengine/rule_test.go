package ruleengine_test

import (
	"encoding/json"
	"strings"
	"testing"

	"gemsub/internal/store"
	"gemsub/internal/tester/ruleengine"
)

func TestRule_UnmarshalJSON_ValidShorthand(t *testing.T) {
	rawJSON := `{
		"phase": "rejection",
		"signal": "http_status",
		"match": { "equals": 403 },
		"verdict": {
			"status": "failed",
			"category": "target_other",
			"reason": "forbidden"
		}
	}`

	var rule ruleengine.Rule
	if err := json.Unmarshal([]byte(rawJSON), &rule); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	if rule.Phase != "rejection" {
		t.Fatalf("expected phase 'rejection', got %q", rule.Phase)
	}
	if rule.Verdict.Status != store.StatusFailed {
		t.Fatalf("expected StatusFailed, got %v", rule.Verdict.Status)
	}
	if rule.Verdict.Category != store.ErrTargetOther {
		t.Fatalf("expected ErrTargetOther, got %v", rule.Verdict.Category)
	}
	if rule.Verdict.Reason != "forbidden" {
		t.Fatalf("expected reason 'forbidden', got %q", rule.Verdict.Reason)
	}

	// Evaluate condition
	ctx403 := &ruleengine.EvaluationContext{StatusCode: 403}
	if !rule.Condition.Evaluate(ctx403) {
		t.Fatalf("expected shorthand rule condition to match status 403")
	}

	ctx200 := &ruleengine.EvaluationContext{StatusCode: 200}
	if rule.Condition.Evaluate(ctx200) {
		t.Fatalf("expected shorthand rule condition not to match status 200")
	}
}

func TestRule_UnmarshalJSON_ValidHeaderShorthand(t *testing.T) {
	rawJSON := `{
		"signal": "header",
		"header": "Server",
		"match": { "contains_any": ["ESF", "GSE"] },
		"verdict": { "status": "passed" }
	}`

	var rule ruleengine.Rule
	if err := json.Unmarshal([]byte(rawJSON), &rule); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	if rule.Verdict.Status != store.StatusPassed {
		t.Fatalf("expected status passed, got %v", rule.Verdict.Status)
	}
}

func TestRule_UnmarshalJSON_ValidCompositeCondition(t *testing.T) {
	rawJSON := `{
		"phase": "verification",
		"condition": {
			"all_of": [
				{
					"signal": "http_status",
					"match": { "equals": 200 }
				},
				{
					"signal": "title",
					"match": { "contains_any": ["gemini"] }
				}
			]
		},
		"verdict": {
			"status": "passed",
			"reason": "verified"
		}
	}`

	var rule ruleengine.Rule
	if err := json.Unmarshal([]byte(rawJSON), &rule); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	ctxMatch := &ruleengine.EvaluationContext{
		StatusCode: 200,
		Title:      "Google Gemini",
	}
	if !rule.Condition.Evaluate(ctxMatch) {
		t.Fatalf("expected composite condition to match ctxMatch")
	}

	ctxMismatch := &ruleengine.EvaluationContext{
		StatusCode: 200,
		Title:      "Other Service",
	}
	if rule.Condition.Evaluate(ctxMismatch) {
		t.Fatalf("expected composite condition not to match ctxMismatch")
	}
}

func TestRule_UnmarshalJSON_RegexOperator(t *testing.T) {
	rawJSON := `{
		"signal": "final_url_path",
		"match": { "regex": "^/app-(?:unavailable|blocked)" },
		"verdict": { "status": "failed", "category": "region_blocked" }
	}`

	var rule ruleengine.Rule
	if err := json.Unmarshal([]byte(rawJSON), &rule); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
}

func TestRule_UnmarshalJSON_StrictValidation(t *testing.T) {
	tests := []struct {
		name         string
		jsonInput    string
		errSubstring string
	}{
		{
			name: "Zero operator keys in match block",
			jsonInput: `{
				"signal": "http_status",
				"match": {},
				"verdict": { "status": "passed" }
			}`,
			errSubstring: "must contain exactly one operator key",
		},
		{
			name: "Multiple operator keys in match block",
			jsonInput: `{
				"signal": "http_status",
				"match": { "equals": 200, "in_range": [200, 299] },
				"verdict": { "status": "passed" }
			}`,
			errSubstring: "must contain exactly one operator key",
		},
		{
			name: "Unknown operator key (regex_match instead of regex)",
			jsonInput: `{
				"signal": "http_status",
				"match": { "regex_match": ".*" },
				"verdict": { "status": "passed" }
			}`,
			errSubstring: "unknown match operator key",
		},
		{
			name: "Unknown field in rule",
			jsonInput: `{
				"signal": "http_status",
				"match": { "equals": 200 },
				"verdict": { "status": "passed" },
				"unknown_field": 123
			}`,
			errSubstring: "unknown field \"unknown_field\" in rule",
		},
		{
			name: "Unknown field in condition",
			jsonInput: `{
				"condition": {
					"all_of": [{"signal": "http_status", "match": {"equals": 200}}],
					"unexpected_attr": "value"
				},
				"verdict": { "status": "passed" }
			}`,
			errSubstring: "unknown field \"unexpected_attr\" in condition",
		},
		{
			name: "Condition combined with header",
			jsonInput: `{
				"condition": { "all_of": [{"signal": "http_status", "match": {"equals": 200}}] },
				"header": "Server",
				"verdict": { "status": "passed" }
			}`,
			errSubstring: "cannot specify \"header\" when \"condition\" is present",
		},
		{
			name: "Both shorthand signal and condition specified",
			jsonInput: `{
				"signal": "http_status",
				"match": { "equals": 200 },
				"condition": { "all_of": [{"signal": "http_status", "match": {"equals": 200}}] },
				"verdict": { "status": "passed" }
			}`,
			errSubstring: "cannot specify both condition and shorthand signal",
		},
		{
			name: "Missing both shorthand signal and condition",
			jsonInput: `{
				"verdict": { "status": "passed" }
			}`,
			errSubstring: "must specify either shorthand signal/match or condition",
		},
		{
			name: "Empty all_of array rejected",
			jsonInput: `{
				"condition": { "all_of": [] },
				"verdict": { "status": "passed" }
			}`,
			errSubstring: "condition \"all_of\" array cannot be empty",
		},
		{
			name: "Empty any_of array rejected",
			jsonInput: `{
				"condition": { "any_of": [] },
				"verdict": { "status": "passed" }
			}`,
			errSubstring: "condition \"any_of\" array cannot be empty",
		},
		{
			name: "Empty contains_any array rejected",
			jsonInput: `{
				"signal": "http_status",
				"match": { "contains_any": [] },
				"verdict": { "status": "passed" }
			}`,
			errSubstring: "match operator \"contains_any\" array cannot be empty",
		},
		{
			name: "Contains_any needle empty after normalization rejected",
			jsonInput: `{
				"signal": "title",
				"match": { "contains_any": ["   "] },
				"verdict": { "status": "passed" }
			}`,
			errSubstring: "is empty after normalization",
		},
		{
			name: "In_range min > max slice rejected",
			jsonInput: `{
				"signal": "http_status",
				"match": { "in_range": [300, 200] },
				"verdict": { "status": "passed" }
			}`,
			errSubstring: "min (300) cannot be greater than max (200)",
		},
		{
			name: "In_range min > max object rejected",
			jsonInput: `{
				"signal": "http_status",
				"match": { "in_range": {"min": 300, "max": 200} },
				"verdict": { "status": "passed" }
			}`,
			errSubstring: "min (300) cannot be greater than max (200)",
		},
		{
			name: "Invalid verdict status typo rejected",
			jsonInput: `{
				"signal": "http_status",
				"match": { "equals": 200 },
				"verdict": { "status": "passed_typo" }
			}`,
			errSubstring: "invalid verdict status \"passed_typo\"",
		},
		{
			name: "Invalid verdict category typo rejected",
			jsonInput: `{
				"signal": "http_status",
				"match": { "equals": 200 },
				"verdict": { "status": "failed", "category": "region_block" }
			}`,
			errSubstring: "invalid verdict category \"region_block\"",
		},
		{
			name: "Unknown field in verdict",
			jsonInput: `{
				"signal": "http_status",
				"match": { "equals": 200 },
				"verdict": { "status": "passed", "extra": "forbidden" }
			}`,
			errSubstring: "unknown field \"extra\" in verdict",
		},
		{
			name: "Invalid signal kind",
			jsonInput: `{
				"signal": "non_existent_signal",
				"match": { "equals": 200 },
				"verdict": { "status": "passed" }
			}`,
			errSubstring: "unknown signal kind",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rule ruleengine.Rule
			err := json.Unmarshal([]byte(tt.jsonInput), &rule)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.errSubstring)
			}
			if !strings.Contains(err.Error(), tt.errSubstring) {
				t.Fatalf("expected error containing %q, got %q", tt.errSubstring, err.Error())
			}
		})
	}
}

func TestCondition_UnmarshalJSON_MultipleCombinatorsDeterministic(t *testing.T) {
	rawJSON := `{
		"all_of": [{"signal": "http_status", "match": {"equals": 200}}],
		"any_of": [{"signal": "http_status", "match": {"equals": 200}}]
	}`

	_, err := ruleengine.UnmarshalCondition([]byte(rawJSON))
	if err == nil {
		t.Fatalf("expected error for multiple combinators, got nil")
	}

	expectedMsg := "condition cannot specify multiple combinators: all_of, any_of"
	if err.Error() != expectedMsg {
		t.Fatalf("expected exact error message %q, got %q", expectedMsg, err.Error())
	}
}
