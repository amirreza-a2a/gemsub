package publisher

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// RemoteProtocol represents the transport protocol of a Git remote.
type RemoteProtocol string

const (
	ProtocolSSH   RemoteProtocol = "ssh"
	ProtocolHTTPS RemoteProtocol = "https"
	ProtocolHTTP  RemoteProtocol = "http"
)

// GitRemote represents a parsed Git remote specification.
type GitRemote struct {
	Raw      string
	Protocol RemoteProtocol
	User     string
	Host     string
	Port     int
	Path     string // Normalized repository path (without leading/trailing slashes or .git suffix)
}

// scpRegex matches SCP-like Git remote syntax: [user@]host:path
// e.g. "git@github.com:owner/repo.git", "github.com:owner/repo.git"
var scpRegex = regexp.MustCompile(`^(?:([a-zA-Z0-9_.-]+)@)?([a-zA-Z0-9_.-]+):(\S+)$`)

// normalizeRepoPath validates and normalizes a repository path.
// It rejects paths containing "." or ".." traversal segments, including encoded forms (%2e, %2e%2e).
// It rejects encoded path separators (%2F, %2f).
// It rejects path segments with leading or trailing whitespace.
// It trims surrounding slashes, collapses redundant consecutive slashes,
// and strips one optional ".git" suffix from a named repository component.
// Paths where the repository name itself is ".git", empty, or traversal are rejected.
func normalizeRepoPath(p string) (string, error) {
	trimmed := strings.TrimSpace(p)
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return "", fmt.Errorf("remote repository path must not be empty")
	}

	if strings.Contains(strings.ToLower(trimmed), "%2f") {
		return "", fmt.Errorf("remote repository path must not contain encoded path separators (%%2F)")
	}

	segments := strings.Split(trimmed, "/")
	var validSegments []string
	for _, seg := range segments {
		if seg == "" {
			continue
		}
		if seg != strings.TrimSpace(seg) {
			return "", fmt.Errorf("remote repository path segment must not contain leading or trailing whitespace: %q", seg)
		}
		lowerSeg := strings.ToLower(seg)
		if lowerSeg == "." || lowerSeg == ".." || lowerSeg == "%2e" || lowerSeg == "%2e%2e" {
			return "", fmt.Errorf("remote repository path must not contain %q traversal segment", seg)
		}
		if strings.Contains(seg, "%") {
			if _, err := url.PathUnescape(seg); err != nil {
				return "", fmt.Errorf("invalid percent-encoding in repository path segment %q: %w", seg, err)
			}
		}
		validSegments = append(validSegments, seg)
	}

	if len(validSegments) == 0 {
		return "", fmt.Errorf("remote repository path must not be empty")
	}

	// Strip .git suffix only from the final named repository component
	lastIdx := len(validSegments) - 1
	lastSeg := validSegments[lastIdx]
	if lastSeg == ".git" {
		return "", fmt.Errorf("remote repository name must not be %q", lastSeg)
	}
	if strings.HasSuffix(lastSeg, ".git") {
		stripped := strings.TrimSuffix(lastSeg, ".git")
		if stripped == "" || stripped == "." || stripped == ".." {
			return "", fmt.Errorf("remote repository name must not be empty or traversal after stripping .git: %q", lastSeg)
		}
		if stripped != strings.TrimSpace(stripped) {
			return "", fmt.Errorf("remote repository name must not contain trailing whitespace: %q", lastSeg)
		}
		validSegments[lastIdx] = stripped
	}

	norm := strings.Join(validSegments, "/")
	if norm == "" {
		return "", fmt.Errorf("remote repository path must not be empty")
	}

	return norm, nil
}

// isDefaultPort checks whether a port matches the default port for the given protocol.
func isDefaultPort(proto RemoteProtocol, port int) bool {
	switch proto {
	case ProtocolSSH:
		return port == 22 || port == 0
	case ProtocolHTTPS:
		return port == 443 || port == 0
	case ProtocolHTTP:
		return port == 80 || port == 0
	default:
		return port == 0
	}
}

// ParseRemoteURL parses a raw Git remote specification into a structured GitRemote.
// Supported formats are strictly network Git remotes:
//   - SCP-style SSH: git@host:owner/repo.git
//   - SSH URI: ssh://[user@]host[:port]/owner/repo.git
//   - HTTPS: https://[user[:pass]@]host[:port]/owner/repo.git
//   - HTTP: http://[user[:pass]@]host[:port]/owner/repo.git
//
// Local filesystem paths (e.g. /..., ./..., ../...) and file:// URIs are rejected.
// Query parameters and fragments are rejected.
func ParseRemoteURL(raw string) (*GitRemote, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("remote URL must not be empty")
	}

	// 1. Explicitly reject local filesystem paths
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "\\") ||
		strings.HasPrefix(trimmed, "./") || strings.HasPrefix(trimmed, ".\\") ||
		strings.HasPrefix(trimmed, "../") || strings.HasPrefix(trimmed, "..\\") {
		return nil, fmt.Errorf("local filesystem paths are not supported as remote URLs: %q", raw)
	}

	// 2. Check for URI syntax (contains "://")
	if strings.Contains(trimmed, "://") {
		u, err := url.Parse(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid remote URL %q: %w", raw, err)
		}

		if u.RawQuery != "" {
			return nil, fmt.Errorf("remote URL must not contain query parameters: %q", raw)
		}
		if u.Fragment != "" {
			return nil, fmt.Errorf("remote URL must not contain fragments: %q", raw)
		}

		if strings.Contains(strings.ToLower(trimmed), "%2f") {
			return nil, fmt.Errorf("remote URL must not contain encoded path separators (%%2F): %q", raw)
		}
		if u.RawPath != "" {
			return nil, fmt.Errorf("remote URL contains ambiguous percent-encoded path: %q", raw)
		}

		scheme := strings.ToLower(u.Scheme)
		var proto RemoteProtocol
		var defaultPort int

		switch scheme {
		case "ssh":
			proto = ProtocolSSH
			defaultPort = 22
		case "https":
			proto = ProtocolHTTPS
			defaultPort = 443
		case "http":
			proto = ProtocolHTTP
			defaultPort = 80
		default:
			return nil, fmt.Errorf("unsupported remote protocol %q", u.Scheme)
		}

		host := strings.ToLower(u.Hostname())
		if host == "" {
			return nil, fmt.Errorf("remote host must not be empty")
		}

		port := 0
		if portStr := u.Port(); portStr != "" {
			p, err := strconv.Atoi(portStr)
			if err != nil || p <= 0 || p > 65535 {
				return nil, fmt.Errorf("invalid remote port %q", portStr)
			}
			port = p
		} else {
			port = defaultPort
		}

		var user string
		if u.User != nil {
			user = u.User.Username()
		}

		normPath, err := normalizeRepoPath(u.Path)
		if err != nil {
			return nil, err
		}

		return &GitRemote{
			Raw:      raw,
			Protocol: proto,
			User:     user,
			Host:     host,
			Port:     port,
			Path:     normPath,
		}, nil
	}

	// 3. Check for SCP-like syntax: [user@]host:path
	if m := scpRegex.FindStringSubmatch(trimmed); m != nil {
		user := m[1]
		host := strings.ToLower(m[2])
		rawPath := m[3]

		// Disallow Windows drive letters like C:\path, C:/path, C:path
		if len(host) == 1 && isDriveLetter(host[0]) {
			return nil, fmt.Errorf("local filesystem paths are not supported as remote URLs: %q", raw)
		}

		normPath, err := normalizeRepoPath(rawPath)
		if err != nil {
			return nil, err
		}

		return &GitRemote{
			Raw:      raw,
			Protocol: ProtocolSSH,
			User:     user,
			Host:     host,
			Port:     22,
			Path:     normPath,
		}, nil
	}

	return nil, fmt.Errorf("invalid remote URL syntax %q", raw)
}

// CanonicalRemoteIdentity computes a canonical repository identity string for a Git remote.
// The identity is normalized such that semantically equivalent remotes across protocols
// (e.g. SSH and HTTPS for the same repository) produce the exact same identity string.
func CanonicalRemoteIdentity(raw string) (string, error) {
	remote, err := ParseRemoteURL(raw)
	if err != nil {
		return "", err
	}

	host := strings.ToLower(remote.Host)
	portPart := ""
	if remote.Port > 0 && !isDefaultPort(remote.Protocol, remote.Port) {
		portPart = fmt.Sprintf(":%d", remote.Port)
	}

	return fmt.Sprintf("%s%s/%s", host, portPart, remote.Path), nil
}

// SameRepository checks whether two Git remote specifications identify the same repository.
// It matches canonical repository identities across equivalent transport protocols (SSH, HTTPS, HTTP)
// while continuing to enforce host, path, and non-default port safety boundaries.
// If either remote is invalid or malformed, SameRepository fails closed and returns false.
func SameRepository(remoteA, remoteB string) bool {
	a := strings.TrimSpace(remoteA)
	b := strings.TrimSpace(remoteB)
	if a == "" || b == "" {
		return false
	}

	idA, errA := CanonicalRemoteIdentity(a)
	idB, errB := CanonicalRemoteIdentity(b)
	if errA == nil && errB == nil {
		return idA == idB
	}

	// Fallback for bare or relative local filesystem paths in internal test harnesses.
	// Both operands must independently satisfy the local-path fallback predicate.
	if isLocalTestPath(a) && isLocalTestPath(b) {
		if isWindowsLocalPath(a) && isWindowsLocalPath(b) {
			cleanA := filepath.Clean(strings.ReplaceAll(a, "/", "\\"))
			cleanB := filepath.Clean(strings.ReplaceAll(b, "/", "\\"))
			return strings.EqualFold(cleanA, cleanB)
		}
		if isWindowsLocalPath(a) || isWindowsLocalPath(b) {
			return false
		}
		return filepath.Clean(a) == filepath.Clean(b)
	}

	return false
}

func isLocalTestPath(p string) bool {
	if p == "" {
		return false
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") || strings.HasPrefix(p, ".") {
		return true
	}
	if len(p) >= 2 && isDriveLetter(p[0]) && p[1] == ':' {
		return true
	}
	return false
}

func isWindowsLocalPath(p string) bool {
	if len(p) >= 2 && isDriveLetter(p[0]) && p[1] == ':' {
		return true
	}
	if strings.HasPrefix(p, `\\`) {
		return true
	}
	return false
}

func isDriveLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// ValidateRemoteURL validates that a string is a supported, syntactically valid Git remote specification.
func ValidateRemoteURL(raw string) error {
	_, err := ParseRemoteURL(raw)
	return err
}
