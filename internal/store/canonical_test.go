package store_test

import (
	"testing"

	"gemsub/internal/parser"
	"gemsub/internal/store"
)

func TestCanonicalizeLink_DeterminismAndSorting(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "query params sorted alphabetically",
			input:    "vless://uuid-1@example.com:443?type=ws&security=tls&path=%2Fws#Node1",
			expected: "vless://uuid-1@example.com:443?path=%2Fws&security=tls&type=ws#Node1",
		},
		{
			name:     "already sorted query params unchanged",
			input:    "vless://uuid-1@example.com:443?path=%2Fws&security=tls&type=ws#Node1",
			expected: "vless://uuid-1@example.com:443?path=%2Fws&security=tls&type=ws#Node1",
		},
		{
			name:     "scheme and host lowercased",
			input:    "VLESS://UUID-1@EXAMPLE.COM:443?type=tcp#Node",
			expected: "vless://UUID-1@example.com:443?type=tcp#Node",
		},
		{
			name:     "duplicate query keys sorted deterministically",
			input:    "trojan://pass@host.com:443?foo=beta&foo=alpha#Tag",
			expected: "trojan://pass@host.com:443?foo=alpha&foo=beta#Tag",
		},
		{
			name:     "whitespace in link trimmed",
			input:    "  vless://uuid@host.com:443?type=tcp#Node  \n",
			expected: "vless://uuid@host.com:443?type=tcp#Node",
		},
		{
			name:     "fragment whitespace trimmed",
			input:    "vless://uuid@host.com:443?type=tcp#  Node Tag  ",
			expected: "vless://uuid@host.com:443?type=tcp#Node Tag",
		},
		{
			name:     "vmess scheme lowercased and base64 preserved",
			input:    "VMESS://eyJhZGQiOiIxLjEuMS4xIn0=",
			expected: "vmess://eyJhZGQiOiIxLjEuMS4xIn0=",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := store.CanonicalizeLink(tc.input)
			if got != tc.expected {
				t.Errorf("CanonicalizeLink(%q)\ngot:  %q\nwant: %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestCanonicalizeLink_ParserCompatibility(t *testing.T) {
	rawLinks := []string{
		"vless://11111111-1111-1111-1111-111111111111@example.com:443?type=ws&security=reality&pbk=pubkey123&sid=1234&sni=example.com&fp=chrome#US-01",
		"trojan://password123@trojan.example.com:443?security=tls&sni=trojan.example.com&type=tcp#Trojan-Node",
		"ss://YWVzLTEyOC1nY206cGFzc3dvcmQ=@1.2.3.4:8388#Shadowsocks-Node",
	}

	for _, raw := range rawLinks {
		canonical := store.CanonicalizeLink(raw)

		// Both raw and canonical must parse successfully in parser.Parse
		candRaw, errRaw := parser.Parse(raw)
		if errRaw != nil {
			t.Fatalf("parser.Parse failed for raw link %q: %v", raw, errRaw)
		}

		candCanonical, errCanonical := parser.Parse(canonical)
		if errCanonical != nil {
			t.Fatalf("parser.Parse failed for canonical link %q: %v", canonical, errCanonical)
		}

		if candRaw.Outbound.Type != candCanonical.Outbound.Type {
			t.Errorf("outbound type mismatch: raw=%s, canonical=%s", candRaw.Outbound.Type, candCanonical.Outbound.Type)
		}
	}
}

func TestCanonicalizeLink_FragmentDistinguishesIdentity(t *testing.T) {
	linkA := "vless://uuid@host.com:443?type=tcp#Node-1"
	linkB := "vless://uuid@host.com:443?type=tcp#Node-2"

	cKeyA := store.CanonicalizeLink(linkA)
	cKeyB := store.CanonicalizeLink(linkB)

	if cKeyA == cKeyB {
		t.Errorf("expected different fragments to produce distinct CanonicalLinks, got identical: %q", cKeyA)
	}
}
