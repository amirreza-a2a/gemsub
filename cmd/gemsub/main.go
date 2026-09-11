// Command gemsub is a daemon that pulls VPN subscription links,
// tests each one against a real target (to catch regional blocks
// that a plain connectivity check misses), and serves the passing
// subset as a subscription that Throne can auto-update from.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"gemsub/internal/config"
	"gemsub/internal/events"
	"gemsub/internal/logging"
	"gemsub/internal/publisher"
	"gemsub/internal/scheduler"
	"gemsub/internal/source"
	"gemsub/internal/store"
	"gemsub/internal/subserver"
	"gemsub/internal/tui"
	"gemsub/internal/tui/adapter"
	"gemsub/internal/tui/country"
	"gemsub/internal/version"
)

func main() {
	// Initialize default standard terminal logging during bootstrap
	logging.Setup(true, 1000, os.Stderr)

	configPath := flag.String("config", "./config.json", "path to config file")
	headless := flag.Bool("headless", false, "run without the TUI (daemon + sub server only)")
	probeLimit := flag.Int("limit", 0, "limit number of parsed candidates to probe per cycle (0 = unlimited)")
	publish := flag.Bool("publish", false, "enable git publishing after test cycles (overrides config)")
	flagMode := flag.String("flag-mode", "", "country flag presentation mode: auto, unicode, ascii")
	showVersion := flag.Bool("version", false, "print version information and exit")
	flag.BoolVar(showVersion, "v", false, "print version information and exit (shorthand)")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.Info())
		return
	}

	configSvc, err := config.NewService(*configPath, nil, nil)
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}

	baseCfg := configSvc.Get()
	runtimeCfg := baseCfg.Clone()

	if *headless {
		runtimeCfg.Headless = true
	}
	if *probeLimit > 0 {
		runtimeCfg.ProbeLimit = *probeLimit
	}
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "publish" {
			runtimeCfg.Publishing.Enabled = *publish
			if *publish {
				if err := runtimeCfg.Validate(); err != nil {
					slog.Error("config validation failed", "err", err)
					os.Exit(1)
				}
			}
		}
		if f.Name == "flag-mode" {
			runtimeCfg.FlagMode = *flagMode
			if err := runtimeCfg.Validate(); err != nil {
				slog.Error("config validation failed", "err", err)
				os.Exit(1)
			}
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		slog.Info("shutting down...")
		cancel()
	}()

	if err := runLifecycle(ctx, runtimeCfg, os.Stderr, configSvc); err != nil {
		slog.Error("application error", "err", err)
	}
}

// runLifecycle coordinates the complete application lifecycle:
// 1. Instantiates infrastructure and presentation components
// 2. Starts scheduler and subserver workers
// 3. Runs headless wait or Bubble Tea event loop
// 4. Coordinates graceful shutdown: cancellation, worker drain, terminal release, Store.Save()
func runLifecycle(ctx context.Context, cfg *config.Config, logWriter io.Writer, configSvc ...*config.Service) error {
	rt, cleanup := setupRuntime(cfg, logWriter, configSvc...)
	defer cleanup()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup

	// Step 5: Start Scheduler via ControlService
	if err := rt.SchedulerCtrl.Start(ctx); err != nil {
		slog.Error("scheduler start error", "err", err)
		cancel()
	}
	defer func() { _ = rt.SchedulerCtrl.Stop() }()

	// Step 6: Start HTTP subserver
	srv := subserver.New(&cfg.Serve, rt.Store)
	srv.SetEventBus(rt.Bus)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := srv.Run(ctx); err != nil {
			slog.Error("subserver error", "err", err)
			cancel()
		}
	}()

	// Step 7: Headless execution path has zero TUI runtime components
	if cfg.Headless {
		<-ctx.Done()
		wg.Wait()
		return nil
	}

	// Step 8: Run Bubble Tea program in non-headless mode
	go func() {
		<-ctx.Done()
		rt.Program.Quit()
	}()

	if _, err := rt.Program.Run(); err != nil {
		slog.Error("tui error", "err", err)
	}
	cancel()
	wg.Wait()
	return nil
}

// Runtime encapsulates application infrastructure and optional presentation components.
type Runtime struct {
	ConfigSvc     *config.Service
	SourceSvc     *source.Service
	PublishSvc    *publisher.Service
	SchedulerCtrl *scheduler.ControlService
	RingHandler   *logging.RingLogHandler
	Store         *store.Store
	Bus           *events.EventBus
	Adapter       *adapter.Adapter
	TUIModel      *tui.Model
	Program       *tea.Program
}

// setupRuntime instantiates application components in the strict startup sequence.
// In headless mode, it guarantees zero TUI runtime components are created.
func setupRuntime(cfg *config.Config, logWriter io.Writer, configSvc ...*config.Service) (*Runtime, func()) {
	if logWriter == nil {
		logWriter = os.Stderr
	}

	// Step 1: Configure logging handler based on headless / TUI mode
	ringHandler := logging.Setup(cfg.Headless, 1000, logWriter)

	// Step 2: Initialize Store and load persisted state before scheduler execution
	st := store.New(cfg.StateFile, cfg.Test.MaxInconclusiveCycles)
	if err := st.Load(); err != nil {
		slog.Warn("could not load previous state", "err", err)
	}

	// Step 3: Instantiate EventBus
	bus := events.New()

	var svc *config.Service
	var srcSvc *source.Service
	var pubSvc *publisher.Service
	if len(configSvc) > 0 && configSvc[0] != nil {
		svc = configSvc[0]
		svc.SetEventPublisher(bus)
		srcSvc = source.NewService(svc)
		pubSvc = publisher.NewService(svc, st, bus)
	} else {
		pubSvc = publisher.NewService(nil, st, bus)
	}

	// Step 4: Instantiate Scheduler and ControlService
	sched := scheduler.New(cfg, st, bus)
	if pubSvc != nil {
		sched.SetPublisher(pubSvc)
	}
	schedCtrl := scheduler.NewControlService(sched)

	rt := &Runtime{
		ConfigSvc:     svc,
		SourceSvc:     srcSvc,
		PublishSvc:    pubSvc,
		SchedulerCtrl: schedCtrl,
		RingHandler:   ringHandler,
		Store:         st,
		Bus:           bus,
	}

	// Step 5: In non-headless mode, instantiate and subscribe TUI adapter BEFORE scheduler execution
	if !cfg.Headless {
		ad := adapter.New(st, bus, ringHandler)
		if cfg.FlagMode != "" {
			ad.SetFlagMode(country.Mode(cfg.FlagMode))
		}
		ad.Subscribe()
		rt.Adapter = ad
		rt.TUIModel = tui.New(ad)
		rt.Program = tea.NewProgram(rt.TUIModel, tea.WithAltScreen())
	}

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			if rt.SchedulerCtrl != nil {
				_ = rt.SchedulerCtrl.Stop()
			}
			if rt.Adapter != nil {
				rt.Adapter.Close()
			}
			if rt.Bus != nil {
				rt.Bus.Close()
			}
			// Invariant: final store state must be explicitly persisted before process exit
			if rt.Store != nil {
				if err := rt.Store.Save(); err != nil {
					slog.Error("shutdown: failed to persist store state", "err", err)
				}
			}
		})
	}

	return rt, cleanup
}
