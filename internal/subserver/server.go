// Package subserver serves the current list of passing configs over
// HTTP in a format Throne's Subscription group type can consume
// directly, so Throne's own auto-update handles refreshing — no
// manual copy/paste, no curl.
package subserver

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gemsub/internal/config"
	"gemsub/internal/store"
)

type Server struct {
	cfg *config.ServeConfig
	st  *store.Store
}

func New(cfg *config.ServeConfig, st *store.Store) *Server {
	return &Server{cfg: cfg, st: st}
}

// Run starts the HTTP server and blocks until ctx is cancelled or an error occurs.
// When ctx is cancelled, it gracefully shuts down the server with a 5-second timeout.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc(s.cfg.Path, s.handleSub)
	mux.HandleFunc("/healthz", s.handleHealth)

	httpSrv := &http.Server{
		Addr:    s.cfg.Listen,
		Handler: mux,
	}

	serverStopped := make(chan struct{})
	shutdownDone := make(chan error, 1)

	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			shutdownDone <- httpSrv.Shutdown(shutdownCtx)
		case <-serverStopped:
			shutdownDone <- nil
		}
	}()

	slog.Info("subserver: listening", "addr", s.cfg.Listen, "path", s.cfg.Path)
	err := httpSrv.ListenAndServe()
	close(serverStopped)

	if errors.Is(err, http.ErrServerClosed) {
		return <-shutdownDone
	}
	<-shutdownDone
	return err
}

func (s *Server) handleSub(w http.ResponseWriter, r *http.Request) {
	links := s.st.Passing()
	body := strings.Join(links, "\n")

	if s.cfg.Format == "base64" {
		body = base64.StdEncoding.EncodeToString([]byte(body))
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(body))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	stats := s.st.Stats()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(
		"ok\n" +
			"passed: " + strconv.Itoa(stats.Passed) + "\n" +
			"failed: " + strconv.Itoa(stats.Failed) + "\n" +
			"inconclusive: " + strconv.Itoa(stats.Inconclusive) + "\n" +
			"servable: " + strconv.Itoa(stats.Servable) + "\n" +
			"cycles: " + strconv.Itoa(stats.CycleCount) + "\n",
	))
}
