package scheduler

import (
	"context"

	"gemsub/internal/tester"
)

// RunCycleForTest runs a single cycle with the provided probe runner for testing.
func (s *Scheduler) RunCycleForTest(ctx context.Context, runner tester.ProbeRunner) {
	s.runCycleWithRunner(ctx, runner)
}

// GetRotatorForTest exposes getRotator for concurrent initialization testing.
func (s *Scheduler) GetRotatorForTest() any {
	return s.getRotator()
}
