package ruleengine

import (
	"fmt"

	"gemsub/internal/store"
	"gemsub/internal/tester/classifiercore"
)

// Evaluate executes an immutable EvaluationContext against a RuleSet specification
// through the 5 canonical pipeline phases:
//
//	Phase 1: Common Status Gate (429, 503, 500, 502, 504, 403)
//	Phase 2: Accepted Status Gate (default [200]; skips payload inspection on mismatch)
//	Phase 3: Interception Preemption (canonical challenge phrases + custom interception rules)
//	Phase 4: Negative Rejections (regional or domain restriction rules)
//	Phase 5: Positive Verifications & Fallback (strict verification vs permissive pass)
//
// All emitted Result outcomes have their Latency clamped via classifiercore.ClampLatency.
func Evaluate(ctx *EvaluationContext, spec *RuleSet) Result {
	if ctx == nil {
		return Result{
			Status:    store.StatusFailed,
			Category:  store.ErrTargetOther,
			Reason:    "nil evaluation context",
			Latency:   classifiercore.ClampLatency(0),
			Retryable: false,
		}
	}

	if spec == nil {
		return Result{
			Status:     store.StatusFailed,
			Category:   store.ErrTargetOther,
			StatusCode: ctx.StatusCode,
			Reason:     "nil ruleset specification",
			Latency:    classifiercore.ClampLatency(ctx.MeasuredLatency),
			Retryable:  false,
		}
	}

	// Auto-partition if created directly as a struct literal without NewRuleSet
	if len(spec.Interceptions) == 0 && len(spec.Rejections) == 0 && len(spec.Verifications) == 0 && len(spec.Rules) > 0 {
		_ = spec.initAndValidate()
	}

	clampedLatency := classifiercore.ClampLatency(ctx.MeasuredLatency)

	// Phase 1: Common HTTP Status Codes
	if common, ok := classifiercore.ClassifyCommonHTTPStatus(ctx.StatusCode, ctx.Headers); ok {
		retryAfter := common.RetryAfter
		if retryAfter == nil && ctx.RetryAfter != nil {
			retryAfter = ctx.RetryAfter
		}
		return Result{
			Status:     common.Status,
			Category:   common.Category,
			StatusCode: common.StatusCode,
			Reason:     common.Reason,
			Latency:    clampedLatency,
			Retryable:  common.Retryable,
			RetryAfter: retryAfter,
		}
	}

	// Phase 2: Accepted Status Gate
	acceptedCodes := spec.AcceptedStatusCodes
	if len(acceptedCodes) == 0 {
		acceptedCodes = []int{200}
	}
	isAccepted := false
	for _, code := range acceptedCodes {
		if ctx.StatusCode == code {
			isAccepted = true
			break
		}
	}
	if !isAccepted {
		return Result{
			Status:     store.StatusFailed,
			Category:   store.ErrTargetOther,
			StatusCode: ctx.StatusCode,
			Reason:     fmt.Sprintf("unexpected HTTP status %d", ctx.StatusCode),
			Latency:    clampedLatency,
			Retryable:  false,
			RetryAfter: ctx.RetryAfter,
		}
	}

	// Phase 3: Interception Preemption
	if cat, reason, ok := classifiercore.DetectInterception(ctx.NormalizedBody); ok {
		return Result{
			Status:     store.StatusFailed,
			Category:   cat,
			StatusCode: ctx.StatusCode,
			Reason:     reason,
			Latency:    clampedLatency,
			Retryable:  false,
			RetryAfter: ctx.RetryAfter,
		}
	}

	for _, rule := range spec.Interceptions {
		if rule.Condition != nil && rule.Condition.Evaluate(ctx) {
			return Result{
				Status:     rule.Verdict.Status,
				Category:   rule.Verdict.Category,
				StatusCode: ctx.StatusCode,
				Reason:     rule.Verdict.Reason,
				Latency:    clampedLatency,
				Retryable:  false,
				RetryAfter: ctx.RetryAfter,
			}
		}
	}

	// Phase 4: Negative Rejections
	for _, rule := range spec.Rejections {
		if rule.Condition != nil && rule.Condition.Evaluate(ctx) {
			return Result{
				Status:     rule.Verdict.Status,
				Category:   rule.Verdict.Category,
				StatusCode: ctx.StatusCode,
				Reason:     rule.Verdict.Reason,
				Latency:    clampedLatency,
				Retryable:  false,
				RetryAfter: ctx.RetryAfter,
			}
		}
	}

	// Phase 5: Positive Verifications & Fallback
	for _, rule := range spec.Verifications {
		if rule.Condition != nil && rule.Condition.Evaluate(ctx) {
			reason := rule.Verdict.Reason
			if reason == "" {
				reason = "ok"
			}
			return Result{
				Status:     store.StatusPassed,
				Category:   store.ErrNone,
				StatusCode: ctx.StatusCode,
				Reason:     reason,
				Latency:    clampedLatency,
				Retryable:  false,
				RetryAfter: ctx.RetryAfter,
			}
		}
	}

	if spec.Mode == ModePermissive {
		return Result{
			Status:     store.StatusPassed,
			Category:   store.ErrNone,
			StatusCode: ctx.StatusCode,
			Reason:     "ok",
			Latency:    clampedLatency,
			Retryable:  false,
			RetryAfter: ctx.RetryAfter,
		}
	}

	// Strict mode fallback
	return Result{
		Status:     store.StatusFailed,
		Category:   store.ErrTargetOther,
		StatusCode: ctx.StatusCode,
		Reason:     spec.EffectiveStrictFallbackReason(),
		Latency:    clampedLatency,
		Retryable:  false,
		RetryAfter: ctx.RetryAfter,
	}
}
