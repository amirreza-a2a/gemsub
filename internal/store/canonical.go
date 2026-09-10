package store

import (
	"net"
	"net/url"
	"slices"
	"strings"
)

// CanonicalizeLink returns a deterministic, canonical representation of a share link
// suitable for use as the authoritative key in the Store.
// It lowercases the scheme and hostname, sorts query parameters and duplicate values,
// preserves IPv6 bracket notation using net.JoinHostPort,
// and trims whitespace from the fragment while preserving original credentials,
// path, and fragment identity.
func CanonicalizeLink(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	// Handle vmess:// base64 payload
	if len(raw) >= 8 && strings.EqualFold(raw[:8], "vmess://") {
		return "vmess://" + raw[8:]
	}

	frag := ""
	if idx := strings.IndexByte(raw, '#'); idx >= 0 {
		trimmedFrag := strings.TrimSpace(raw[idx+1:])
		if trimmedFrag != "" {
			frag = "#" + trimmedFrag
		}
		raw = raw[:idx]
	}

	u, err := url.Parse(raw)
	if err != nil {
		return raw + frag
	}

	u.Scheme = strings.ToLower(u.Scheme)

	hostname := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" {
		u.Host = net.JoinHostPort(hostname, port)
	} else {
		if strings.Contains(hostname, ":") {
			u.Host = "[" + hostname + "]"
		} else {
			u.Host = hostname
		}
	}

	q := u.Query()
	if len(q) > 0 {
		for _, vals := range q {
			if len(vals) > 1 {
				slices.Sort(vals)
			}
		}
		u.RawQuery = q.Encode()
	}

	return u.String() + frag
}

// CanonicalizeLinks transforms a set of raw candidate links into canonical form.
// Duplicate raw links that share a canonical identity are naturally deduplicated.
// This is a pure transformation that executes without locking Store state.
func CanonicalizeLinks(currentLinks map[string]struct{}) map[string]struct{} {
	if currentLinks == nil {
		return nil
	}
	canonicalPresent := make(map[string]struct{}, len(currentLinks))
	for raw := range currentLinks {
		canonicalPresent[CanonicalizeLink(raw)] = struct{}{}
	}
	return canonicalPresent
}
