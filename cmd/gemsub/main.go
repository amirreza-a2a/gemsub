// Command gemsub is a daemon that pulls VPN subscription links,
// tests each one against a real target (to catch regional blocks
// that a plain connectivity check misses), and serves the passing
// subset as a subscription that Throne can auto-update from.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"gemsub/internal/config"
	"gemsub/internal/scheduler"
	"gemsub/internal/store"
	"gemsub/internal/subserver"
)

func main() {
	configPath := flag.String("config", "./config.json", "path to config file")
	headless := flag.Bool("headless", false, "run without the TUI (daemon + sub server only)")
	probeLimit := flag.Int("limit", 0, "limit number of parsed candidates to probe per cycle (0 = unlimited)")
	publish := flag.Bool("publish", false, "enable git publishing after test cycles (overrides config)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
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
					log.Fatalf("config: %v", err)
				}
			}
		}
	})

	st := store.New(cfg.StateFile, cfg.Test.MaxInconclusiveCycles)
	if err := st.Load(); err != nil {
		log.Printf("warning: could not load previous state: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		log.Println("shutting down...")
		cancel()
	}()

	sched := scheduler.New(cfg, st)
	go sched.Run(ctx)

	srv := subserver.New(&cfg.Serve, st)
	go func() {
		if err := srv.Run(ctx); err != nil {
			log.Fatalf("subserver: %v", err)
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
	log.Println("TUI not wired up yet — running headless. Use -headless to silence this note.")
	<-ctx.Done()
}
