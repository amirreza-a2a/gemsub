// Package source fetches upstream subscriptions and normalizes them
// into a flat list of raw share links (vless://, vmess://, ...).
package source

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var httpClient = newHTTPClient()

// FetchAll pulls every source URL and returns the deduplicated union
// of all links found. A single failing source is logged by the
// caller via the returned per-source error, not fatal to the others.
// If the context is cancelled, fetching stops immediately and the cancellation
// error is returned.
func FetchAll(ctx context.Context, urls []string) (links []string, errs []error) {
	if err := ctx.Err(); err != nil {
		return nil, []error{err}
	}
	seen := make(map[string]struct{})

	for _, u := range urls {
		if err := ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", u, err))
			break
		}
		raw, err := fetchOne(ctx, u)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", u, err))
			if ctx.Err() != nil {
				break
			}
			continue
		}
		for _, link := range splitLinks(raw) {
			if _, dup := seen[link]; dup {
				continue
			}
			seen[link] = struct{}{}
			links = append(links, link)
		}
	}

	return links, errs
}

func fetchOne(ctx context.Context, u string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(1 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return "", err // not retryable
		}
		req.Header.Set("User-Agent", "gemsub/1.0")

		resp, err := httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			lastErr = err // network/TLS error → transient, retry
			continue
		}

		if resp.StatusCode == http.StatusOK {
			body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20)) // 32MB safety cap
			resp.Body.Close()
			if err != nil {
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				lastErr = err
				continue
			}
			return string(body), nil
		}

		resp.Body.Close()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		statusErr := fmt.Errorf("unexpected status %d", resp.StatusCode)

		// Only retry transient HTTP statuses: 429 and 5xx.
		// Permanent client errors (400, 401, 403, 404, etc.) are not retried.
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			lastErr = statusErr
			continue
		}
		return "", statusErr // permanent error, no retry
	}
	return "", lastErr
}

// splitLinks handles both plain link-per-line subscriptions and
// whole-blob base64-encoded ones (the common "sub" format), same
// heuristic as before: if the first non-empty line doesn't look like
// a share link, assume the whole thing is base64.
func splitLinks(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	firstLine := strings.TrimSpace(strings.SplitN(raw, "\n", 2)[0])
	if !strings.Contains(firstLine, "://") {
		if decoded, err := b64Decode(strings.ReplaceAll(raw, "\n", "")); err == nil {
			raw = decoded
		}
	}

	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "://") {
			continue
		}
		out = append(out, line)
	}
	return out
}

func b64Decode(s string) (string, error) {
	s = strings.TrimSpace(s)
	if pad := len(s) % 4; pad != 0 {
		s += strings.Repeat("=", 4-pad)
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		data, err = base64.URLEncoding.DecodeString(s)
		if err != nil {
			return "", err
		}
	}
	return string(data), nil
}
