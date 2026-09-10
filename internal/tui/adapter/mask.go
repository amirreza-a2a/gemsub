package adapter

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"gemsub/internal/store"
)

// ExtractProtocol returns the protocol identifier from the link scheme.
func ExtractProtocol(link string) string {
	link = strings.TrimSpace(link)
	switch {
	case strings.HasPrefix(link, "vless://"):
		return "vless"
	case strings.HasPrefix(link, "vmess://"):
		return "vmess"
	case strings.HasPrefix(link, "trojan://"):
		return "trojan"
	case strings.HasPrefix(link, "ss://"):
		return "ss"
	default:
		return "unknown"
	}
}

func b64Decode(s string) (string, error) {
	s = strings.TrimSpace(s)
	if pad := len(s) % 4; pad != 0 {
		s += strings.Repeat("=", 4-pad)
	}
	if data, err := base64.StdEncoding.DecodeString(s); err == nil {
		return string(data), nil
	}
	data, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func cleanHostPort(s string) string {
	if i := strings.IndexAny(s, "?/"); i >= 0 {
		s = s[:i]
	}
	return s
}

func splitHostPort(hostport string) (host, port string, err error) {
	hostport = cleanHostPort(hostport)
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		return h, p, nil
	}
	i := strings.LastIndex(hostport, ":")
	if i < 0 {
		return hostport, "", fmt.Errorf("missing port in %q", hostport)
	}
	h := hostport[:i]
	p := hostport[i+1:]
	h = strings.TrimPrefix(strings.TrimSuffix(h, "]"), "[")
	return h, p, nil
}

// FormatHostPort formats host and port using bracketed host:port notation for IPv6 addresses.
func FormatHostPort(host, port string) string {
	if host == "" && port == "" {
		return ""
	}
	cleanHost := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if strings.Contains(cleanHost, ":") {
		host = "[" + cleanHost + "]"
	}
	if port != "" {
		return host + ":" + port
	}
	return host
}

// ExtractEndpoint extracts host:port without any userinfo or passwords.
func ExtractEndpoint(link string) string {
	link = strings.TrimSpace(link)
	if strings.HasPrefix(link, "vmess://") {
		raw := strings.TrimPrefix(link, "vmess://")
		if decoded, err := b64Decode(raw); err == nil {
			var v struct {
				Add  string `json:"add"`
				Port any    `json:"port"`
			}
			if err := json.Unmarshal([]byte(decoded), &v); err == nil && v.Add != "" {
				portStr := ""
				switch p := v.Port.(type) {
				case string:
					portStr = strings.TrimSpace(p)
				case float64:
					portStr = strconv.Itoa(int(p))
				case int:
					portStr = strconv.Itoa(p)
				case json.Number:
					portStr = p.String()
				}
				if portStr != "" {
					return FormatHostPort(v.Add, portStr)
				}
				return v.Add
			}
		}
	}

	if strings.HasPrefix(link, "ss://") {
		raw := strings.TrimPrefix(link, "ss://")
		if idx := strings.Index(raw, "#"); idx >= 0 {
			raw = raw[:idx]
		}
		if at := strings.LastIndex(raw, "@"); at >= 0 {
			return cleanHostPort(raw[at+1:])
		}
		if decoded, err := b64Decode(cleanHostPort(raw)); err == nil {
			if at := strings.LastIndex(decoded, "@"); at >= 0 {
				return cleanHostPort(decoded[at+1:])
			}
		}
		return "ss"
	}

	u, err := url.Parse(link)
	if err == nil && u.Host != "" {
		return u.Host
	}

	// Fallback for non-standard links
	idx := strings.Index(link, "://")
	if idx != -1 {
		rest := link[idx+3:]
		// Strip userinfo if present
		if at := strings.LastIndex(rest, "@"); at != -1 {
			rest = rest[at+1:]
		}
		// Strip query or fragment
		for _, delim := range []string{"?", "#", "/"} {
			if d := strings.Index(rest, delim); d != -1 {
				rest = rest[:d]
			}
		}
		if rest != "" {
			return rest
		}
	}

	return link
}

// ExtractConnectionParams extracts detailed connection parameters (host, port, path, sni)
// without revealing sensitive user credentials or tokens.
func ExtractConnectionParams(link string) (host, port, path, sni string) {
	link = strings.TrimSpace(link)
	if strings.HasPrefix(link, "vmess://") {
		raw := strings.TrimPrefix(link, "vmess://")
		if decoded, err := b64Decode(raw); err == nil {
			var v struct {
				Add  string `json:"add"`
				Port any    `json:"port"`
				Path string `json:"path"`
				Host string `json:"host"`
				SNI  string `json:"sni"`
			}
			if err := json.Unmarshal([]byte(decoded), &v); err == nil {
				host = v.Add
				switch p := v.Port.(type) {
				case string:
					port = strings.TrimSpace(p)
				case float64:
					port = strconv.Itoa(int(p))
				case int:
					port = strconv.Itoa(p)
				case json.Number:
					port = p.String()
				}
				path = v.Path
				sni = v.SNI
				if sni == "" {
					sni = v.Host
				}
				return host, port, path, sni
			}
		}
	}

	if strings.HasPrefix(link, "ss://") {
		endpoint := ExtractEndpoint(link)
		h, p, _ := splitHostPort(endpoint)
		return h, p, "", ""
	}

	u, err := url.Parse(link)
	if err == nil {
		host = u.Hostname()
		port = u.Port()
		q := u.Query()
		path = q.Get("path")
		if path == "" {
			path = u.Path
		}
		sni = q.Get("sni")
		if sni == "" {
			sni = q.Get("peer")
		}
		if sni == "" {
			sni = q.Get("host")
		}
		return host, port, path, sni
	}

	return "", "", "", ""
}

// ExtractRemark returns the unescaped remark/tag (URL fragment) if present.
func ExtractRemark(link string) string {
	u, err := url.Parse(link)
	if err == nil && u.Fragment != "" {
		if unescaped, err := url.QueryUnescape(u.Fragment); err == nil {
			return unescaped
		}
		return u.Fragment
	}

	if idx := strings.Index(link, "#"); idx != -1 {
		rem := link[idx+1:]
		if unescaped, err := url.QueryUnescape(rem); err == nil {
			return unescaped
		}
		return rem
	}

	return ""
}

// MaskLink redacts passwords, private UUIDs, and sensitive credentials for safe terminal display.
func MaskLink(link string) string {
	link = strings.TrimSpace(link)
	if link == "" {
		return ""
	}

	if strings.HasPrefix(link, "vmess://") {
		// VMess link: decode JSON, redact id, and re-format
		raw := strings.TrimPrefix(link, "vmess://")
		decoded, err := b64Decode(raw)
		if err == nil {
			var m map[string]any
			if err := json.Unmarshal([]byte(decoded), &m); err == nil {
				m["id"] = "[REDACTED]"
				if bytes, err := json.Marshal(m); err == nil {
					return "vmess://" + base64.StdEncoding.EncodeToString(bytes)
				}
			}
		}
		return "vmess://[REDACTED]"
	}

	if strings.HasPrefix(link, "ss://") {
		raw := strings.TrimPrefix(link, "ss://")
		remark := ""
		if idx := strings.Index(raw, "#"); idx >= 0 {
			remark = raw[idx:]
			raw = raw[:idx]
		}

		if at := strings.LastIndex(raw, "@"); at >= 0 {
			// SIP002 with @: ss://userinfo@host:port...
			after := raw[at+1:]
			return "ss://[REDACTED]@" + after + remark
		}

		// Legacy base64 Shadowsocks: ss://base64(method:password@host:port)...
		decoded, err := b64Decode(cleanHostPort(raw))
		if err == nil {
			if at := strings.LastIndex(decoded, "@"); at >= 0 {
				hostport := decoded[at+1:]
				return "ss://[REDACTED]@" + hostport + remark
			}
		}
		return "ss://[REDACTED]" + remark
	}

	u, err := url.Parse(link)
	if err == nil {
		// Redact UserInfo
		if u.User != nil {
			u.User = url.User("[REDACTED]")
		}
		// Redact sensitive query parameters if present
		q := u.Query()
		sensitiveKeys := []string{"key", "secret", "password", "token", "auth"}
		modifiedQuery := false
		for _, k := range sensitiveKeys {
			if q.Has(k) {
				q.Set(k, "[REDACTED]")
				modifiedQuery = true
			}
		}
		if modifiedQuery {
			u.RawQuery = q.Encode()
		}
		res := u.String()
		res = strings.ReplaceAll(res, "%5BREDACTED%5D", "[REDACTED]")
		return res
	}

	// Fallback regex/string masking
	if at := strings.Index(link, "@"); at != -1 {
		schemeIdx := strings.Index(link, "://")
		if schemeIdx != -1 && schemeIdx+3 < at {
			return link[:schemeIdx+3] + "[REDACTED]" + link[at:]
		}
	}

	return "[REDACTED]"
}

// FormatHistoryGlyphs formats bounded history samples into a compact glyph string e.g. "[●●○×······]".
func FormatHistoryGlyphs(samples []store.ProbeSample, capacity int) string {
	if capacity <= 0 {
		capacity = 10
	}

	var sb strings.Builder
	sb.WriteRune('[')

	for _, s := range samples {
		switch s.Status {
		case store.StatusPassed:
			sb.WriteRune('●')
		case store.StatusFailed:
			sb.WriteRune('×')
		case store.StatusInconclusive:
			sb.WriteRune('○')
		default:
			sb.WriteRune('·')
		}
	}

	// Pad remaining capacity with dim dot
	for i := len(samples); i < capacity; i++ {
		sb.WriteRune('·')
	}

	sb.WriteRune(']')
	return sb.String()
}

// FormatHistoryGlyphsFromStatuses formats bounded history sample statuses into a compact glyph string e.g. "[●●○×······]".
func FormatHistoryGlyphsFromStatuses(statuses [16]byte, count, capacity int) string {
	if capacity <= 0 {
		capacity = 10
	}
	if count > 16 {
		count = 16
	}

	var sb strings.Builder
	sb.Grow(capacity*3 + 2)
	sb.WriteRune('[')

	for i := 0; i < count; i++ {
		switch statuses[i] {
		case 'P':
			sb.WriteRune('●')
		case 'F':
			sb.WriteRune('×')
		case 'I':
			sb.WriteRune('○')
		default:
			sb.WriteRune('·')
		}
	}

	// Pad remaining capacity with dim dot
	for i := count; i < capacity; i++ {
		sb.WriteRune('·')
	}

	sb.WriteRune(']')
	return sb.String()
}
