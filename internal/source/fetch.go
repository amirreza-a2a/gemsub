// Package source fetches upstream subscriptions and normalizes them
// into a flat list of raw share links (vless://, vmess://, ...).
package source

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 20 * time.Second}

// FetchAll pulls every source URL and returns the deduplicated union
// of all links found. A single failing source is logged by the
// caller via the returned per-source error, not fatal to the others.
func FetchAll(urls []string) (links []string, errs []error) {
	seen := make(map[string]struct{})

	for _, u := range urls {
		raw, err := fetchOne(u)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", u, err))
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

func fetchOne(u string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	// Some hosts (GitHub raw included) are picky about a missing UA.
	req.Header.Set("User-Agent", "gemsub/1.0")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20)) // 32MB safety cap
	if err != nil {
		return "", err
	}
	return string(body), nil
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
