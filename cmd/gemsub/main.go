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
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *headless {
		cfg.Headless = true
	}

	st := store.New(cfg.StateFile)
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
		if err := srv.Run(); err != nil {
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
