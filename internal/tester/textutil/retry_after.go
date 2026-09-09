// Package textutil provides shared text normalization and HTTP header utilities.
// It is a leaf package with no dependencies on tester, gemini, or transport packages.
package textutil

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ParseRetryAfter parses an HTTP Retry-After header value into a time.Duration.
// It supports both delta-seconds (e.g. "120") and HTTP-date formats (RFC 1123, RFC 850, ANSI C).
// Returns nil if the header is empty or cannot be parsed.
func ParseRetryAfter(header string) *time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	if sec, err := strconv.Atoi(header); err == nil && sec >= 0 {
		d := time.Duration(sec) * time.Second
		return &d
	}
	if t, err := http.ParseTime(header); err == nil {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		return &d
	}
	return nil
}
