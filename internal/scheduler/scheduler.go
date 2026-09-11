// Package scheduler runs the fetch -> parse -> test -> persist cycle
// on a timer, and exposes a Trigger channel so the TUI (or a future
// HTTP endpoint) can force an immediate re-run without waiting for
// the interval.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"gemsub/internal/config"
	"gemsub/internal/events"
	"gemsub/internal/parser"
	"gemsub/internal/publisher"
	"gemsub/internal/source"
	"gemsub/internal/store"
	"gemsub/internal/tester"
)

type Scheduler struct {
	mu          sync.RWMutex
	cfg         config.Config
	st          *store.Store
	pub         *publisher.Publisher
	bus         *events.EventBus
	rotatorOnce sync.Once
	rotator     *candidateRotator

	intervalCh chan time.Duration
	runner     tester.ProbeRunner

	// Trigger lets anything (TUI, signal handler, ...) request an
	// immediate cycle instead of waiting for the interval. Buffered
	// so a trigger while a cycle is already running isn't lost.
	Trigger chan struct{}
}

func New(cfg *config.Config, st *store.Store, bus ...*events.EventBus) *Scheduler {
	var c config.Config
	if cfg != nil {
		c = *cfg.Clone()
	}
	var pub *publisher.Publisher
	if c.Publishing.Enabled {
		pub = publisher.New(&c.Publishing, st)
	}
	var b *events.EventBus
	if len(bus) > 0 && bus[0] != nil {
		b = bus[0]
	} else {
		b = events.New()
	}
	return &Scheduler{
		cfg:        c,
		st:         st,
		pub:        pub,
		bus:        b,
		rotator:    newCandidateRotator(),
		intervalCh: make(chan time.Duration, 1),
		Trigger:    make(chan struct{}, 1),
	}
}

func (s *Scheduler) getRotator() *candidateRotator {
	s.rotatorOnce.Do(func() {
		if s.rotator == nil {
			s.rotator = newCandidateRotator()
		}
	})
	return s.rotator
}

// EventBus returns the scheduler's event bus.
func (s *Scheduler) EventBus() *events.EventBus {
	return s.bus
}

// SetEventBus replaces the event bus (e.g. for testing).
func (s *Scheduler) SetEventBus(bus *events.EventBus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bus = bus
}

// SetPublisher allows configuring a custom publisher (e.g. for testing).
func (s *Scheduler) SetPublisher(pub *publisher.Publisher) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pub = pub
}

// Config returns a copy of the scheduler's current runtime configuration.
func (s *Scheduler) Config() config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return *s.cfg.Clone()
}

// FetchInterval returns the current active cycle interval.
func (s *Scheduler) FetchInterval() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.FetchInterval
}

// ProbeLimit returns the current active candidate probe limit.
func (s *Scheduler) ProbeLimit() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.ProbeLimit
}

// UpdateConfig updates the scheduler runtime configuration under lock,
// updating child publishers and notifying running timers if FetchInterval changed.
func (s *Scheduler) UpdateConfig(newCfg config.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()

	oldInterval := s.cfg.FetchInterval
	cloned := newCfg.Clone()
	if cloned != nil {
		s.cfg = *cloned
	}

	// Update or instantiate/teardown publisher based on updated publishing configuration
	if s.cfg.Publishing.Enabled {
		if s.pub == nil {
			s.pub = publisher.New(&s.cfg.Publishing, s.st)
		} else {
			s.pub.UpdateConfig(s.cfg.Publishing)
		}
	} else {
		s.pub = nil
	}

	newInterval := s.cfg.FetchInterval

	// If fetch interval changed, signal running event loop
	if newInterval != oldInterval && newInterval > 0 {
		select {
		case s.intervalCh <- newInterval:
		default:
			select {
			case <-s.intervalCh:
			default:
			}
			select {
			case s.intervalCh <- newInterval:
			default:
			}
		}
	}
}

// Run blocks until ctx is cancelled, running one cycle immediately
// and then one per FetchInterval (or whenever Trigger fires).
func (s *Scheduler) Run(ctx context.Context) {
	s.mu.RLock()
	bus := s.bus
	s.mu.RUnlock()

	var configSub <-chan any
	if bus != nil {
		configSub = bus.Subscribe()
		defer bus.Unsubscribe(configSub)
	}

	s.runCycle(ctx)

	s.mu.RLock()
	interval := s.cfg.FetchInterval
	s.mu.RUnlock()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runCycle(ctx)
		case <-s.Trigger:
			s.runCycle(ctx)
			s.mu.RLock()
			interval = s.cfg.FetchInterval
			s.mu.RUnlock()
			ticker.Reset(interval)
		case evt, ok := <-configSub:
			if !ok {
				configSub = nil
				continue
			}
			if cu, ok := evt.(config.ConfigUpdated); ok {
				s.UpdateConfig(cu.New)
			}
		case newInterval := <-s.intervalCh:
			if newInterval > 0 {
				interval = newInterval
				ticker.Reset(newInterval)
			}
		}
	}
}

func (s *Scheduler) runCycle(ctx context.Context) {
	s.mu.RLock()
	runner := s.runner
	s.mu.RUnlock()
	if runner == nil {
		runner = tester.Probe
	}
	s.runCycleWithRunner(ctx, runner)
}

func (s *Scheduler) runCycleWithRunner(ctx context.Context, runner tester.ProbeRunner) {
	slog.Info("scheduler: cycle starting")
	cycleStart := time.Now()
	s.bus.Publish(events.CycleStarted{
		StartedAt: cycleStart,
	})

	s.mu.RLock()
	sources := s.cfg.EnabledSourceURLs()
	probeLimit := s.cfg.ProbeLimit
	testCfg := *s.cfg.Test.Clone()
	pub := s.pub
	s.mu.RUnlock()

	// Invariant:
	// - When at least one source remains enabled, disabled-source candidates are
	//   omitted from the fetched link set and naturally age out via Store.MaxAbsentCycles.
	// - When ALL configured sources are disabled, the scheduler intentionally skips
	//   StartCycle() and probing, preserving current Store state to prevent an accidental
	//   wiping of all candidates during complete source suspension.
	if len(sources) == 0 {
		slog.Warn("scheduler: no enabled sources; skipping cycle")
		s.bus.Publish(events.CycleFinished{
			ProgressMetrics: events.ProgressMetrics{
				Total:        0,
				Completed:    0,
				Passed:       0,
				Failed:       0,
				Inconclusive: 0,
			},
			Duration:  time.Since(cycleStart),
			Cancelled: ctx.Err() != nil,
			Servable:  s.st.Stats().Servable,
		})
		return
	}

	links, fetchErrs := source.FetchAll(ctx, sources)
	for _, e := range fetchErrs {
		slog.Error("scheduler: fetch error", "err", e)
	}
	slog.Info("scheduler: raw links fetched", "count", len(links))

	if len(links) == 0 && len(fetchErrs) > 0 {
		slog.Warn("scheduler: fetch failed; aborting cycle without updating store or publishing", "errors", len(fetchErrs), "links", 0)
		s.bus.Publish(events.CycleFinished{
			ProgressMetrics: events.ProgressMetrics{
				Total:        0,
				Completed:    0,
				Passed:       0,
				Failed:       0,
				Inconclusive: 0,
			},
			Duration:  time.Since(cycleStart),
			Cancelled: ctx.Err() != nil,
			Servable:  s.st.Stats().Servable,
		})
		return
	}

	var candidates []parser.Candidate
	linkSet := make(map[string]struct{}, len(links))
	for _, link := range links {
		linkSet[link] = struct{}{}
		cand, err := parser.Parse(link)
		if err != nil {
			continue // malformed / unsupported entry, quietly skipped
		}
		candidates = append(candidates, cand)
	}
	totalParsed := len(candidates)

	// Drop stale results for links no longer present in this cycle's
	// source, so a config removed upstream also disappears from what
	// we serve to Throne.
	s.st.StartCycle(linkSet)

	// Maintain rotation state only for candidates currently present.
	rot := s.getRotator()
	rot.PruneStale(linkSet)

	// Apply fair Least-Recently-Tested (LRT) candidate rotation under ProbeLimit.
	candidates = rot.SelectCandidates(candidates, probeLimit)

	slog.Info("scheduler: selected candidates for probing", "selected", len(candidates), "total", totalParsed)
	s.bus.Publish(events.CandidatesLoaded{
		Total:      len(candidates),
		Candidates: candidates,
	})

	var passed, failed, inconclusive int64
	var regionBlocked, timeout int64

	completed := tester.RunPoolWithRunner(ctx, candidates, &testCfg, runner, s.bus, func(r store.Result) {
		s.st.PutWithTransition(r)
		rot.RecordTested(r.Link, r.TestedAt)
		switch r.Status {
		case store.StatusPassed:
			atomic.AddInt64(&passed, 1)
		case store.StatusFailed:
			atomic.AddInt64(&failed, 1)
			if r.Category == store.ErrRegionBlocked {
				atomic.AddInt64(&regionBlocked, 1)
			} else if r.Category == store.ErrTimeout {
				atomic.AddInt64(&timeout, 1)
			}
		case store.StatusInconclusive:
			atomic.AddInt64(&inconclusive, 1)
		}
	})

	if !completed || ctx.Err() != nil {
		slog.Warn("scheduler: cycle cancelled/interrupted; saving partial state without incrementing cycle_count",
			"duration", time.Since(cycleStart).Round(time.Second))
		if err := s.st.Save(); err != nil {
			slog.Error("scheduler: save state failed", "err", err)
		}
		s.bus.Publish(events.CycleFinished{
			ProgressMetrics: events.ProgressMetrics{
				Total:        len(candidates),
				Completed:    int(atomic.LoadInt64(&passed) + atomic.LoadInt64(&failed) + atomic.LoadInt64(&inconclusive)),
				Passed:       int(atomic.LoadInt64(&passed)),
				Failed:       int(atomic.LoadInt64(&failed)),
				Inconclusive: int(atomic.LoadInt64(&inconclusive)),
			},
			Duration:  time.Since(cycleStart),
			Cancelled: true,
			Servable:  s.st.Stats().Servable,
		})
		return
	}

	s.st.FinishCycle()
	if err := s.st.Save(); err != nil {
		slog.Error("scheduler: save state failed", "err", err)
	}

	stats := s.st.Stats()
	slog.Info("scheduler: cycle done",
		"duration", time.Since(cycleStart).Round(time.Second),
		"passed", stats.Passed,
		"failed", stats.Failed,
		"region_blocked", atomic.LoadInt64(&regionBlocked),
		"timeout", atomic.LoadInt64(&timeout),
		"inconclusive", stats.Inconclusive,
		"servable", stats.Servable,
	)

	s.bus.Publish(events.CycleFinished{
		ProgressMetrics: events.ProgressMetrics{
			Total:        len(candidates),
			Completed:    len(candidates),
			Passed:       int(atomic.LoadInt64(&passed)),
			Failed:       int(atomic.LoadInt64(&failed)),
			Inconclusive: int(atomic.LoadInt64(&inconclusive)),
		},
		Duration:  time.Since(cycleStart),
		Cancelled: false,
		Servable:  stats.Servable,
	})

	if pub != nil {
		if err := pub.Publish(ctx); err != nil {
			slog.Error("scheduler: publish failed", "err", err)
		}
	}
}
