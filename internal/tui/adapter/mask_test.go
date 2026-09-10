package adapter_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"gemsub/internal/store"
	"gemsub/internal/tui/adapter"
)

func TestExtractProtocol(t *testing.T) {
	tests := []struct {
		link string
		want string
	}{
		{"vless://uuid@1.2.3.4:443", "vless"},
		{"vmess://eyJhZGQiOiIxLjIuMy40In0=", "vmess"},
		{"trojan://pass@1.2.3.4:443", "trojan"},
		{"ss://YWVzLTEyOC1nY206cGFzc0AxLjIuMy40OjQ0Mw==#remark", "ss"},
		{"http://example.com", "unknown"},
		{"", "unknown"},
	}

	for _, tt := range tests {
		got := adapter.ExtractProtocol(tt.link)
		if got != tt.want {
			t.Errorf("ExtractProtocol(%q) = %q, want %q", tt.link, got, tt.want)
		}
	}
}

func TestExtractEndpoint(t *testing.T) {
	tests := []struct {
		link string
		want string
	}{
		{"vless://my-secret-uuid@example.com:443?type=ws", "example.com:443"},
		{"trojan://my-secret-password@[2001:db8::1]:8443", "[2001:db8::1]:8443"},
		{"ss://aes-128-gcm:secret@10.0.0.1:8388#server", "10.0.0.1:8388"},
		{"http://invalid-scheme", "invalid-scheme"},
		// VMess with numeric port
		{"vmess://eyJhZGQiOiIxLjIuMy40IiwicG9ydCI6NDQzfQ==", "1.2.3.4:443"},
		// VMess with string port
		{"vmess://eyJhZGQiOiIxLjIuMy40IiwicG9ydCI6Ijg0NDMifQ==", "1.2.3.4:8443"},
		// VMess with domain
		{"vmess://eyJhZGQiOiJleGFtcGxlLmNvbSIsInBvcnQiOjIwNTN9", "example.com:2053"},
		// VMess with IPv6 address
		{"vmess://eyJhZGQiOiIyMDAxOmRiODo6MSIsInBvcnQiOjg0NDN9", "[2001:db8::1]:8443"},
		// VMess without port
		{"vmess://eyJhZGQiOiIxLjIuMy40In0=", "1.2.3.4"},
	}

	for _, tt := range tests {
		got := adapter.ExtractEndpoint(tt.link)
		if got != tt.want {
			t.Errorf("ExtractEndpoint(%q) = %q, want %q", tt.link, got, tt.want)
		}
		if strings.Contains(got, "secret") || strings.Contains(got, "uuid") || strings.Contains(got, "password") {
			t.Errorf("ExtractEndpoint leaked credentials in %q: %q", tt.link, got)
		}
	}
}

func TestFormatHostPort(t *testing.T) {
	tests := []struct {
		host string
		port string
		want string
	}{
		{"1.1.1.1", "443", "1.1.1.1:443"},
		{"example.com", "80", "example.com:80"},
		{"2001:db8::1", "443", "[2001:db8::1]:443"},
		{"[2001:db8::1]", "443", "[2001:db8::1]:443"},
		{"2001:db8::1", "", "[2001:db8::1]"},
		{"", "", ""},
	}
	for _, tt := range tests {
		got := adapter.FormatHostPort(tt.host, tt.port)
		if got != tt.want {
			t.Errorf("FormatHostPort(%q, %q) = %q, want %q", tt.host, tt.port, got, tt.want)
		}
	}
}

func TestExtractConnectionParams(t *testing.T) {
	// 1. VLESS link with path and sni
	linkVless := "vless://uuid@example.com:443?type=ws&path=%2Fgraphql&sni=cdn.example.com#Node1"
	host, port, path, sni := adapter.ExtractConnectionParams(linkVless)
	if host != "example.com" || port != "443" || path != "/graphql" || sni != "cdn.example.com" {
		t.Errorf("ExtractConnectionParams(VLESS) = (%q, %q, %q, %q), want (example.com, 443, /graphql, cdn.example.com)",
			host, port, path, sni)
	}

	// 2. VMess link with path and sni
	// {"add":"1.2.3.4","port":8443,"path":"/ws","sni":"my-sni.com"}
	linkVmess := "vmess://eyJhZGQiOiIxLjIuMy40IiwicG9ydCI6ODQ0MywicGF0aCI6Ii93cyIsInNuaSI6Im15LXNuaS5jb20ifQ=="
	host, port, path, sni = adapter.ExtractConnectionParams(linkVmess)
	if host != "1.2.3.4" || port != "8443" || path != "/ws" || sni != "my-sni.com" {
		t.Errorf("ExtractConnectionParams(VMess) = (%q, %q, %q, %q), want (1.2.3.4, 8443, /ws, my-sni.com)",
			host, port, path, sni)
	}

	// 3. VLESS link with IPv6 host
	linkVless6 := "vless://uuid@[2001:db8::1]:443?type=ws#Node6"
	host, port, path, sni = adapter.ExtractConnectionParams(linkVless6)
	if host != "2001:db8::1" || port != "443" {
		t.Errorf("ExtractConnectionParams(VLESS IPv6) = (%q, %q), want (2001:db8::1, 443)", host, port)
	}
}

func TestExtractRemark(t *testing.T) {
	tests := []struct {
		link string
		want string
	}{
		{"vless://uuid@example.com:443#My%20Server", "My Server"},
		{"trojan://pass@example.com:443#Frankfurt", "Frankfurt"},
		{"vless://uuid@example.com:443", ""},
	}

	for _, tt := range tests {
		got := adapter.ExtractRemark(tt.link)
		if got != tt.want {
			t.Errorf("ExtractRemark(%q) = %q, want %q", tt.link, got, tt.want)
		}
	}
}

func TestMaskLink_MandatoryMasking(t *testing.T) {
	// Base64 VMess with secret UUID: {"add":"1.2.3.4","port":443,"id":"my-secret-vmess-guid-1234","net":"ws"}
	vmessRaw := "vmess://eyJhZGQiOiIxLjIuMy40IiwicG9ydCI6NDQzLCJpZCI6Im15LXNlY3JldC12bWVzcy1ndWlkLTEyMzQiLCJuZXQiOiJ3cyJ9"

	// SIP002 Shadowsocks with base64 credentials before @: chacha20-ietf-poly1305:cartoon-password -> Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpjYXJ0b29uLXBhc3N3b3Jk
	ssSIP002 := "ss://Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpjYXJ0b29uLXBhc3N3b3Jk@1.2.3.4:8388#SIP002Server"

	// Legacy base64 Shadowsocks (whole method:pass@host:port base64 encoded): aes-128-gcm:secretpassword@1.2.3.4:8388 -> YWVzLTEyOC1nY206c2VjcmV0cGFzc3dvcmRAMS4yLjMuNDo4Mzg4
	ssLegacyB64 := "ss://YWVzLTEyOC1nY206c2VjcmV0cGFzc3dvcmRAMS4yLjMuNDo4Mzg4#LegacySS"

	tests := []struct {
		name            string
		link            string
		forbiddenTokens []string
		mustContain     []string
	}{
		{
			name:            "VLESS with UUID",
			link:            "vless://4a3b1234-abcd-ef01-2345-6789abcdef01@example.com:443?type=ws&path=%2Fws#Server1",
			forbiddenTokens: []string{"4a3b1234-abcd-ef01-2345-6789abcdef01"},
			mustContain:     []string{"vless://[REDACTED]@", "example.com:443", "Server1"},
		},
		{
			name:            "VLESS with query parameter tokens",
			link:            "vless://secret-uuid@example.com:443?token=super-secret-token&key=secret-key#QueryNode",
			forbiddenTokens: []string{"secret-uuid", "super-secret-token", "secret-key"},
			mustContain:     []string{"vless://[REDACTED]@", "example.com:443", "QueryNode"},
		},
		{
			name:            "Trojan with password",
			link:            "trojan://supersecretpassword123@1.2.3.4:443?security=tls#DE",
			forbiddenTokens: []string{"supersecretpassword123"},
			mustContain:     []string{"trojan://[REDACTED]@", "1.2.3.4:443", "DE"},
		},
		{
			name:            "VMess base64 JSON",
			link:            vmessRaw,
			forbiddenTokens: []string{"my-secret-vmess-guid-1234"},
			mustContain:     []string{"vmess://"},
		},
		{
			name:            "Shadowsocks plaintext with @",
			link:            "ss://aes-256-gcm:supersecret@1.2.3.4:8388#SS",
			forbiddenTokens: []string{"supersecret"},
			mustContain:     []string{"ss://[REDACTED]@", "1.2.3.4:8388", "SS"},
		},
		{
			name:            "Shadowsocks SIP002 base64 with @",
			link:            ssSIP002,
			forbiddenTokens: []string{"cartoon-password", "Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpjYXJ0b29uLXBhc3N3b3Jk"},
			mustContain:     []string{"ss://[REDACTED]@", "1.2.3.4:8388", "SIP002Server"},
		},
		{
			name:            "Shadowsocks legacy single base64 string",
			link:            ssLegacyB64,
			forbiddenTokens: []string{"secretpassword", "YWVzLTEyOC1nY206c2VjcmV0cGFzc3dvcmRAMS4yLjMuNDo4Mzg4"},
			mustContain:     []string{"ss://[REDACTED]@", "1.2.3.4:8388", "LegacySS"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			masked := adapter.MaskLink(tt.link)
			for _, token := range tt.forbiddenTokens {
				if strings.Contains(masked, token) {
					t.Errorf("MaskLink(%q) leaked sensitive token %q: %q", tt.link, token, masked)
				}
			}
			for _, req := range tt.mustContain {
				if !strings.Contains(masked, req) {
					t.Errorf("MaskLink(%q) missing required component %q: %q", tt.link, req, masked)
				}
			}
		})
	}

	// Explicit verification that VMess base64 JSON contains "[REDACTED]" for id
	maskedVMess := adapter.MaskLink(vmessRaw)
	rawB64 := strings.TrimPrefix(maskedVMess, "vmess://")
	dec, err := base64.StdEncoding.DecodeString(rawB64)
	if err != nil {
		t.Fatalf("failed to decode masked vmess base64: %v", err)
	}
	if !strings.Contains(string(dec), `"[REDACTED]"`) {
		t.Errorf("expected id in decoded vmess to be redacted, got: %s", string(dec))
	}
}

func TestFormatHistoryGlyphs(t *testing.T) {
	samples := []store.ProbeSample{
		{Status: store.StatusPassed},
		{Status: store.StatusFailed},
		{Status: store.StatusInconclusive},
	}

	glyphs := adapter.FormatHistoryGlyphs(samples, 5)
	expectedPrefix := "[●×○"
	if !strings.HasPrefix(glyphs, expectedPrefix) {
		t.Errorf("FormatHistoryGlyphs() = %q, expected to start with %q", glyphs, expectedPrefix)
	}
	if !strings.HasSuffix(glyphs, "]") {
		t.Errorf("FormatHistoryGlyphs() = %q, expected to end with ']'", glyphs)
	}
	if len([]rune(glyphs)) != 7 { // '[' + 5 glyphs + ']'
		t.Errorf("FormatHistoryGlyphs() length in runes = %d, want 7", len([]rune(glyphs)))
	}
}

func TestFormatHistoryGlyphsFromStatuses_Equivalence(t *testing.T) {
	samples := []store.ProbeSample{
		{Status: store.StatusPassed},
		{Status: store.StatusFailed},
		{Status: store.StatusInconclusive},
	}

	var statuses [16]byte
	statuses[0] = 'P'
	statuses[1] = 'F'
	statuses[2] = 'I'

	for cap := 1; cap <= 10; cap++ {
		legacy := adapter.FormatHistoryGlyphs(samples, cap)
		compact := adapter.FormatHistoryGlyphsFromStatuses(statuses, len(samples), cap)
		if legacy != compact {
			t.Fatalf("mismatch at cap %d: legacy=%q compact=%q", cap, legacy, compact)
		}
	}
}
