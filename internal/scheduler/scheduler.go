// Package scheduler runs the fetch -> parse -> test -> persist cycle
// on a timer, and exposes a Trigger channel so the TUI (or a future
// HTTP endpoint) can force an immediate re-run without waiting for
// the interval.
package scheduler

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"gemsub/internal/config"
	"gemsub/internal/parser"
	"gemsub/internal/publisher"
	"gemsub/internal/source"
	"gemsub/internal/store"
	"gemsub/internal/tester"
)

type Scheduler struct {
	cfg *config.Config
	st  *store.Store
	pub *publisher.Publisher

	// Trigger lets anything (TUI, signal handler, ...) request an
	// immediate cycle instead of waiting for the interval. Buffered
	// so a trigger while a cycle is already running isn't lost.
	Trigger chan struct{}
}

func New(cfg *config.Config, st *store.Store) *Scheduler {
	var pub *publisher.Publisher
	if cfg.Publishing.Enabled {
		pub = publisher.New(&cfg.Publishing, st)
	}
	return &Scheduler{
		cfg:     cfg,
		st:      st,
		pub:     pub,
		Trigger: make(chan struct{}, 1),
	}
}

// SetPublisher allows configuring a custom publisher (e.g. for testing).
func (s *Scheduler) SetPublisher(pub *publisher.Publisher) {
	s.pub = pub
}

// Run blocks until ctx is cancelled, running one cycle immediately
// and then one per FetchInterval (or whenever Trigger fires).
func (s *Scheduler) Run(ctx context.Context) {
	s.runCycle(ctx)

	ticker := time.NewTicker(s.cfg.FetchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runCycle(ctx)
		case <-s.Trigger:
			s.runCycle(ctx)
			ticker.Reset(s.cfg.FetchInterval)
		}
	}
}

func (s *Scheduler) runCycle(ctx context.Context) {
	slog.Info("scheduler: cycle starting")
	cycleStart := time.Now()

	links, fetchErrs := source.FetchAll(ctx, s.cfg.Sources)
	for _, e := range fetchErrs {
		slog.Error("scheduler: fetch error", "err", e)
	}
	slog.Info("scheduler: raw links fetched", "count", len(links))

	if len(links) == 0 && len(fetchErrs) > 0 {
		slog.Warn("scheduler: fetch failed; aborting cycle without updating store or publishing", "errors", len(fetchErrs), "links", 0)
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
	if s.cfg.ProbeLimit > 0 && len(candidates) > s.cfg.ProbeLimit {
		candidates = candidates[:s.cfg.ProbeLimit]
	}
	slog.Info("scheduler: selected candidates for probing", "selected", len(candidates), "total", totalParsed)

	// Drop stale results for links no longer present in this cycle's
	// source, so a config removed upstream also disappears from what
	// we serve to Throne.
	s.st.StartCycle(linkSet)

	var passed, failed, inconclusive int64
	var regionBlocked, timeout int64

	completed := tester.RunPool(ctx, candidates, &s.cfg.Test, func(r store.Result) {
		s.st.PutWithTransition(r)
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

	if s.pub != nil {
		if err := s.pub.Publish(ctx); err != nil {
			slog.Error("scheduler: publish failed", "err", err)
		}
	}
}
