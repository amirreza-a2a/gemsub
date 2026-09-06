package store_test

import (
	"testing"

	"github.com/sagernet/sing-box/option"

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
		{
			name:     "ipv6 with port preserves brackets and lowercases hex",
			input:    "vless://uuid-ipv6@[2001:DB8::1]:443?type=tcp#Node-IPv6",
			expected: "vless://uuid-ipv6@[2001:db8::1]:443?type=tcp#Node-IPv6",
		},
		{
			name:     "ipv6 without port preserves brackets",
			input:    "vless://uuid-ipv6@[2001:DB8::1]?type=tcp#Node-IPv6",
			expected: "vless://uuid-ipv6@[2001:db8::1]?type=tcp#Node-IPv6",
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

func TestCanonicalizeLink_IPv6ParserCompatibility(t *testing.T) {
	ipv6Links := []string{
		"vless://11111111-1111-1111-1111-111111111111@[2001:db8::1]:443?type=ws&security=tls#IPv6-Port",
		"trojan://password123@[2001:db8::1]:443?security=tls&type=tcp#Trojan-IPv6",
	}

	for _, raw := range ipv6Links {
		canonical := store.CanonicalizeLink(raw)
		cand, err := parser.Parse(canonical)
		if err != nil {
			t.Fatalf("parser.Parse failed for canonical IPv6 link %q: %v", canonical, err)
		}
		if cand.Outbound.Type == "" {
			t.Fatalf("expected non-empty outbound type for %q", canonical)
		}
	}
}

func TestCanonicalizeLink_ParserCompatibility(t *testing.T) {
	rawLinks := []string{
		"vless://11111111-1111-1111-1111-111111111111@example.com:443?flow=xtls-rprx-vision&fp=chrome&host=example.com&path=%2Fws&pbk=pubkey123&security=reality&sid=1234&sni=example.com&type=ws#US-01",
		"trojan://password123@trojan.example.com:443?security=tls&sni=trojan.example.com&type=tcp#Trojan-Node",
		"ss://YWVzLTEyOC1nY206cGFzc3dvcmQ=@1.2.3.4:8388#Shadowsocks-Node",
		"vmess://eyJhZGQiOiIxLjIuMy40IiwicG9ydCI6IjQ0MyIsImlkIjoiMTExMTExMTEtMTExMS0xMTExLTExMTEtMTExMTExMTExMTExIiwibmV0Ijoid3MiLCJwYXRoIjoiL3dzIiwidGxzIjoidGxzIiwic25pIjoiZXhhbXBsZS5jb20ifQ==",
	}

	for _, raw := range rawLinks {
		canonical := store.CanonicalizeLink(raw)
		assertOutboundsSemanticallyEqual(t, raw, canonical)
	}
}

func assertOutboundsSemanticallyEqual(t *testing.T, raw, canonical string) {
	candRaw, err := parser.Parse(raw)
	if err != nil {
		t.Fatalf("parser.Parse(raw) failed for %q: %v", raw, err)
	}
	candCanon, err := parser.Parse(canonical)
	if err != nil {
		t.Fatalf("parser.Parse(canonical) failed for %q: %v", canonical, err)
	}

	if candRaw.Outbound.Type != candCanon.Outbound.Type {
		t.Fatalf("outbound type mismatch: raw=%s, canonical=%s", candRaw.Outbound.Type, candCanon.Outbound.Type)
	}

	switch candRaw.Outbound.Type {
	case "vless":
		oRaw := candRaw.Outbound.Options.(*option.VLESSOutboundOptions)
		oCanon := candCanon.Outbound.Options.(*option.VLESSOutboundOptions)
		if oRaw.Server != oCanon.Server {
			t.Errorf("vless Server mismatch: raw=%s, canon=%s", oRaw.Server, oCanon.Server)
		}
		if oRaw.ServerPort != oCanon.ServerPort {
			t.Errorf("vless ServerPort mismatch: raw=%d, canon=%d", oRaw.ServerPort, oCanon.ServerPort)
		}
		if oRaw.UUID != oCanon.UUID {
			t.Errorf("vless UUID mismatch: raw=%s, canon=%s", oRaw.UUID, oCanon.UUID)
		}
		if oRaw.Flow != oCanon.Flow {
			t.Errorf("vless Flow mismatch: raw=%s, canon=%s", oRaw.Flow, oCanon.Flow)
		}
		assertTLSOptionsEqual(t, oRaw.TLS, oCanon.TLS)
		assertTransportOptionsEqual(t, oRaw.Transport, oCanon.Transport)

	case "trojan":
		oRaw := candRaw.Outbound.Options.(*option.TrojanOutboundOptions)
		oCanon := candCanon.Outbound.Options.(*option.TrojanOutboundOptions)
		if oRaw.Server != oCanon.Server {
			t.Errorf("trojan Server mismatch: raw=%s, canon=%s", oRaw.Server, oCanon.Server)
		}
		if oRaw.ServerPort != oCanon.ServerPort {
			t.Errorf("trojan ServerPort mismatch: raw=%d, canon=%d", oRaw.ServerPort, oCanon.ServerPort)
		}
		if oRaw.Password != oCanon.Password {
			t.Errorf("trojan Password mismatch: raw=%s, canon=%s", oRaw.Password, oCanon.Password)
		}
		assertTLSOptionsEqual(t, oRaw.TLS, oCanon.TLS)
		assertTransportOptionsEqual(t, oRaw.Transport, oCanon.Transport)

	case "vmess":
		oRaw := candRaw.Outbound.Options.(*option.VMessOutboundOptions)
		oCanon := candCanon.Outbound.Options.(*option.VMessOutboundOptions)
		if oRaw.Server != oCanon.Server {
			t.Errorf("vmess Server mismatch: raw=%s, canon=%s", oRaw.Server, oCanon.Server)
		}
		if oRaw.ServerPort != oCanon.ServerPort {
			t.Errorf("vmess ServerPort mismatch: raw=%d, canon=%d", oRaw.ServerPort, oCanon.ServerPort)
		}
		if oRaw.UUID != oCanon.UUID {
			t.Errorf("vmess UUID mismatch: raw=%s, canon=%s", oRaw.UUID, oCanon.UUID)
		}
		assertTLSOptionsEqual(t, oRaw.TLS, oCanon.TLS)
		assertTransportOptionsEqual(t, oRaw.Transport, oCanon.Transport)

	case "shadowsocks":
		oRaw := candRaw.Outbound.Options.(*option.ShadowsocksOutboundOptions)
		oCanon := candCanon.Outbound.Options.(*option.ShadowsocksOutboundOptions)
		if oRaw.Server != oCanon.Server {
			t.Errorf("ss Server mismatch: raw=%s, canon=%s", oRaw.Server, oCanon.Server)
		}
		if oRaw.ServerPort != oCanon.ServerPort {
			t.Errorf("ss ServerPort mismatch: raw=%d, canon=%d", oRaw.ServerPort, oCanon.ServerPort)
		}
		if oRaw.Method != oCanon.Method {
			t.Errorf("ss Method mismatch: raw=%s, canon=%s", oRaw.Method, oCanon.Method)
		}
		if oRaw.Password != oCanon.Password {
			t.Errorf("ss Password mismatch: raw=%s, canon=%s", oRaw.Password, oCanon.Password)
		}
	}
}

func assertTLSOptionsEqual(t *testing.T, raw, canon *option.OutboundTLSOptions) {
	if (raw == nil) != (canon == nil) {
		t.Fatalf("TLS nil mismatch: raw=%v, canon=%v", raw != nil, canon != nil)
	}
	if raw == nil {
		return
	}
	if raw.Enabled != canon.Enabled {
		t.Errorf("TLS Enabled mismatch: raw=%v, canon=%v", raw.Enabled, canon.Enabled)
	}
	if raw.ServerName != canon.ServerName {
		t.Errorf("TLS ServerName mismatch: raw=%s, canon=%s", raw.ServerName, canon.ServerName)
	}
	if (raw.UTLS == nil) != (canon.UTLS == nil) {
		t.Fatalf("UTLS nil mismatch: raw=%v, canon=%v", raw.UTLS != nil, canon.UTLS != nil)
	}
	if raw.UTLS != nil {
		if raw.UTLS.Enabled != canon.UTLS.Enabled {
			t.Errorf("UTLS Enabled mismatch: raw=%v, canon=%v", raw.UTLS.Enabled, canon.UTLS.Enabled)
		}
		if raw.UTLS.Fingerprint != canon.UTLS.Fingerprint {
			t.Errorf("UTLS Fingerprint mismatch: raw=%s, canon=%s", raw.UTLS.Fingerprint, canon.UTLS.Fingerprint)
		}
	}
	if (raw.Reality == nil) != (canon.Reality == nil) {
		t.Fatalf("Reality nil mismatch: raw=%v, canon=%v", raw.Reality != nil, canon.Reality != nil)
	}
	if raw.Reality != nil {
		if raw.Reality.Enabled != canon.Reality.Enabled {
			t.Errorf("Reality Enabled mismatch: raw=%v, canon=%v", raw.Reality.Enabled, canon.Reality.Enabled)
		}
		if raw.Reality.PublicKey != canon.Reality.PublicKey {
			t.Errorf("Reality PublicKey mismatch: raw=%s, canon=%s", raw.Reality.PublicKey, canon.Reality.PublicKey)
		}
		if raw.Reality.ShortID != canon.Reality.ShortID {
			t.Errorf("Reality ShortID mismatch: raw=%s, canon=%s", raw.Reality.ShortID, canon.Reality.ShortID)
		}
	}
}

func assertTransportOptionsEqual(t *testing.T, raw, canon *option.V2RayTransportOptions) {
	if (raw == nil) != (canon == nil) {
		t.Fatalf("Transport nil mismatch: raw=%v, canon=%v", raw != nil, canon != nil)
	}
	if raw == nil {
		return
	}
	if raw.Type != canon.Type {
		t.Errorf("Transport Type mismatch: raw=%s, canon=%s", raw.Type, canon.Type)
	}
	if raw.WebsocketOptions.Path != canon.WebsocketOptions.Path {
		t.Errorf("WS Path mismatch: raw=%s, canon=%s", raw.WebsocketOptions.Path, canon.WebsocketOptions.Path)
	}
	if len(raw.WebsocketOptions.Headers) != len(canon.WebsocketOptions.Headers) {
		t.Errorf("WS Headers count mismatch: raw=%d, canon=%d", len(raw.WebsocketOptions.Headers), len(canon.WebsocketOptions.Headers))
	}
	for k, v := range raw.WebsocketOptions.Headers {
		if len(v) > 0 && len(canon.WebsocketOptions.Headers[k]) > 0 {
			if v[0] != canon.WebsocketOptions.Headers[k][0] {
				t.Errorf("WS Header %s mismatch: raw=%s, canon=%s", k, v[0], canon.WebsocketOptions.Headers[k][0])
			}
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
