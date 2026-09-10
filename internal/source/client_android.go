//go:build android

package source

import (
	"net/http"
	"time"
)

// newHTTPClient creates an http.Client for Android/Termux environments.
// It detects the Termux resolver configuration at $PREFIX/etc/resolv.conf and configures
// the client's transport dialer to use the discovered nameservers rather than falling back
// to loopback 127.0.0.1:53 in pure-Go (CGO_ENABLED=0) builds.
// If no Termux nameservers are discovered, it falls back to the default transport.
func newHTTPClient() *http.Client {
	path := termuxResolvPath()
	servers, _ := loadResolvConf(path)
	tr := newTermuxTransport(servers)
	if tr == nil {
		return &http.Client{Timeout: 20 * time.Second}
	}
	return &http.Client{
		Timeout:   20 * time.Second,
		Transport: tr,
	}
}
