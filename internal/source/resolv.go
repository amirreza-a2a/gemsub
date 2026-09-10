package source

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

const maxNameservers = 3

// parseResolvConf parses nameservers from standard resolv.conf formatted input.
// Directives other than "nameserver" and invalid IPs are ignored.
// It returns up to maxNameservers formatted as "host:port" (default port 53).
func parseResolvConf(r io.Reader) []string {
	if r == nil {
		return nil
	}

	var servers []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		// Strip comments starting with '#' or ';'
		if idx := strings.IndexAny(line, "#;"); idx != -1 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}

		rawAddr := fields[1]
		formatted := formatNameserver(rawAddr)
		if formatted == "" {
			continue
		}

		servers = append(servers, formatted)
		if len(servers) >= maxNameservers {
			break
		}
	}
	return servers
}

func formatNameserver(addr string) string {
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	if ip := net.ParseIP(addr); ip != nil {
		return net.JoinHostPort(addr, "53")
	}
	return ""
}

// loadResolvConf reads and parses the resolv.conf file at path.
func loadResolvConf(path string) ([]string, error) {
	if path == "" {
		return nil, os.ErrNotExist
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseResolvConf(f), nil
}

// termuxResolvPath locates the active resolv.conf file in Termux/Android environments.
// It checks $PREFIX/etc/resolv.conf, then the standard Termux root path, and finally /etc/resolv.conf.
func termuxResolvPath() string {
	if prefix := os.Getenv("PREFIX"); prefix != "" {
		p := filepath.Join(prefix, "etc", "resolv.conf")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	const standardTermux = "/data/data/com.termux/files/usr/etc/resolv.conf"
	if _, err := os.Stat(standardTermux); err == nil {
		return standardTermux
	}
	const standardLinux = "/etc/resolv.conf"
	if _, err := os.Stat(standardLinux); err == nil {
		return standardLinux
	}
	return ""
}

// newResolvDialer builds a net.Dialer equipped with a pure-Go net.Resolver
// that directs DNS queries across the provided nameservers in round-robin order.
func newResolvDialer(servers []string) *net.Dialer {
	if len(servers) == 0 {
		return &net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}
	}

	var counter uint32
	dnsDialer := &net.Dialer{
		Timeout: 5 * time.Second,
	}

	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			idx := atomic.AddUint32(&counter, 1) - 1
			target := servers[idx%uint32(len(servers))]
			return dnsDialer.DialContext(ctx, network, target)
		},
	}

	return &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Resolver:  resolver,
	}
}

// newTermuxTransport constructs an http.Transport configured with environment proxy support,
// HTTP/2 attempt, standard connection pooling, and idle timeouts, using the discovered
// nameservers for DNS resolution.
// If servers is empty, it returns nil to indicate the default transport should be used.
func newTermuxTransport(servers []string) http.RoundTripper {
	if len(servers) == 0 {
		return nil
	}

	dialer := newResolvDialer(servers)
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}
