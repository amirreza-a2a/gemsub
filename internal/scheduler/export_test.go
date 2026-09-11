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

// SetRunnerForTest sets a custom probe runner for test execution of Run.
func (s *Scheduler) SetRunnerForTest(runner tester.ProbeRunner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runner = runner
}
