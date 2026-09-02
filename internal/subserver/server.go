// Package subserver serves the current list of passing configs over
// HTTP in a format Throne's Subscription group type can consume
// directly, so Throne's own auto-update handles refreshing — no
// manual copy/paste, no curl.
package subserver

import (
	"encoding/base64"
	"log"
	"net/http"
	"strconv"
	"strings"

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

// Run starts the HTTP server and blocks until it exits or ctx-like
// shutdown is triggered elsewhere (the caller owns the *http.Server
// lifecycle via Serve's returned error).
func (s *Server) Run() error {
	mux := http.NewServeMux()
	mux.HandleFunc(s.cfg.Path, s.handleSub)
	mux.HandleFunc("/healthz", s.handleHealth)

	log.Printf("subserver: listening on %s%s", s.cfg.Listen, s.cfg.Path)
	return http.ListenAndServe(s.cfg.Listen, mux)
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
			"cycles: " + strconv.Itoa(stats.CycleCount) + "\n",
	))
}
