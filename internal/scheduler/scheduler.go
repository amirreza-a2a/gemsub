// Package scheduler runs the fetch -> parse -> test -> persist cycle
// on a timer, and exposes a Trigger channel so the TUI (or a future
// HTTP endpoint) can force an immediate re-run without waiting for
// the interval.
package scheduler

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"gemsub/internal/config"
	"gemsub/internal/parser"
	"gemsub/internal/source"
	"gemsub/internal/store"
	"gemsub/internal/tester"
)

type Scheduler struct {
	cfg *config.Config
	st  *store.Store

	// Trigger lets anything (TUI, signal handler, ...) request an
	// immediate cycle instead of waiting for the interval. Buffered
	// so a trigger while a cycle is already running isn't lost.
	Trigger chan struct{}
}

func New(cfg *config.Config, st *store.Store) *Scheduler {
	return &Scheduler{
		cfg:     cfg,
		st:      st,
		Trigger: make(chan struct{}, 1),
	}
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
	log.Printf("scheduler: cycle starting")
	cycleStart := time.Now()

	links, fetchErrs := source.FetchAll(s.cfg.Sources)
	for _, e := range fetchErrs {
		log.Printf("scheduler: fetch error: %v", e)
	}
	log.Printf("scheduler: %d raw links fetched", len(links))

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
	log.Printf("scheduler: selected %d of %d parsed candidates for probing", len(candidates), totalParsed)

	// Drop stale results for links no longer present in this cycle's
	// source, so a config removed upstream also disappears from what
	// we serve to Throne.
	s.st.StartCycle(linkSet)

	var passed, failed, inconclusive int64
	completed := tester.RunPool(ctx, candidates, &s.cfg.Test, func(r store.Result) {
		s.st.PutWithTransition(r)
		switch r.Status {
		case store.StatusPassed:
			atomic.AddInt64(&passed, 1)
		case store.StatusFailed:
			atomic.AddInt64(&failed, 1)
		case store.StatusInconclusive:
			atomic.AddInt64(&inconclusive, 1)
		}
	})

	if !completed || ctx.Err() != nil {
		log.Printf("scheduler: cycle cancelled/interrupted after %s; saving partial state without incrementing cycle_count",
			time.Since(cycleStart).Round(time.Second))
		if err := s.st.Save(); err != nil {
			log.Printf("scheduler: save state failed: %v", err)
		}
		return
	}

	s.st.FinishCycle()
	if err := s.st.Save(); err != nil {
		log.Printf("scheduler: save state failed: %v", err)
	}

	stats := s.st.Stats()
	log.Printf("scheduler: cycle done in %s — %d passed, %d failed, %d inconclusive (%d servable to throne)",
		time.Since(cycleStart).Round(time.Second), stats.Passed, stats.Failed, stats.Inconclusive, stats.Servable)
}
