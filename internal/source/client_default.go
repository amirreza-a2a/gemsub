//go:build !android

package source

import (
	"net/http"
	"time"
)

// newHTTPClient creates the standard http.Client on non-Android platforms.
// It preserves standard Go/OS DNS resolution and transport semantics completely.
func newHTTPClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second}
}
