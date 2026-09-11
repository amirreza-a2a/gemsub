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
	"sync"
	"time"

	"gemsub/internal/config"
	"gemsub/internal/events"
	"gemsub/internal/store"
)

type Server struct {
	mu  sync.RWMutex
	cfg config.ServeConfig
	st  *store.Store
	bus *events.EventBus
}

func New(cfg *config.ServeConfig, st *store.Store) *Server {
	var c config.ServeConfig
	if cfg != nil {
		c = *cfg
	}
	return &Server{cfg: c, st: st}
}

// SetEventBus sets the event bus for receiving configuration updates.
func (s *Server) SetEventBus(bus *events.EventBus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bus = bus
}

// UpdateConfig updates hot-reloadable configuration in a thread-safe manner.
// Listen and Path changes are logged as requiring restart and not hot-rebound.
func (s *Server) UpdateConfig(cfg config.ServeConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cfg.Listen != s.cfg.Listen || cfg.Path != s.cfg.Path {
		slog.Warn("subserver: listen address or path changed; restart required to apply",
			"current_listen", s.cfg.Listen, "new_listen", cfg.Listen,
			"current_path", s.cfg.Path, "new_path", cfg.Path)
	}
	s.cfg.Format = cfg.Format
}

// Config returns a copy of the current serve configuration.
func (s *Server) Config() config.ServeConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Run starts the HTTP server and blocks until ctx is cancelled or an error occurs.
// When ctx is cancelled, it gracefully shuts down the server with a 5-second timeout.
func (s *Server) Run(ctx context.Context) error {
	s.mu.RLock()
	basePath := s.cfg.Path
	listenAddr := s.cfg.Listen
	bus := s.bus
	s.mu.RUnlock()

	if basePath == "" {
		basePath = "/sub"
	}
	base := strings.TrimRight(basePath, "/")
	if base == "" {
		base = "/"
	}

	mux := http.NewServeMux()
	mux.HandleFunc(base, s.handleSub)
	if base != "/" {
		mux.HandleFunc(base+"/", s.handleSub)
	}
	mux.HandleFunc("/healthz", s.handleHealth)

	httpSrv := &http.Server{
		Addr:    listenAddr,
		Handler: mux,
	}

	if bus != nil {
		configSub := bus.Subscribe()
		defer bus.Unsubscribe(configSub)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case evt, ok := <-configSub:
					if !ok {
						return
					}
					if cu, ok := evt.(config.ConfigUpdated); ok {
						s.UpdateConfig(cu.New.Serve)
					}
				}
			}
		}()
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

	slog.Info("subserver: listening", "addr", listenAddr, "path", basePath)
	err := httpSrv.ListenAndServe()
	close(serverStopped)

	if errors.Is(err, http.ErrServerClosed) {
		return <-shutdownDone
	}
	<-shutdownDone
	return err
}

func (s *Server) handleSub(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	basePath := s.cfg.Path
	defaultFormat := s.cfg.Format
	s.mu.RUnlock()
	if basePath == "" {
		basePath = "/sub"
	}
	base := strings.TrimRight(basePath, "/")
	if base == "" {
		base = "/"
	}

	path := strings.TrimRight(r.URL.Path, "/")
	if path == "" {
		path = "/"
	}

	var isGenericRoute, isGeminiRoute, isBaseRoute bool
	if base == "/" {
		isBaseRoute = (path == "/")
		isGenericRoute = (path == "/generic")
		isGeminiRoute = (path == "/gemini")
	} else {
		isBaseRoute = (path == base)
		isGenericRoute = (path == base+"/generic")
		isGeminiRoute = (path == base+"/gemini")
	}

	if !isBaseRoute && !isGenericRoute && !isGeminiRoute {
		http.NotFound(w, r)
		return
	}

	q := r.URL.Query()

	// Validate projection query parameter if present
	projParam := strings.TrimSpace(strings.ToLower(q.Get("projection")))
	if projParam != "" && projParam != "generic" && projParam != "gemini" {
		http.Error(w, "invalid projection: "+projParam, http.StatusBadRequest)
		return
	}

	// Projection routing precedence:
	// 1. Dedicated route has precedence over query projection.
	// 2. Query projection is used only for the base subscription endpoint.
	// 3. Otherwise default projection is Gemini.
	var projection string
	if isGenericRoute {
		projection = "generic"
	} else if isGeminiRoute {
		projection = "gemini"
	} else if isBaseRoute {
		if projParam == "generic" {
			projection = "generic"
		} else {
			projection = "gemini"
		}
	}

	// Protocol aliases: support ?protocol=... and ?proto=...
	p1 := strings.TrimSpace(strings.ToLower(q.Get("protocol")))
	p2 := strings.TrimSpace(strings.ToLower(q.Get("proto")))
	if p1 != "" && p2 != "" && p1 != p2 {
		http.Error(w, "conflicting protocol and proto parameters", http.StatusBadRequest)
		return
	}
	targetProto := p1
	if targetProto == "" {
		targetProto = p2
	}
	if targetProto != "" {
		if targetProto != "vless" && targetProto != "vmess" && targetProto != "trojan" {
			http.Error(w, "invalid protocol: "+targetProto, http.StatusBadRequest)
			return
		}
	}

	// Format override: support ?format=raw and ?format=base64 (request-local only)
	formatParam := strings.TrimSpace(strings.ToLower(q.Get("format")))
	format := defaultFormat
	if format == "" {
		format = "base64"
	}
	if formatParam != "" {
		if formatParam != "raw" && formatParam != "base64" {
			http.Error(w, "invalid format: "+formatParam, http.StatusBadRequest)
			return
		}
		format = formatParam
	}

	// Sourced directly from authoritative Store projections
	var links []string
	if projection == "generic" {
		links = s.st.NetworkPassing()
	} else {
		links = s.st.Passing()
	}

	// Protocol filtering (if requested)
	if targetProto != "" {
		prefix := targetProto + "://"
		var filtered []string
		for _, link := range links {
			if strings.HasPrefix(link, prefix) {
				filtered = append(filtered, link)
			}
		}
		links = filtered
	}

	body := strings.Join(links, "\n")
	if format == "base64" {
		if body != "" {
			body = base64.StdEncoding.EncodeToString([]byte(body))
		} else {
			body = ""
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
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
			"generic_servable: " + strconv.Itoa(stats.GenericServable) + "\n" +
			"cycles: " + strconv.Itoa(stats.CycleCount) + "\n",
	))
}
