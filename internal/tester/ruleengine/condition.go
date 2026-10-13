package ruleengine

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Condition evaluates whether a set of predicates over an EvaluationContext is satisfied.
type Condition interface {
	Evaluate(ctx *EvaluationContext) bool
}

// =============================================================================
// Leaf Condition
// =============================================================================

// LeafCondition pairs a Signal extractor with a Matcher predicate.
type LeafCondition struct {
	Signal  Signal
	Matcher Matcher
}

// NewLeafCondition constructs a LeafCondition.
func NewLeafCondition(sig Signal, m Matcher) *LeafCondition {
	return &LeafCondition{
		Signal:  sig,
		Matcher: m,
	}
}

func (c *LeafCondition) Evaluate(ctx *EvaluationContext) bool {
	if c == nil || c.Signal == nil || c.Matcher == nil {
		return false
	}
	val := c.Signal.Extract(ctx)
	return c.Matcher.Match(val)
}

// =============================================================================
// Composite Combinators
// =============================================================================

// AllOf returns a Condition that performs logical AND across its sub-conditions.
// Panics if conditions is empty or if any condition is nil.
func AllOf(conditions ...Condition) Condition {
	if len(conditions) == 0 {
		panic("ruleengine: AllOf requires at least 1 condition")
	}
	for i, c := range conditions {
		if c == nil {
			panic(fmt.Sprintf("ruleengine: AllOf condition at index %d is nil", i))
		}
	}
	return &allOfCondition{conditions: conditions}
}

type allOfCondition struct {
	conditions []Condition
}

func (c *allOfCondition) Evaluate(ctx *EvaluationContext) bool {
	if len(c.conditions) == 0 {
		return false
	}
	for _, cond := range c.conditions {
		if cond == nil || !cond.Evaluate(ctx) {
			return false
		}
	}
	return true
}

// AnyOf returns a Condition that performs logical OR across its sub-conditions.
// Panics if conditions is empty or if any condition is nil.
func AnyOf(conditions ...Condition) Condition {
	if len(conditions) == 0 {
		panic("ruleengine: AnyOf requires at least 1 condition")
	}
	for i, c := range conditions {
		if c == nil {
			panic(fmt.Sprintf("ruleengine: AnyOf condition at index %d is nil", i))
		}
	}
	return &anyOfCondition{conditions: conditions}
}

type anyOfCondition struct {
	conditions []Condition
}

func (c *anyOfCondition) Evaluate(ctx *EvaluationContext) bool {
	if len(c.conditions) == 0 {
		return false
	}
	for _, cond := range c.conditions {
		if cond != nil && cond.Evaluate(ctx) {
			return true
		}
	}
	return false
}

// Not returns a Condition that inverts the outcome of its inner condition.
// Panics if condition is nil.
func Not(condition Condition) Condition {
	if condition == nil {
		panic("ruleengine: Not condition cannot be nil")
	}
	return &notCondition{condition: condition}
}

type notCondition struct {
	condition Condition
}

func (c *notCondition) Evaluate(ctx *EvaluationContext) bool {
	if c.condition == nil {
		return false
	}
	return !c.condition.Evaluate(ctx)
}

// =============================================================================
// JSON Unmarshaling for Condition
// =============================================================================

var validConditionCombinatorKeys = map[string]struct{}{
	"all_of": {},
	"any_of": {},
	"not":    {},
}

var validLeafConditionKeys = map[string]struct{}{
	"signal": {},
	"header": {},
	"match":  {},
}

// UnmarshalCondition parses a JSON object into a Condition, strictly validating keys.
func UnmarshalCondition(raw json.RawMessage) (Condition, error) {
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		return nil, fmt.Errorf("invalid condition object: %w", err)
	}

	if len(rawMap) == 0 {
		return nil, fmt.Errorf("condition object cannot be empty")
	}

	// Check if this is a composite combinator (all_of, any_of, not) deterministically
	var foundCombinators []string
	for _, k := range []string{"all_of", "any_of", "not"} {
		if _, ok := rawMap[k]; ok {
			foundCombinators = append(foundCombinators, k)
		}
	}

	if len(foundCombinators) > 1 {
		return nil, fmt.Errorf("condition cannot specify multiple combinators: %s", strings.Join(foundCombinators, ", "))
	}

	if len(foundCombinators) == 1 {
		combinatorKey := foundCombinators[0]
		// Strict validation: NO other keys are allowed alongside the combinator key
		if len(rawMap) > 1 {
			otherKeys := make([]string, 0, len(rawMap)-1)
			for k := range rawMap {
				if k != combinatorKey {
					otherKeys = append(otherKeys, k)
				}
			}
			sort.Strings(otherKeys)
			return nil, fmt.Errorf("unknown field %q in condition (cannot combine with %q)", otherKeys[0], combinatorKey)
		}

		switch combinatorKey {
		case "all_of":
			var subRaws []json.RawMessage
			if err := json.Unmarshal(rawMap["all_of"], &subRaws); err != nil {
				return nil, fmt.Errorf("invalid all_of array: %w", err)
			}
			if len(subRaws) == 0 {
				return nil, fmt.Errorf("condition \"all_of\" array cannot be empty")
			}
			subConds := make([]Condition, 0, len(subRaws))
			for i, subRaw := range subRaws {
				subCond, err := UnmarshalCondition(subRaw)
				if err != nil {
					return nil, fmt.Errorf("invalid all_of[%d]: %w", i, err)
				}
				subConds = append(subConds, subCond)
			}
			return AllOf(subConds...), nil

		case "any_of":
			var subRaws []json.RawMessage
			if err := json.Unmarshal(rawMap["any_of"], &subRaws); err != nil {
				return nil, fmt.Errorf("invalid any_of array: %w", err)
			}
			if len(subRaws) == 0 {
				return nil, fmt.Errorf("condition \"any_of\" array cannot be empty")
			}
			subConds := make([]Condition, 0, len(subRaws))
			for i, subRaw := range subRaws {
				subCond, err := UnmarshalCondition(subRaw)
				if err != nil {
					return nil, fmt.Errorf("invalid any_of[%d]: %w", i, err)
				}
				subConds = append(subConds, subCond)
			}
			return AnyOf(subConds...), nil

		case "not":
			subCond, err := UnmarshalCondition(rawMap["not"])
			if err != nil {
				return nil, fmt.Errorf("invalid not condition: %w", err)
			}
			return Not(subCond), nil
		}
	}

	// If not a combinator, it must be a leaf condition {"signal": ..., "match": ...}
	if _, hasSignal := rawMap["signal"]; !hasSignal {
		// Unknown fields
		keys := make([]string, 0, len(rawMap))
		for k := range rawMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("unknown field %q in condition", keys[0])
	}

	// Validate allowed leaf keys
	for k := range rawMap {
		if _, ok := validLeafConditionKeys[k]; !ok {
			return nil, fmt.Errorf("unknown field %q in condition", k)
		}
	}

	var sigKindStr string
	if err := json.Unmarshal(rawMap["signal"], &sigKindStr); err != nil {
		return nil, fmt.Errorf("invalid signal string: %w", err)
	}

	var headerName string
	if headerRaw, hasHeader := rawMap["header"]; hasHeader {
		if err := json.Unmarshal(headerRaw, &headerName); err != nil {
			return nil, fmt.Errorf("invalid header string: %w", err)
		}
	}

	sig, err := ParseSignal(SignalKind(sigKindStr), headerName)
	if err != nil {
		return nil, err
	}

	matchRaw, hasMatch := rawMap["match"]
	if !hasMatch {
		return nil, fmt.Errorf("condition missing required \"match\" block")
	}

	matcher, err := UnmarshalMatcher(matchRaw)
	if err != nil {
		return nil, err
	}

	return NewLeafCondition(sig, matcher), nil
}
