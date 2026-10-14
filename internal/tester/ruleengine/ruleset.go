package ruleengine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"gemsub/internal/store"
)

// VerificationMode dictates how Phase 5 handles target verification.
type VerificationMode string

const (
	// ModeStrict requires at least one positive verification rule to match.
	// If none match, a verification failure verdict is emitted.
	ModeStrict VerificationMode = "strict"

	// ModePermissive treats any response passing negative rejection checks
	// with an accepted HTTP status as passed.
	ModePermissive VerificationMode = "permissive"
)

// DefaultStrictFallbackReason is the canonical fallback reason emitted in strict mode
// when no positive verification rule matches.
const DefaultStrictFallbackReason = "possible interception / false positive: positive verification signals absent"

// RuleSet defines a declarative target classification specification.
type RuleSet struct {
	Name                     string           `json:"name"`
	Mode                     VerificationMode `json:"mode"`
	AcceptedStatusCodes      []int            `json:"accepted_status_codes,omitempty"`
	StrictModeFallbackReason string           `json:"strict_mode_fallback_reason,omitempty"`
	Rules                    []Rule           `json:"rules,omitempty"`

	// Partitioned buckets
	Interceptions []Rule `json:"-"`
	Rejections    []Rule `json:"-"`
	Verifications []Rule `json:"-"`

	initialized bool
}

// Initialized reports whether the RuleSet has been properly validated and partitioned.
func (rs *RuleSet) Initialized() bool {
	return rs != nil && rs.initialized
}

// RuleSetConfig configures a new RuleSet instance.
type RuleSetConfig struct {
	Name                     string
	Mode                     VerificationMode
	AcceptedStatusCodes      []int
	StrictModeFallbackReason string
	Rules                    []Rule
}

// NewRuleSet constructs, validates, and partitions a RuleSet.
func NewRuleSet(cfg RuleSetConfig) (*RuleSet, error) {
	rs := &RuleSet{
		Name:                     cfg.Name,
		Mode:                     cfg.Mode,
		AcceptedStatusCodes:      cfg.AcceptedStatusCodes,
		StrictModeFallbackReason: cfg.StrictModeFallbackReason,
		Rules:                    cfg.Rules,
	}
	if err := rs.initAndValidate(); err != nil {
		return nil, err
	}
	return rs, nil
}

// EffectiveStrictFallbackReason returns the configured strict mode fallback reason,
// or DefaultStrictFallbackReason if unset.
func (rs *RuleSet) EffectiveStrictFallbackReason() string {
	if rs != nil && rs.StrictModeFallbackReason != "" {
		return rs.StrictModeFallbackReason
	}
	return DefaultStrictFallbackReason
}

// Validate validates and partitions the RuleSet if not already partitioned.
func (rs *RuleSet) Validate() error {
	return rs.initAndValidate()
}

func (rs *RuleSet) initAndValidate() error {
	rs.initialized = false

	if rs.Mode == "" {
		rs.Mode = ModeStrict
	}
	if rs.Mode != ModeStrict && rs.Mode != ModePermissive {
		return fmt.Errorf("invalid verification mode %q: must be %q or %q", rs.Mode, ModeStrict, ModePermissive)
	}

	if len(rs.AcceptedStatusCodes) == 0 {
		rs.AcceptedStatusCodes = []int{http.StatusOK}
	}

	rs.Interceptions = nil
	rs.Rejections = nil
	rs.Verifications = nil

	for i, r := range rs.Rules {
		// 1. Validate Verdict
		if r.Verdict.Status == store.StatusPassed {
			if r.Verdict.Category != store.ErrNone && r.Verdict.Category != "" {
				return fmt.Errorf("rule %d has passed status with non-none category %q", i, r.Verdict.Category)
			}
			r.Verdict.Category = store.ErrNone
		} else if r.Verdict.Status == store.StatusFailed || r.Verdict.Status == store.StatusInconclusive {
			if r.Verdict.Category == "" || r.Verdict.Category == store.ErrNone {
				return fmt.Errorf("rule %d with status %q requires a non-empty error category", i, r.Verdict.Status)
			}
		} else {
			return fmt.Errorf("rule %d has invalid verdict status %q", i, r.Verdict.Status)
		}

		// 2. Validate Condition
		if err := validateCondition(r.Condition); err != nil {
			return fmt.Errorf("rule %d: %w", i, err)
		}

		// 3. Infer or Validate Phase & Partition
		phase := strings.TrimSpace(strings.ToLower(r.Phase))
		if phase == "" {
			if r.Verdict.Status == store.StatusPassed {
				phase = "verification"
			} else {
				phase = "rejection"
			}
		}

		switch phase {
		case "interception":
			if r.Verdict.Status == store.StatusPassed {
				return fmt.Errorf("rule %d contradiction: passed verdict cannot be in interception phase", i)
			}
			r.Phase = "interception"
			rs.Interceptions = append(rs.Interceptions, r)
		case "rejection":
			if r.Verdict.Status == store.StatusPassed {
				return fmt.Errorf("rule %d contradiction: passed verdict cannot be in rejection phase", i)
			}
			r.Phase = "rejection"
			rs.Rejections = append(rs.Rejections, r)
		case "verification":
			if r.Verdict.Status == store.StatusFailed || r.Verdict.Status == store.StatusInconclusive {
				return fmt.Errorf("rule %d contradiction: %s verdict cannot be in verification phase", i, r.Verdict.Status)
			}
			r.Phase = "verification"
			rs.Verifications = append(rs.Verifications, r)
		default:
			return fmt.Errorf("rule %d has unknown phase %q: must be \"interception\", \"rejection\", or \"verification\"", i, phase)
		}
	}

	rs.initialized = true
	return nil
}

func validateCondition(c Condition) error {
	if c == nil {
		return nil
	}
	switch cond := c.(type) {
	case *LeafCondition:
		if cond.Signal != nil && cond.Signal.Kind() == SignalRawBody {
			if _, isContains := cond.Matcher.(*containsAnyMatcher); isContains {
				return fmt.Errorf("rule uses ContainsAny with signal %q: ContainsAny expects normalized text; use %q or regex instead", SignalRawBody, SignalNormalizedBody)
			}
		}
		return nil
	case *allOfCondition:
		for _, sub := range cond.conditions {
			if err := validateCondition(sub); err != nil {
				return err
			}
		}
		return nil
	case *anyOfCondition:
		for _, sub := range cond.conditions {
			if err := validateCondition(sub); err != nil {
				return err
			}
		}
		return nil
	case *notCondition:
		return validateCondition(cond.condition)
	default:
		return nil
	}
}

var validRuleSetKeys = map[string]struct{}{
	"name":                        {},
	"mode":                        {},
	"accepted_status_codes":       {},
	"strict_mode_fallback_reason": {},
	"rules":                       {},
}

// UnmarshalJSON strictly deserializes a RuleSet, verifying keys and validating rules.
func (rs *RuleSet) UnmarshalJSON(data []byte) error {
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err != nil {
		return fmt.Errorf("invalid ruleset object: %w", err)
	}

	for k := range rawMap {
		if _, ok := validRuleSetKeys[k]; !ok {
			return fmt.Errorf("unknown field %q in ruleset", k)
		}
	}

	if nameRaw, ok := rawMap["name"]; ok {
		if err := json.Unmarshal(nameRaw, &rs.Name); err != nil {
			return fmt.Errorf("invalid ruleset name: %w", err)
		}
	}

	if modeRaw, ok := rawMap["mode"]; ok {
		if err := json.Unmarshal(modeRaw, &rs.Mode); err != nil {
			return fmt.Errorf("invalid ruleset mode: %w", err)
		}
	}

	if ascRaw, ok := rawMap["accepted_status_codes"]; ok {
		if err := json.Unmarshal(ascRaw, &rs.AcceptedStatusCodes); err != nil {
			return fmt.Errorf("invalid accepted_status_codes: %w", err)
		}
	}

	if reasonRaw, ok := rawMap["strict_mode_fallback_reason"]; ok {
		if err := json.Unmarshal(reasonRaw, &rs.StrictModeFallbackReason); err != nil {
			return fmt.Errorf("invalid strict_mode_fallback_reason: %w", err)
		}
	}

	if rulesRaw, ok := rawMap["rules"]; ok {
		if err := json.Unmarshal(rulesRaw, &rs.Rules); err != nil {
			return fmt.Errorf("invalid rules array: %w", err)
		}
	}

	return rs.initAndValidate()
}
