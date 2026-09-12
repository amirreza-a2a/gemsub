package publisher_test

import (
	"testing"

	"gemsub/internal/publisher"
)

func TestParseRemoteURL_Valid(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		protocol publisher.RemoteProtocol
		host     string
		port     int
		path     string
		user     string
	}{
		{
			name:     "SCP SSH with user",
			input:    "git@github.com:owner/repo.git",
			protocol: publisher.ProtocolSSH,
			host:     "github.com",
			port:     22,
			path:     "owner/repo",
			user:     "git",
		},
		{
			name:     "SCP SSH without user",
			input:    "github.com:owner/repo.git",
			protocol: publisher.ProtocolSSH,
			host:     "github.com",
			port:     22,
			path:     "owner/repo",
			user:     "",
		},
		{
			name:     "SSH URI standard",
			input:    "ssh://git@github.com/owner/repo.git",
			protocol: publisher.ProtocolSSH,
			host:     "github.com",
			port:     22,
			path:     "owner/repo",
			user:     "git",
		},
		{
			name:     "SSH URI with custom port",
			input:    "ssh://git@gitlab.example.com:2222/group/subgroup/project.git",
			protocol: publisher.ProtocolSSH,
			host:     "gitlab.example.com",
			port:     2222,
			path:     "group/subgroup/project",
			user:     "git",
		},
		{
			name:     "HTTPS standard",
			input:    "https://github.com/owner/repo.git",
			protocol: publisher.ProtocolHTTPS,
			host:     "github.com",
			port:     443,
			path:     "owner/repo",
			user:     "",
		},
		{
			name:     "HTTPS with default port explicit",
			input:    "https://github.com:443/owner/repo.git",
			protocol: publisher.ProtocolHTTPS,
			host:     "github.com",
			port:     443,
			path:     "owner/repo",
			user:     "",
		},
		{
			name:     "HTTPS with custom port",
			input:    "https://git.corp.net:8443/org/repo.git",
			protocol: publisher.ProtocolHTTPS,
			host:     "git.corp.net",
			port:     8443,
			path:     "org/repo",
			user:     "",
		},
		{
			name:     "HTTP standard",
			input:    "http://git.local/owner/repo.git",
			protocol: publisher.ProtocolHTTP,
			host:     "git.local",
			port:     80,
			path:     "owner/repo",
			user:     "",
		},
		{
			name:     "HTTP with custom port",
			input:    "http://git.local:8080/owner/repo.git",
			protocol: publisher.ProtocolHTTP,
			host:     "git.local",
			port:     8080,
			path:     "owner/repo",
			user:     "",
		},
		{
			name:     "HTTPS with nested path and leading/redundant slashes",
			input:    "https://github.com//group//subgroup/project.git/",
			protocol: publisher.ProtocolHTTPS,
			host:     "github.com",
			port:     443,
			path:     "group/subgroup/project",
			user:     "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rem, err := publisher.ParseRemoteURL(tc.input)
			if err != nil {
				t.Fatalf("ParseRemoteURL(%q) unexpected error: %v", tc.input, err)
			}
			if rem.Protocol != tc.protocol {
				t.Errorf("protocol = %q, want %q", rem.Protocol, tc.protocol)
			}
			if rem.Host != tc.host {
				t.Errorf("host = %q, want %q", rem.Host, tc.host)
			}
			if tc.port != 0 && rem.Port != tc.port {
				t.Errorf("port = %d, want %d", rem.Port, tc.port)
			}
			if rem.Path != tc.path {
				t.Errorf("path = %q, want %q", rem.Path, tc.path)
			}
			if tc.user != "" && rem.User != tc.user {
				t.Errorf("user = %q, want %q", rem.User, tc.user)
			}
		})
	}
}

func TestParseRemoteURL_Invalid(t *testing.T) {
	invalidInputs := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"whitespace only", "   \t\n  "},
		{"unsupported protocol ftp", "ftp://github.com/owner/repo.git"},
		{"unsupported protocol git", "git://github.com/owner/repo.git"},
		{"unsupported protocol git+ssh", "git+ssh://git@github.com/owner/repo.git"},
		{"unsupported file scheme", "file:///var/git/repo.git"},
		{"local absolute filesystem path", "/var/git/repo.git"},
		{"local relative path dot-slash", "./relative/repo.git"},
		{"local relative path dot-dot-slash", "../relative/repo.git"},
		{"query parameter in HTTPS", "https://github.com/owner/repo.git?evil=true"},
		{"query parameter in SSH", "ssh://git@github.com/owner/repo.git?foo=bar"},
		{"fragment in HTTPS", "https://github.com/owner/repo.git#fragment"},
		{"fragment in SSH", "ssh://git@github.com/owner/repo.git#frag"},
		{"traversal dot-dot in HTTPS", "https://github.com/owner/repo/../other.git"},
		{"traversal dot-dot in SSH", "ssh://git@github.com/owner/../../other.git"},
		{"traversal dot-dot in SCP", "git@github.com:owner/../other.git"},
		{"traversal single-dot in HTTPS", "https://github.com/owner/./repo.git"},
		{"traversal single-dot in SCP", "git@github.com:owner/./repo.git"},
		{"missing host in https", "https:///owner/repo.git"},
		{"missing host in ssh", "ssh:///owner/repo.git"},
		{"missing path in scp", "git@github.com:"},
		{"missing path in https", "https://github.com/"},
		{"missing path in https only git", "https://github.com/.git"},
		{"malformed URL bracket", "http://[invalid:host:port"},
		{"invalid port number", "https://github.com:999999/owner/repo.git"},
		{"zero port number", "https://github.com:0/owner/repo.git"},
		{"negative port number", "https://github.com:-1/owner/repo.git"},
		{"encoded slash in HTTPS uppercase %2F", "https://github.com/owner%2Frepo.git"},
		{"encoded slash in HTTPS lowercase %2f", "https://github.com/owner%2frepo.git"},
		{"encoded slash in SSH URI", "ssh://git@github.com/owner%2Frepo.git"},
		{"encoded slash in SCP", "git@github.com:owner%2Frepo.git"},
		{"encoded traversal in HTTPS %2E%2E", "https://github.com/owner/%2E%2E/repo.git"},
		{"encoded traversal in HTTPS %2e%2e", "https://github.com/owner/%2e%2e/repo.git"},
		{"encoded single-dot in HTTPS %2E", "https://github.com/owner/%2E/repo.git"},
		{"encoded traversal in SCP %2E%2E", "git@github.com:owner/%2E%2E/repo.git"},
		{"encoded traversal in SCP %2e%2e", "git@github.com:owner/%2e%2e/repo.git"},
		{"malformed percent-encoding in HTTPS incomplete", "https://github.com/owner/repo%2.git"},
		{"malformed percent-encoding in HTTPS non-hex", "https://github.com/owner/repo%ZZ.git"},
		{"malformed percent-encoding in SCP", "git@github.com:owner/repo%ZZ.git"},
		{"path segment with leading whitespace", "https://github.com/owner/ repo.git"},
		{"path segment with trailing whitespace", "https://github.com/owner/repo .git"},
		{"repository name literally .git in HTTPS", "https://github.com/owner/.git"},
		{"repository name literally .git in SCP", "git@github.com:owner/.git"},
		{"repository name ..git in HTTPS", "https://github.com/owner/..git"},
		{"repository name ...git in SCP", "git@github.com:owner/...git"},
	}

	for _, tc := range invalidInputs {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := publisher.ParseRemoteURL(tc.input); err == nil {
				t.Errorf("ParseRemoteURL(%q) expected error, got nil", tc.input)
			}
			if err := publisher.ValidateRemoteURL(tc.input); err == nil {
				t.Errorf("ValidateRemoteURL(%q) expected error, got nil", tc.input)
			}
		})
	}
}

func TestCanonicalRemoteIdentity_Equivalence(t *testing.T) {
	equivalentGroup := []string{
		"git@github.com:owner/repo.git",
		"ssh://git@github.com/owner/repo.git",
		"ssh://git@github.com:22/owner/repo.git",
		"ssh://github.com/owner/repo.git",
		"https://github.com/owner/repo.git",
		"https://github.com:443/owner/repo.git",
		"https://GitHub.COM/owner/repo.git",
		"https://github.com/owner/repo",
		"https://github.com//owner//repo.git/",
		"http://github.com/owner/repo.git",
		"http://github.com:80/owner/repo.git",
	}

	expectedID := "github.com/owner/repo"

	for _, u := range equivalentGroup {
		id, err := publisher.CanonicalRemoteIdentity(u)
		if err != nil {
			t.Fatalf("CanonicalRemoteIdentity(%q) error: %v", u, err)
		}
		if id != expectedID {
			t.Errorf("CanonicalRemoteIdentity(%q) = %q, want %q", u, id, expectedID)
		}
	}

	for i, u1 := range equivalentGroup {
		for j, u2 := range equivalentGroup {
			if !publisher.SameRepository(u1, u2) {
				t.Errorf("SameRepository(%q, %q) [indices %d, %d] = false, want true", u1, u2, i, j)
			}
		}
	}
}

func TestSameRepository_SafetyBoundaries(t *testing.T) {
	unequalPairs := []struct {
		name string
		urlA string
		urlB string
	}{
		{
			name: "different hosts",
			urlA: "https://github.com/owner/repo.git",
			urlB: "https://gitlab.com/owner/repo.git",
		},
		{
			name: "different paths",
			urlA: "https://github.com/owner/repo-a.git",
			urlB: "https://github.com/owner/repo-b.git",
		},
		{
			name: "different custom port vs default port",
			urlA: "https://github.com:8443/owner/repo.git",
			urlB: "https://github.com/owner/repo.git",
		},
		{
			name: "different SSH custom port vs default port",
			urlA: "ssh://git@github.com:2222/owner/repo.git",
			urlB: "ssh://git@github.com:22/owner/repo.git",
		},
		{
			name: "SSH custom port vs SCP SSH",
			urlA: "ssh://git@github.com:2222/owner/repo.git",
			urlB: "git@github.com:owner/repo.git",
		},
		{
			name: "different SSH hosts",
			urlA: "git@github.com:owner/repo.git",
			urlB: "git@bitbucket.org:owner/repo.git",
		},
	}

	for _, tc := range unequalPairs {
		t.Run(tc.name, func(t *testing.T) {
			if publisher.SameRepository(tc.urlA, tc.urlB) {
				t.Errorf("SameRepository(%q, %q) = true, want false", tc.urlA, tc.urlB)
			}
			if publisher.SameRepository(tc.urlB, tc.urlA) {
				t.Errorf("SameRepository(%q, %q) = true, want false (symmetric)", tc.urlB, tc.urlA)
			}
		})
	}
}

func TestSameRepository_InternalTestHarnessFallback(t *testing.T) {
	// Preserves internal test harness compatibility where bare local repository paths are used
	if !publisher.SameRepository("/tmp/repo.git", "/tmp/repo.git") {
		t.Errorf("expected matching absolute file paths in internal test harness")
	}
	if publisher.SameRepository("/tmp/repo1.git", "/tmp/repo2.git") {
		t.Errorf("expected mismatch between different file paths")
	}
}

func TestSameRepository_FailClosedNegative(t *testing.T) {
	tests := []struct {
		name string
		urlA string
		urlB string
	}{
		{
			name: "identical invalid remote syntax",
			urlA: "invalid-remote",
			urlB: "invalid-remote",
		},
		{
			name: "identical unsupported protocol ftp",
			urlA: "ftp://github.com/owner/repo.git",
			urlB: "ftp://github.com/owner/repo.git",
		},
		{
			name: "identical unsupported protocol file scheme",
			urlA: "file:///var/git/repo.git",
			urlB: "file:///var/git/repo.git",
		},
		{
			name: "identical unsupported protocol git+ssh",
			urlA: "git+ssh://git@github.com/owner/repo.git",
			urlB: "git+ssh://git@github.com/owner/repo.git",
		},
		{
			name: "identical URLs with query parameters in HTTPS",
			urlA: "https://github.com/owner/repo.git?query=1",
			urlB: "https://github.com/owner/repo.git?query=1",
		},
		{
			name: "identical URLs with query parameters in SSH",
			urlA: "ssh://git@github.com/owner/repo.git?query=1",
			urlB: "ssh://git@github.com/owner/repo.git?query=1",
		},
		{
			name: "identical URLs with fragments in HTTPS",
			urlA: "https://github.com/owner/repo.git#fragment",
			urlB: "https://github.com/owner/repo.git#fragment",
		},
		{
			name: "identical URLs with fragments in SSH",
			urlA: "ssh://git@github.com/owner/repo.git#fragment",
			urlB: "ssh://git@github.com/owner/repo.git#fragment",
		},
		{
			name: "identical URLs with traversal dot-dot in HTTPS",
			urlA: "https://github.com/owner/../repo.git",
			urlB: "https://github.com/owner/../repo.git",
		},
		{
			name: "identical URLs with traversal dot-dot in SSH",
			urlA: "ssh://git@github.com/owner/../../repo.git",
			urlB: "ssh://git@github.com/owner/../../repo.git",
		},
		{
			name: "identical URLs with traversal dot-dot in SCP",
			urlA: "git@github.com:owner/../repo.git",
			urlB: "git@github.com:owner/../repo.git",
		},
		{
			name: "identical URLs with encoded traversal in HTTPS",
			urlA: "https://github.com/owner/%2E%2E/repo.git",
			urlB: "https://github.com/owner/%2E%2E/repo.git",
		},
		{
			name: "identical URLs with encoded path separators in HTTPS",
			urlA: "https://github.com/owner%2Frepo.git",
			urlB: "https://github.com/owner%2Frepo.git",
		},
		{
			name: "identical URLs with encoded path separators in SCP",
			urlA: "git@github.com:owner%2Frepo.git",
			urlB: "git@github.com:owner%2Frepo.git",
		},
		{
			name: "identical URLs with segment leading whitespace",
			urlA: "https://github.com/owner/ repo.git",
			urlB: "https://github.com/owner/ repo.git",
		},
		{
			name: "identical URLs with ambiguous .git repository name",
			urlA: "https://github.com/owner/.git",
			urlB: "https://github.com/owner/.git",
		},
		{
			name: "identical URLs with malformed percent-encoding",
			urlA: "https://github.com/owner/repo%ZZ.git",
			urlB: "https://github.com/owner/repo%ZZ.git",
		},
		{
			name: "arbitrary text versus absolute local path",
			urlA: "not-a-path",
			urlB: "/var/git/repo.git",
		},
		{
			name: "absolute local path versus arbitrary text",
			urlA: "/var/git/repo.git",
			urlB: "not-a-path",
		},
		{
			name: "arbitrary text versus relative local path",
			urlA: "not-a-path",
			urlB: "./relative/repo.git",
		},
		{
			name: "relative local path versus arbitrary text",
			urlA: "./relative/repo.git",
			urlB: "not-a-path",
		},
		{
			name: "valid remote versus invalid remote",
			urlA: "https://github.com/owner/repo.git",
			urlB: "invalid-remote",
		},
		{
			name: "invalid remote versus valid remote",
			urlA: "invalid-remote",
			urlB: "https://github.com/owner/repo.git",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if publisher.SameRepository(tc.urlA, tc.urlB) {
				t.Errorf("SameRepository(%q, %q) = true, want false (fail-closed)", tc.urlA, tc.urlB)
			}
		})
	}
}
