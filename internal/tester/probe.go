// Package tester probes a single candidate outbound in-process: no
// subprocess, no temp config file, no local listening port. Each
// probe spins up a minimal sing-box Box containing just that one
// outbound, dials the real target through it, and tears the Box
// down again.
//
// NOTE: like internal/parser, this depends on github.com/sagernet/
// sing-box and github.com/sagernet/sing, which this sandbox can't
// fetch. The Box lifecycle calls below (box.New / Start / Outbound()
// / Close()) and the OutboundManager lookup method are based on the
// documented API at https://pkg.go.dev/github.com/sagernet/sing-box
// but have not been compiled against the real module — verify the
// OutboundManager method name (it may be Outbound(tag) or Get(tag)
// depending on version) after `go mod tidy` on your machine.
package tester

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"

	"gemsub/internal/config"
	"gemsub/internal/parser"
	"gemsub/internal/store"
)

// Probe runs one candidate through a fresh Box and returns the
// store.Result to record.
func Probe(ctx context.Context, cand parser.Candidate, cfg *config.TestConfig) store.Result {
	start := time.Now()

	probeCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	result := store.Result{Link: cand.Link, TestedAt: start}

	dialFn, closeBox, err := buildDialer(probeCtx, cand)
	if err != nil {
		result.Reason = fmt.Sprintf("build outbound: %v", err)
		return result
	}
	defer closeBox()

	client := &http.Client{
		Timeout: cfg.Timeout,
		Transport: &http.Transport{
			DialContext:         dialFn,
			TLSHandshakeTimeout: cfg.Timeout,
		},
	}

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, cfg.TargetURL, nil)
	if err != nil {
		result.Reason = fmt.Sprintf("build request: %v", err)
		return result
	}

	resp, err := client.Do(req)
	if err != nil {
		result.Reason = fmt.Sprintf("request failed: %v", err)
		return result
	}
	defer resp.Body.Close()

	result.Latency = time.Since(start)

	if resp.StatusCode != http.StatusOK {
		result.Reason = fmt.Sprintf("unexpected status %d", resp.StatusCode)
		return result
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20)) // 2MB is plenty for this check
	if err != nil {
		result.Reason = fmt.Sprintf("read body: %v", err)
		return result
	}

	lower := strings.ToLower(string(body))
	for _, phrase := range cfg.BlockPhrases {
		if strings.Contains(lower, strings.ToLower(phrase)) {
			result.Reason = "target returned a regional block message"
			return result
		}
	}

	result.Passed = true
	result.Reason = "ok"
	return result
}

// buildDialer spins up a minimal Box containing just this one
// outbound (plus a direct outbound for anything the protocol itself
// needs internally, e.g. DNS) and returns a DialContext-shaped func
// bound to it, plus a cleanup func that tears the Box down.
func buildDialer(ctx context.Context, cand parser.Candidate) (func(context.Context, string, string) (net.Conn, error), func(), error) {
	boxCtx := include.Context(ctx)

	instance, err := box.New(box.Options{
		Context: boxCtx,
		Options: option.Options{
			Log:       &option.LogOptions{Disabled: true},
			Outbounds: []option.Outbound{cand.Outbound, {Type: "direct", Tag: "direct"}},
		},
	})
	if err != nil {
		return nil, func() {}, fmt.Errorf("create box: %w", err)
	}

	if err := instance.Start(); err != nil {
		instance.Close()
		return nil, func() {}, fmt.Errorf("start box: %w", err)
	}

	closeFn := func() { instance.Close() }

	ob, loaded := instance.Outbound().Outbound(cand.Outbound.Tag)
	if !loaded {
		closeFn()
		return nil, func() {}, fmt.Errorf("outbound %q not registered", cand.Outbound.Tag)
	}

	dialFn := func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		dest := M.ParseSocksaddrHostPort(host, parsePort(port))
		return ob.DialContext(ctx, network, dest)
	}

	return dialFn, closeFn, nil
}

func parsePort(s string) uint16 {
	var p uint16
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		p = p*10 + uint16(c-'0')
	}
	return p
}
