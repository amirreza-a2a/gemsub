package scheduler

import (
	"context"
	"errors"
	"sync"
	"time"

	"gemsub/internal/events"
)

// State represents the operational lifecycle state of the scheduler.
type State int

const (
	StateIdle State = iota
	StateRunning
	StatePaused
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "IDLE"
	case StateRunning:
		return "RUNNING"
	case StatePaused:
		return "PAUSED"
	default:
		return "UNKNOWN"
	}
}

var (
	// ErrSchedulerAlreadyRunning is returned when attempting to Start a scheduler that is already running.
	ErrSchedulerAlreadyRunning = errors.New("scheduler already running")

	// ErrSchedulerNotRunning is returned when attempting to Trigger, Pause, or Resume a scheduler that is not running.
	ErrSchedulerNotRunning = errors.New("scheduler not running")

	// ErrSchedulerAlreadyPaused is returned when attempting to Pause an already-paused scheduler.
	ErrSchedulerAlreadyPaused = errors.New("scheduler already paused")

	// ErrSchedulerNotPaused is returned when attempting to Resume a scheduler that is not paused.
	ErrSchedulerNotPaused = errors.New("scheduler not paused")

	// ErrSchedulerPaused is returned when attempting to Trigger an execution while the scheduler is paused.
	ErrSchedulerPaused = errors.New("scheduler is paused")
)

// Status contains an isolated snapshot of the scheduler runtime control state.
// It distinguishes between daemon liveness (Running: the scheduler loop goroutine is alive)
// and probe cycle execution (CycleActive: an individual test cycle is actively executing).
type Status struct {
	// State indicates the current operational lifecycle state (IDLE, RUNNING, PAUSED).
	State State `json:"state"`

	// Running indicates whether the scheduler loop daemon goroutine is alive.
	Running bool `json:"running"`

	// Paused indicates whether timer execution is currently paused.
	Paused bool `json:"paused"`

	// CycleActive indicates whether an individual test cycle is actively executing.
	CycleActive bool `json:"cycle_active"`

	NextCycleEstimate time.Time              `json:"next_cycle_estimate,omitempty"`
	LastCycleStart    time.Time              `json:"last_cycle_start,omitempty"`
	LastCycleEnd      time.Time              `json:"last_cycle_end,omitempty"`
	LastDuration      time.Duration          `json:"last_duration,omitempty"`
	LastMetrics       events.ProgressMetrics `json:"last_metrics"`
	LastCancelled     bool                   `json:"last_cancelled,omitempty"`
	LastServable      int                    `json:"last_servable"`
	CycleCount        int                    `json:"cycle_count"`
}

// ControlService coordinates scheduler lifecycle, execution control, and runtime telemetry.
// It acts as the boundary between presentation layers and scheduler internals.
type ControlService struct {
	mu        sync.Mutex
	sched     *Scheduler
	running   bool
	stopping  bool
	cancel    context.CancelFunc
	done      chan struct{}
	activeOps sync.WaitGroup
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
	if c.running || c.stopping {
		return ErrSchedulerAlreadyRunning
	}
	if c.sched == nil {
		return errors.New("scheduler not configured")
	}

	// Ensure clean scheduler control state prior to starting
	c.sched.ResetControlState()
	c.sched.InitNextEstimate()

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
	if !c.running || c.stopping {
		c.mu.Unlock()
		return nil
	}
	c.stopping = true
	c.running = false
	cancel := c.cancel
	done := c.done
	sched := c.sched
	c.cancel = nil
	c.mu.Unlock()

	// Signal cancellation outside the critical section
	if cancel != nil {
		cancel()
	}

	// Await complete shutdown of the scheduler goroutine
	if done != nil {
		<-done
	}

	// Await any in-flight control operations to finish
	c.activeOps.Wait()

	// Restore clean idle state on the scheduler
	if sched != nil {
		sched.ResetControlState()
	}

	c.mu.Lock()
	c.stopping = false
	c.done = nil
	c.mu.Unlock()

	return nil
}

// Pause halts scheduled cycle execution without interrupting an active cycle.
// If the scheduler is not running, ErrSchedulerNotRunning is returned.
// If the scheduler is already paused, ErrSchedulerAlreadyPaused is returned.
func (c *ControlService) Pause() error {
	c.mu.Lock()
	if !c.running || c.stopping {
		c.mu.Unlock()
		return ErrSchedulerNotRunning
	}
	if c.sched == nil {
		c.mu.Unlock()
		return errors.New("scheduler not configured")
	}

	if !c.sched.Pause() {
		c.mu.Unlock()
		return ErrSchedulerAlreadyPaused
	}
	c.activeOps.Add(1)
	bus := c.sched.EventBus()
	c.mu.Unlock()

	defer c.activeOps.Done()

	if bus != nil {
		bus.Publish(events.SchedulerPaused{
			PausedAt: time.Now(),
		})
	}
	return nil
}

// Resume resumes periodic cycle execution using the configured interval.
// If the scheduler is not running, ErrSchedulerNotRunning is returned.
// If the scheduler is not paused, ErrSchedulerNotPaused is returned.
func (c *ControlService) Resume() error {
	c.mu.Lock()
	if !c.running || c.stopping {
		c.mu.Unlock()
		return ErrSchedulerNotRunning
	}
	if c.sched == nil {
		c.mu.Unlock()
		return errors.New("scheduler not configured")
	}

	if !c.sched.Resume() {
		c.mu.Unlock()
		return ErrSchedulerNotPaused
	}
	c.activeOps.Add(1)
	bus := c.sched.EventBus()
	c.mu.Unlock()

	defer c.activeOps.Done()

	if bus != nil {
		bus.Publish(events.SchedulerResumed{
			ResumedAt: time.Now(),
		})
	}
	return nil
}

// IsPaused returns true if the scheduler is actively running and paused.
func (c *ControlService) IsPaused() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.running || c.stopping || c.sched == nil {
		return false
	}
	return c.sched.IsPaused()
}

// TriggerNow requests an immediate cycle execution; returns true if accepted, false otherwise.
func (c *ControlService) TriggerNow() bool {
	return c.Trigger() == nil
}

// Trigger requests an immediate cycle execution.
// If the scheduler is not running, ErrSchedulerNotRunning is returned.
// If the scheduler is paused, ErrSchedulerPaused is returned.
// If a cycle is already active, the trigger is coalesced to request the next cycle
// without creating overlapping cycles.
func (c *ControlService) Trigger() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.running || c.stopping {
		return ErrSchedulerNotRunning
	}
	if c.sched == nil {
		return errors.New("scheduler not configured")
	}
	if c.sched.IsPaused() {
		return ErrSchedulerPaused
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
	return c.running && !c.stopping
}

// Status returns an isolated snapshot of the scheduler runtime control state.
func (c *ControlService) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()

	var st Status
	if !c.running || c.stopping || c.sched == nil {
		st.State = StateIdle
		st.Running = false
		st.Paused = false
		st.CycleActive = false
		st.NextCycleEstimate = time.Time{}
		if c.sched != nil {
			st.LastCycleStart, st.LastCycleEnd, st.LastDuration, st.LastMetrics, st.LastCancelled, st.LastServable = c.sched.LastCycleInfo()
			st.CycleCount = c.sched.CycleCount()
		}
		return st
	}

	st.Running = true
	paused := c.sched.IsPaused()
	st.Paused = paused
	if paused {
		st.State = StatePaused
		st.NextCycleEstimate = time.Time{}
	} else {
		st.State = StateRunning
		st.NextCycleEstimate = c.sched.NextCycleEstimate()
	}
	st.CycleActive = c.sched.CycleActive()
	st.LastCycleStart, st.LastCycleEnd, st.LastDuration, st.LastMetrics, st.LastCancelled, st.LastServable = c.sched.LastCycleInfo()
	st.CycleCount = c.sched.CycleCount()
	return st
}
