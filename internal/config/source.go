package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// SourceItem represents a configured subscription source.
type SourceItem struct {
	ID      string    `json:"id"`
	URL     string    `json:"url"`
	Name    string    `json:"name,omitempty"`
	Enabled bool      `json:"enabled"`
	AddedAt time.Time `json:"added_at,omitempty"`
}

// Source is an alias for SourceItem.
type Source = SourceItem

// UnmarshalJSON unmarshals a SourceItem from either a legacy string URL
// or a structured JSON object. Legacy string entries default to Enabled: true.
func (s *SourceItem) UnmarshalJSON(data []byte) error {
	// 1. Check if the element is a legacy JSON string (e.g. "https://...")
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		normURL, err := NormalizeURL(str)
		if err != nil {
			normURL = strings.TrimSpace(str)
		}
		s.URL = normURL
		s.Enabled = true
		s.ID = GenerateSourceID(s.URL)
		s.Name = DefaultSourceName(s.URL)
		s.AddedAt = time.Now().UTC()
		return nil
	}

	// 2. Otherwise unmarshal as structured object
	type plain struct {
		ID      string     `json:"id"`
		URL     string     `json:"url"`
		Name    string     `json:"name"`
		Enabled *bool      `json:"enabled"`
		AddedAt *time.Time `json:"added_at"`
	}
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}

	normURL, err := NormalizeURL(p.URL)
	if err != nil {
		normURL = strings.TrimSpace(p.URL)
	}

	s.ID = p.ID
	s.URL = normURL
	s.Name = p.Name
	if p.Enabled != nil {
		s.Enabled = *p.Enabled
	} else {
		// Default to enabled if omitted in JSON
		s.Enabled = true
	}
	if p.AddedAt != nil {
		s.AddedAt = *p.AddedAt
	} else {
		s.AddedAt = time.Now().UTC()
	}
	if s.ID == "" {
		s.ID = GenerateSourceID(s.URL)
	}
	if s.Name == "" {
		s.Name = DefaultSourceName(s.URL)
	}
	return nil
}

// NormalizeURL validates and normalizes a source URL by trimming whitespace,
// lowercasing the scheme and host, and stripping default ports.
func NormalizeURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("source URL must not be empty")
	}

	u, err := url.ParseRequestURI(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid source URL %q: %w", raw, err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("invalid source URL %q: scheme must be http or https, got %q", raw, u.Scheme)
	}

	host := strings.ToLower(u.Host)
	if host == "" {
		return "", fmt.Errorf("invalid source URL %q: host must not be empty", raw)
	}

	// Strip default ports
	if scheme == "http" && strings.HasSuffix(host, ":80") {
		host = strings.TrimSuffix(host, ":80")
	} else if scheme == "https" && strings.HasSuffix(host, ":443") {
		host = strings.TrimSuffix(host, ":443")
	}

	u.Scheme = scheme
	u.Host = host

	return u.String(), nil
}

// GenerateSourceID creates a stable deterministic source ID from a normalized URL.
func GenerateSourceID(normURL string) string {
	sum := sha256.Sum256([]byte(normURL))
	return "src_" + hex.EncodeToString(sum[:6])
}

// DefaultSourceName extracts a sensible default display name from a normalized URL.
func DefaultSourceName(normURL string) string {
	u, err := url.Parse(normURL)
	if err != nil || u.Host == "" {
		return normURL
	}
	return u.Host
}

// NewSource creates a SourceItem with normalized URL, generated ID, and Enabled set to true.
func NewSource(rawURL string, name ...string) SourceItem {
	normURL, err := NormalizeURL(rawURL)
	if err != nil {
		normURL = strings.TrimSpace(rawURL)
	}
	id := GenerateSourceID(normURL)
	var displayName string
	if len(name) > 0 && name[0] != "" {
		displayName = name[0]
	} else {
		displayName = DefaultSourceName(normURL)
	}
	return SourceItem{
		ID:      id,
		URL:     normURL,
		Name:    displayName,
		Enabled: true,
		AddedAt: time.Now().UTC(),
	}
}

// NewSources creates a slice of SourceItems from raw URLs.
func NewSources(urls ...string) []SourceItem {
	res := make([]SourceItem, len(urls))
	for i, u := range urls {
		res[i] = NewSource(u)
	}
	return res
}

// EnabledSourceURLs returns the URLs of all enabled sources in order.
func (c *Config) EnabledSourceURLs() []string {
	if c == nil {
		return nil
	}
	var urls []string
	for _, s := range c.Sources {
		if s.Enabled {
			urls = append(urls, s.URL)
		}
	}
	return urls
}

// SourceURLs returns the URLs of all sources regardless of enabled state.
func (c *Config) SourceURLs() []string {
	if c == nil {
		return nil
	}
	var urls []string
	for _, s := range c.Sources {
		urls = append(urls, s.URL)
	}
	return urls
}
