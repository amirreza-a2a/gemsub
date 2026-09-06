// Command gemsub is a daemon that pulls VPN subscription links,
// tests each one against a real target (to catch regional blocks
// that a plain connectivity check misses), and serves the passing
// subset as a subscription that Throne can auto-update from.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"gemsub/internal/config"
	"gemsub/internal/logging"
	"gemsub/internal/scheduler"
	"gemsub/internal/store"
	"gemsub/internal/subserver"
)

func main() {
	// Initialize default standard terminal logging during bootstrap
	logging.Setup(true, 1000, os.Stderr)

	configPath := flag.String("config", "./config.json", "path to config file")
	headless := flag.Bool("headless", false, "run without the TUI (daemon + sub server only)")
	probeLimit := flag.Int("limit", 0, "limit number of parsed candidates to probe per cycle (0 = unlimited)")
	publish := flag.Bool("publish", false, "enable git publishing after test cycles (overrides config)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}
	if *headless {
		cfg.Headless = true
	}
	if *probeLimit > 0 {
		cfg.ProbeLimit = *probeLimit
	}
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "publish" {
			cfg.Publishing.Enabled = *publish
			if *publish {
				if err := cfg.Validate(); err != nil {
					slog.Error("config validation failed", "err", err)
					os.Exit(1)
				}
			}
		}
	})

	// Configure handler based on headless / TUI mode
	logging.Setup(cfg.Headless, 1000, os.Stderr)

	st := store.New(cfg.StateFile, cfg.Test.MaxInconclusiveCycles)
	if err := st.Load(); err != nil {
		slog.Warn("could not load previous state", "err", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		slog.Info("shutting down...")
		cancel()
	}()

	sched := scheduler.New(cfg, st)
	go sched.Run(ctx)

	srv := subserver.New(&cfg.Serve, st)
	go func() {
		if err := srv.Run(ctx); err != nil {
			slog.Error("subserver error", "err", err)
			os.Exit(1)
		}
	}()

	if cfg.Headless {
		<-ctx.Done()
		return
	}

	// TODO: attach the bubbletea TUI here, reading from st and
	// writing to sched.Trigger for a manual "refresh now" action.
	// Until that's wired up, headless mode is what you want:
	//   gemsub -headless -config config.json
	slog.Info("TUI not wired up yet — running headless. Use -headless to silence this note.")
	<-ctx.Done()
}
