package scheduler

import (
	"context"
	"errors"
	"sync"
	"time"

	"gemsub/internal/events"
)

var (
	// ErrSchedulerAlreadyRunning is returned when attempting to Start a scheduler that is already running.
	ErrSchedulerAlreadyRunning = errors.New("scheduler already running")

	// ErrSchedulerNotRunning is returned when attempting to Trigger a scheduler that is not running.
	ErrSchedulerNotRunning = errors.New("scheduler not running")
)

// Status contains an isolated snapshot of the scheduler runtime control state.
// It distinguishes between daemon liveness (Running: the scheduler loop goroutine is alive)
// and probe cycle execution (CycleActive: an individual test cycle is actively executing).
type Status struct {
	// Running indicates whether the scheduler loop daemon goroutine is alive.
	Running bool `json:"running"`

	// CycleActive indicates whether an individual test cycle is actively executing.
	CycleActive bool `json:"cycle_active"`

	LastCycleStart time.Time              `json:"last_cycle_start,omitempty"`
	LastCycleEnd   time.Time              `json:"last_cycle_end,omitempty"`
	LastDuration   time.Duration          `json:"last_duration,omitempty"`
	LastMetrics    events.ProgressMetrics `json:"last_metrics"`
	LastCancelled  bool                   `json:"last_cancelled,omitempty"`
	LastServable   int                    `json:"last_servable"`
}

// ControlService coordinates scheduler lifecycle, execution control, and runtime telemetry.
// It acts as the boundary between presentation layers and scheduler internals.
type ControlService struct {
	mu      sync.Mutex
	sched   *Scheduler
	running bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewControlService creates a dedicated lifecycle and control service for the given scheduler.
func NewControlService(sched *Scheduler) *ControlService {
	return &ControlService{
		sched: sched,
	}
}

// Start launches the scheduler loop in a managed background goroutine.
// Returns ctx.Err() if ctx is already cancelled or expired.
// Returns ErrSchedulerAlreadyRunning if the scheduler is already running.
func (c *ControlService) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	if c.running {
		return ErrSchedulerAlreadyRunning
	}
	if c.sched == nil {
		return errors.New("scheduler not configured")
	}

	// Drain any stale triggers from prior cycles before starting
	select {
	case <-c.sched.Trigger:
	default:
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	c.running = true
	c.cancel = cancel
	c.done = done

	go func() {
		defer close(done)
		defer func() {
			c.mu.Lock()
			c.running = false
			c.cancel = nil
			c.mu.Unlock()
		}()

		c.sched.Run(runCtx)
	}()

	return nil
}

// Stop gracefully terminates the running scheduler and blocks until the scheduler
// goroutine exits completely. Calling Stop on an already stopped scheduler is a no-op.
func (c *ControlService) Stop() error {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return nil
	}
	cancel := c.cancel
	done := c.done
	c.mu.Unlock()

	// Signal cancellation outside the critical section
	if cancel != nil {
		cancel()
	}

	// Await complete shutdown of the scheduler goroutine
	if done != nil {
		<-done
	}

	return nil
}

// Trigger requests an immediate cycle execution.
// If the scheduler is not running, ErrSchedulerNotRunning is returned.
// If a cycle is already active, the trigger is coalesced to request the next cycle
// without creating overlapping cycles.
func (c *ControlService) Trigger() error {
	c.mu.Lock()
	running := c.running
	c.mu.Unlock()

	if !running {
		return ErrSchedulerNotRunning
	}

	select {
	case c.sched.Trigger <- struct{}{}:
		return nil
	default:
		// A trigger is already queued for the next cycle
		return nil
	}
}

// IsRunning returns true if the scheduler loop is actively executing.
func (c *ControlService) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// Status returns an isolated snapshot of the scheduler runtime control state.
func (c *ControlService) Status() Status {
	c.mu.Lock()
	running := c.running
	c.mu.Unlock()

	var st Status
	st.Running = running
	if c.sched != nil {
		st.CycleActive = c.sched.CycleActive()
		st.LastCycleStart, st.LastCycleEnd, st.LastDuration, st.LastMetrics, st.LastCancelled, st.LastServable = c.sched.LastCycleInfo()
	}
	return st
}
