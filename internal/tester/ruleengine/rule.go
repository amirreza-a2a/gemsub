package ruleengine

import (
	"encoding/json"
	"fmt"
	"strings"

	"gemsub/internal/store"
)

// Verdict models the classification outcome emitted when a Rule condition matches.
type Verdict struct {
	Status   store.Status        `json:"status"`
	Category store.ErrorCategory `json:"category,omitempty"`
	Reason   string              `json:"reason,omitempty"`
}

var validVerdictKeys = map[string]struct{}{
	"status":   {},
	"category": {},
	"reason":   {},
}

var validStatuses = map[store.Status]struct{}{
	store.StatusPassed:       {},
	store.StatusFailed:       {},
	store.StatusInconclusive: {},
}

var validCategories = map[store.ErrorCategory]struct{}{
	store.ErrNone:              {},
	store.ErrRegionBlocked:     {},
	store.ErrTargetDenied:      {},
	store.ErrTargetRateLimited: {},
	store.ErrProxyRateLimited:  {},
	store.ErrTargetError:       {},
	store.ErrTargetOther:       {},
	store.ErrConnRefused:       {},
	store.ErrTimeout:           {},
	store.ErrReset:             {},
	store.ErrTLS:               {},
	store.ErrReality:           {},
	store.ErrProxyError:        {},
	store.ErrConfig:            {},
}

// UnmarshalJSON strictly validates Verdict fields, rejecting any unknown fields
// and ensuring status and category match canonical store definitions.
func (v *Verdict) UnmarshalJSON(data []byte) error {
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err != nil {
		return fmt.Errorf("invalid verdict object: %w", err)
	}

	for k := range rawMap {
		if _, ok := validVerdictKeys[k]; !ok {
			return fmt.Errorf("unknown field %q in verdict", k)
		}
	}

	statusRaw, ok := rawMap["status"]
	if !ok {
		return fmt.Errorf("verdict missing required \"status\" field")
	}
	var stStr string
	if err := json.Unmarshal(statusRaw, &stStr); err != nil {
		return fmt.Errorf("invalid status string: %w", err)
	}
	st := store.Status(stStr)
	if _, ok := validStatuses[st]; !ok {
		return fmt.Errorf("invalid verdict status %q: must be one of \"passed\", \"failed\", \"inconclusive\"", stStr)
	}
	v.Status = st

	if catRaw, ok := rawMap["category"]; ok {
		var catStr string
		if err := json.Unmarshal(catRaw, &catStr); err != nil {
			return fmt.Errorf("invalid category string: %w", err)
		}
		cat := store.ErrorCategory(catStr)
		if _, ok := validCategories[cat]; !ok {
			return fmt.Errorf("invalid verdict category %q: must be a valid store.ErrorCategory", catStr)
		}
		v.Category = cat
	}

	if reasonRaw, ok := rawMap["reason"]; ok {
		var r string
		if err := json.Unmarshal(reasonRaw, &r); err != nil {
			return fmt.Errorf("invalid reason string: %w", err)
		}
		v.Reason = r
	}

	return nil
}

// Rule defines an evaluation rule mapping a condition predicate to a verdict outcome.
type Rule struct {
	Phase     string    `json:"phase,omitempty"`
	Condition Condition `json:"condition,omitempty"`
	Verdict   Verdict   `json:"verdict"`
}

var validRuleKeys = map[string]struct{}{
	"phase":     {},
	"verdict":   {},
	"condition": {},
	"signal":    {},
	"header":    {},
	"match":     {},
}

// UnmarshalJSON strictly deserializes a Rule from JSON, accepting either
// a composite condition block or single-signal shorthand syntax, while
// strictly rejecting unknown fields and ambiguous specifications.
func (r *Rule) UnmarshalJSON(data []byte) error {
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err != nil {
		return fmt.Errorf("invalid rule object: %w", err)
	}

	for k := range rawMap {
		if _, ok := validRuleKeys[k]; !ok {
			return fmt.Errorf("unknown field %q in rule", k)
		}
	}

	hasCondition := false
	if _, ok := rawMap["condition"]; ok {
		hasCondition = true
	}

	hasHeader := false
	if _, ok := rawMap["header"]; ok {
		hasHeader = true
	}

	hasSignal := false
	if _, ok := rawMap["signal"]; ok {
		hasSignal = true
	}

	hasMatch := false
	if _, ok := rawMap["match"]; ok {
		hasMatch = true
	}

	if hasCondition {
		if hasHeader {
			return fmt.Errorf("cannot specify \"header\" when \"condition\" is present")
		}
		if hasSignal {
			return fmt.Errorf("cannot specify both condition and shorthand signal")
		}
		if hasMatch {
			return fmt.Errorf("cannot specify both condition and shorthand match")
		}

		cond, err := UnmarshalCondition(rawMap["condition"])
		if err != nil {
			return err
		}
		r.Condition = cond
	} else {
		// Shorthand path requires both signal and match
		if !hasSignal && !hasMatch {
			return fmt.Errorf("must specify either shorthand signal/match or condition")
		}
		if !hasSignal {
			return fmt.Errorf("shorthand rule requires \"signal\" field")
		}
		if !hasMatch {
			return fmt.Errorf("shorthand rule requires \"match\" field")
		}

		leafObj := map[string]json.RawMessage{
			"signal": rawMap["signal"],
			"match":  rawMap["match"],
		}
		if hasHeader {
			leafObj["header"] = rawMap["header"]
		}
		leafBytes, err := json.Marshal(leafObj)
		if err != nil {
			return err
		}
		cond, err := UnmarshalCondition(leafBytes)
		if err != nil {
			return err
		}
		r.Condition = cond
	}

	verdictRaw, ok := rawMap["verdict"]
	if !ok {
		return fmt.Errorf("rule missing required \"verdict\" block")
	}
	if err := json.Unmarshal(verdictRaw, &r.Verdict); err != nil {
		return err
	}

	if phaseRaw, ok := rawMap["phase"]; ok {
		var phaseStr string
		if err := json.Unmarshal(phaseRaw, &phaseStr); err != nil {
			return fmt.Errorf("invalid phase string: %w", err)
		}
		r.Phase = strings.TrimSpace(phaseStr)
	}

	return nil
}
