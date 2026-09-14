package tester

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"gemsub/internal/config"
	"gemsub/internal/parser"
	"gemsub/internal/store"
	"gemsub/internal/tester/gemini"
	"gemsub/internal/tester/transport"
)

type probeTimingRecord struct {
	link            string
	status          store.Status
	transportOK     bool
	boxSpinUp       time.Duration
	transportNet    time.Duration
	geminiNet       time.Duration
	boxTeardown     time.Duration
	totalAttempt    time.Duration
	// Compare N=3 extra samples
	reusedSamples   [3]time.Duration
	freshBoxSamples [3]time.Duration
}

func TestProfilingSpike_JitterLifecycle(t *testing.T) {
	statePath := filepath.Join("..", "..", "gemsub_state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Skipf("skipping spike: gemsub_state.json not found: %v", err)
	}

	var state struct {
		Records []struct {
			CanonicalLink string `json:"canonical_link"`
			ActiveLink    string `json:"active_link"`
			Latest        struct {
				Status string `json:"status"`
			} `json:"latest"`
		} `json:"records"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}

	// Select 60 candidates: 30 that were passed, 30 others (across protocols)
	var passedLinks []string
	var otherLinks []string
	for _, r := range state.Records {
		link := r.CanonicalLink
		if link == "" {
			link = r.ActiveLink
		}
		if link == "" {
			continue
		}
		if r.Latest.Status == "passed" && len(passedLinks) < 30 {
			passedLinks = append(passedLinks, link)
		} else if r.Latest.Status != "passed" && len(otherLinks) < 30 {
			otherLinks = append(otherLinks, link)
		}
		if len(passedLinks) >= 30 && len(otherLinks) >= 30 {
			break
		}
	}

	selectedLinks := append(passedLinks, otherLinks...)
	t.Logf("Selected %d candidates (%d previously passed, %d others)", len(selectedLinks), len(passedLinks), len(otherLinks))

	var candidates []parser.Candidate
	for _, l := range selectedLinks {
		c, err := parser.Parse(l)
		if err == nil {
			candidates = append(candidates, c)
		}
	}
	t.Logf("Successfully parsed %d candidates", len(candidates))
	if len(candidates) == 0 {
		t.Fatalf("no candidates parsed")
	}

	// Setup pprof profiling output directory
	outDir := filepath.Join("..", "..", ".scratch", "pprof_spike")
	_ = os.MkdirAll(outDir, 0755)

	cpuFile, err := os.Create(filepath.Join(outDir, "cpu.pprof"))
	if err != nil {
		t.Fatalf("create cpu.pprof: %v", err)
	}
	defer cpuFile.Close()

	if err := pprof.StartCPUProfile(cpuFile); err != nil {
		t.Fatalf("start cpu profile: %v", err)
	}

	cfg := &config.TestConfig{
		HealthURL:     transport.DefaultHealthURL,
		HealthTimeout: 4 * time.Second,
		TargetURL:     "https://gemini.google.com/app",
		Timeout:       10 * time.Second,
		DialTimeout:   4 * time.Second,
		Concurrency:   40,
		MaxRetries:    0, // Exact single attempt for clean breakdown
	}

	var timingsMu sync.Mutex
	var timings []probeTimingRecord

	var spinUpAllocs, spinUpBytes int64
	var teardownAllocs, teardownBytes int64

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Measure standalone Box spin-up & teardown allocations on 10 candidates
	for i := 0; i < 10 && i < len(candidates); i++ {
		c := candidates[i]
		var m1, m2, m3 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m1)

		t0 := time.Now()
		dialFn, closeBox, err := buildDialer(ctx, c, cfg.DialTimeout)
		_ = t0
		_ = dialFn
		runtime.ReadMemStats(&m2)

		if err == nil {
			atomic.AddInt64(&spinUpAllocs, int64(m2.Mallocs-m1.Mallocs))
			atomic.AddInt64(&spinUpBytes, int64(m2.TotalAlloc-m1.TotalAlloc))

			closeBox()
			runtime.ReadMemStats(&m3)
			atomic.AddInt64(&teardownAllocs, int64(m3.Mallocs-m2.Mallocs))
			atomic.AddInt64(&teardownBytes, int64(m3.TotalAlloc-m2.TotalAlloc))
		}
	}

	// Now run the pool at concurrency=40 with instrumented probe
	instrumentedRunner := func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, limiter interface{}) store.Result {
		attemptCtx, cancelAttempt := context.WithTimeout(ctx, cfg.Timeout)
		defer cancelAttempt()

		t0 := time.Now()
		dialFn, closeBox, err := buildDialer(attemptCtx, cand, cfg.DialTimeout)
		dSpinUp := time.Since(t0)

		if err != nil {
			timingsMu.Lock()
			timings = append(timings, probeTimingRecord{
				link:         cand.Link,
				status:       store.StatusFailed,
				boxSpinUp:    dSpinUp,
				totalAttempt: time.Since(t0),
			})
			timingsMu.Unlock()
			return store.Result{Status: store.StatusFailed, Reason: err.Error()}
		}

		// Stage 1
		t1 := time.Now()
		tr := transport.Probe(attemptCtx, dialFn, transport.Config{
			HealthURL:     cfg.HealthURL,
			HealthTimeout: cfg.HealthTimeout,
		})
		dTransport := time.Since(t1)

		var dGemini time.Duration
		var gResult gemini.Result
		if tr.OK {
			t2 := time.Now()
			gResult = gemini.Probe(attemptCtx, dialFn, gemini.Config{
				URL:         cfg.TargetURL,
				Timeout:     cfg.Timeout,
				DialTimeout: cfg.DialTimeout,
			})
			dGemini = time.Since(t2)
		}

		// If candidate connected (transport OK), run comparison:
		// Reused Box (3 samples) vs Fresh Box (3 samples)
		var reusedSamples [3]time.Duration
		var freshSamples [3]time.Duration

		if tr.OK {
			// A: Reused Box samples
			for s := 0; s < 3; s++ {
				ts := time.Now()
				_ = transport.Probe(attemptCtx, dialFn, transport.Config{
					HealthURL:     cfg.HealthURL,
					HealthTimeout: 2 * time.Second,
				})
				reusedSamples[s] = time.Since(ts)
			}

			// B: Fresh Box samples
			for s := 0; s < 3; s++ {
				ts := time.Now()
				freshDial, freshClose, err := buildDialer(attemptCtx, cand, cfg.DialTimeout)
				if err == nil {
					_ = transport.Probe(attemptCtx, freshDial, transport.Config{
						HealthURL:     cfg.HealthURL,
						HealthTimeout: 2 * time.Second,
					})
					freshClose()
				}
				freshSamples[s] = time.Since(ts)
			}
		}

		tClose := time.Now()
		closeBox()
		dTeardown := time.Since(tClose)

		total := time.Since(t0)

		rec := probeTimingRecord{
			link:            cand.Link,
			status:          gResult.Status,
			transportOK:     tr.OK,
			boxSpinUp:       dSpinUp,
			transportNet:    dTransport,
			geminiNet:       dGemini,
			boxTeardown:     dTeardown,
			totalAttempt:    total,
			reusedSamples:   reusedSamples,
			freshBoxSamples: freshSamples,
		}

		timingsMu.Lock()
		timings = append(timings, rec)
		timingsMu.Unlock()

		return store.Result{
			Link:             cand.Link,
			Status:           gResult.Status,
			TransportOK:      tr.OK,
			TransportLatency: tr.Latency,
			Latency:          total,
		}
	}

	// Execute through pool runner
	startPool := time.Now()
	RunPoolWithRunner(ctx, candidates, cfg, func(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig, _ *rate.Limiter) store.Result {
		return instrumentedRunner(ctx, cand, cfg, nil)
	}, nil, nil)
	totalPoolTime := time.Since(startPool)

	pprof.StopCPUProfile()

	// Memory profile
	memFile, err := os.Create(filepath.Join(outDir, "mem.pprof"))
	if err == nil {
		runtime.GC()
		_ = pprof.WriteHeapProfile(memFile)
		memFile.Close()
	}

	// Aggregate and print results
	var sumSpinUp, sumTransport, sumGemini, sumTeardown, sumTotal time.Duration
	var countTransportOK int
	var sumReusedSample, sumFreshSample time.Duration
	var sampleCount int

	for _, rec := range timings {
		sumSpinUp += rec.boxSpinUp
		sumTransport += rec.transportNet
		sumGemini += rec.geminiNet
		sumTeardown += rec.boxTeardown
		sumTotal += (rec.boxSpinUp + rec.transportNet + rec.geminiNet + rec.boxTeardown)

		if rec.transportOK {
			countTransportOK++
			for s := 0; s < 3; s++ {
				if rec.reusedSamples[s] > 0 {
					sumReusedSample += rec.reusedSamples[s]
					sumFreshSample += rec.freshBoxSamples[s]
					sampleCount++
				}
			}
		}
	}

	n := len(timings)
	t.Logf("=== PROFILING SPIKE RESULTS (N=%d candidates, pool concurrency=%d, total pool wall time=%v) ===", n, cfg.Concurrency, totalPoolTime)
	t.Logf("Transport OK: %d / %d (%.1f%%)", countTransportOK, n, float64(countTransportOK)/float64(n)*100)

	avgSpinUp := time.Duration(int64(sumSpinUp) / int64(n))
	avgTeardown := time.Duration(int64(sumTeardown) / int64(n))
	avgTransport := time.Duration(int64(sumTransport) / int64(n))
	avgGemini := time.Duration(0)
	if countTransportOK > 0 {
		avgGemini = time.Duration(int64(sumGemini) / int64(countTransportOK))
	}
	avgLifecycle := avgSpinUp + avgTeardown
	avgTotalPerCand := time.Duration(int64(sumTotal) / int64(n))

	pctLifecycle := float64(sumSpinUp+sumTeardown) / float64(sumTotal) * 100
	pctNetwork := float64(sumTransport+sumGemini) / float64(sumTotal) * 100

	t.Logf("Per-Candidate Time Breakdown (Averages across %d candidates):", n)
	t.Logf("  - Box Spin-up (box.New + Start + Outbound lookup): %v (%.2f%%)", avgSpinUp, float64(sumSpinUp)/float64(sumTotal)*100)
	t.Logf("  - Transport Net Dial/Handshake:                    %v (%.2f%%)", avgTransport, float64(sumTransport)/float64(sumTotal)*100)
	if countTransportOK > 0 {
		t.Logf("  - Gemini Net Dial/Body (on connected):            %v (%.2f%%)", avgGemini, float64(sumGemini)/float64(sumTotal)*100)
	}
	t.Logf("  - Box Teardown (instance.Close):                  %v (%.2f%%)", avgTeardown, float64(sumTeardown)/float64(sumTotal)*100)
	t.Logf("  => Box Lifecycle (Spin-up + Teardown):            %v (%.2f%% of total candidate probe time)", avgLifecycle, pctLifecycle)
	t.Logf("  => Network I/O (Handshakes + Payloads):           %.2f%% of total candidate probe time", pctNetwork)

	if sampleCount > 0 {
		avgReused := time.Duration(int64(sumReusedSample) / int64(sampleCount))
		avgFresh := time.Duration(int64(sumFreshSample) / int64(sampleCount))
		t.Logf("Jitter Sampling Comparison (across %d samples on live candidates):", sampleCount)
		t.Logf("  - Reused Box (dial through existing instance):    %v / sample", avgReused)
		t.Logf("  - Fresh Box (box.New + Start + Dial + Close):     %v / sample", avgFresh)
		t.Logf("  => Fresh Box overhead:                            +%v (%.1fx slower per sample)", avgFresh-avgReused, float64(avgFresh)/float64(avgReused))
	}

	t.Logf("Memory Allocations per Box Spin-up & Teardown:")
	t.Logf("  - Spin-up:  %d allocs, %d bytes (~%.2f KB)", spinUpAllocs/10, spinUpBytes/10, float64(spinUpBytes/10)/1024)
	t.Logf("  - Teardown: %d allocs, %d bytes (~%.2f KB)", teardownAllocs/10, teardownBytes/10, float64(teardownBytes/10)/1024)

	// Save JSON findings
	findingsJSON, _ := json.MarshalIndent(map[string]interface{}{
		"candidates_tested":   n,
		"concurrency":         cfg.Concurrency,
		"pool_wall_time_ms":   totalPoolTime.Milliseconds(),
		"avg_spinup_ms":       float64(avgSpinUp.Microseconds()) / 1000.0,
		"avg_teardown_ms":     float64(avgTeardown.Microseconds()) / 1000.0,
		"avg_lifecycle_ms":    float64(avgLifecycle.Microseconds()) / 1000.0,
		"avg_transport_ms":    float64(avgTransport.Microseconds()) / 1000.0,
		"avg_total_ms":        float64(avgTotalPerCand.Microseconds()) / 1000.0,
		"pct_box_lifecycle":   pctLifecycle,
		"pct_network_io":      pctNetwork,
		"spinup_allocs":       spinUpAllocs / 10,
		"spinup_bytes":        spinUpBytes / 10,
		"teardown_allocs":     teardownAllocs / 10,
		"teardown_bytes":      teardownBytes / 10,
		"connected_count":     countTransportOK,
		"sample_count":        sampleCount,
		"avg_reused_samp_ms":  float64(sumReusedSample.Microseconds()) / float64(sampleCount) / 1000.0,
		"avg_fresh_samp_ms":   float64(sumFreshSample.Microseconds()) / float64(sampleCount) / 1000.0,
	}, "", "  ")

	_ = os.WriteFile(filepath.Join(outDir, "spike_summary.json"), findingsJSON, 0644)
}
