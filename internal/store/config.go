package store

// ScoringConfig defines policy parameters for reliability scoring and servability gating.
type ScoringConfig struct {
	HistoryCapacity            int                           `json:"history_capacity"`
	DecayLambda                float64                       `json:"decay_lambda"`
	MinServableScore           float64                       `json:"min_servable_score"`
	MinObservationsForServing int                           `json:"min_observations_for_serving"`
	MaxAbsentCycles            int                           `json:"max_absent_cycles"`
	CategoryWeights            map[ErrorCategory]float64     `json:"category_weights"`
	AttemptPenaltyMultiplier   float64                       `json:"attempt_penalty_multiplier"`
}

// Clone returns a deep copy of ScoringConfig, duplicating map fields to prevent aliasing.
func (c ScoringConfig) Clone() ScoringConfig {
	cp := c
	if c.CategoryWeights != nil {
		weights := make(map[ErrorCategory]float64, len(c.CategoryWeights))
		for k, v := range c.CategoryWeights {
			weights[k] = v
		}
		cp.CategoryWeights = weights
	}
	return cp
}

// DefaultScoringConfig returns the frozen default policy configuration.
func DefaultScoringConfig() ScoringConfig {
	weights := map[ErrorCategory]float64{
		ErrNone:              1.0,
		ErrRegionBlocked:     0.0,
		ErrTargetDenied:      0.0,
		ErrConnRefused:       0.1,
		ErrReset:             0.1,
		ErrTLS:               0.1,
		ErrReality:           0.1,
		ErrConfig:            0.1,
		ErrTargetOther:       0.1,
		ErrTimeout:           0.4,
		ErrProxyRateLimited:  0.5,
		ErrTargetRateLimited: 0.5,
		ErrTargetError:       0.5,
		ErrProxyError:        0.3,
	}

	return ScoringConfig{
		HistoryCapacity:            10,
		DecayLambda:                0.75,
		MinServableScore:           0.65,
		MinObservationsForServing: 1,
		MaxAbsentCycles:            2,
		CategoryWeights:            weights,
		AttemptPenaltyMultiplier:   0.0, // disabled by default per frozen specification
	}
}
