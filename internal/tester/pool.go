package tester

import (
	"context"
	"sync"

	"gemsub/internal/config"
	"gemsub/internal/parser"
	"gemsub/internal/store"
)

// RunPool probes every candidate with at most cfg.Concurrency probes
// in flight at once, calling onResult for each as it completes (so
// the caller can stream results into the store instead of waiting
// for the whole batch).
func RunPool(ctx context.Context, candidates []parser.Candidate, cfg *config.TestConfig, onResult func(store.Result)) {
	sem := make(chan struct{}, cfg.Concurrency)
	var wg sync.WaitGroup

	for _, cand := range candidates {
		select {
		case <-ctx.Done():
			return
		default:
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(c parser.Candidate) {
			defer wg.Done()
			defer func() { <-sem }()

			result := Probe(ctx, c, cfg)
			onResult(result)
		}(cand)
	}

	wg.Wait()
}
