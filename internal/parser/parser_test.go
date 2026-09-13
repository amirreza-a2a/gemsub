package parser_test

import (
	"strings"
	"testing"

	"github.com/sagernet/sing-box/option"

	"gemsub/internal/parser"
)

func TestParse_RealityDefaultFingerprint(t *testing.T) {
	// Reality link without &fp=
	linkWithoutFP := "vless://dd7429eb-b77b-4a8d-be86-c34cac86faf7@example.com:443?type=tcp&security=reality&sni=www.nvidia.com&flow=xtls-rprx-vision&pbk=MXOVePed0T2SVOfYSEtecn2yApnsVC12dJ6nkujWTVU&sid=14b0eb7919eb7d#test"

	cand, err := parser.Parse(linkWithoutFP)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	if len(cand.Warnings) == 0 {
		t.Errorf("expected warning about defaulted fingerprint")
	}

	hasDefaultWarning := false
	for _, w := range cand.Warnings {
		if strings.Contains(w, "defaulted uTLS fingerprint to chrome") {
			hasDefaultWarning = true
			break
		}
	}
	if !hasDefaultWarning {
		t.Errorf("expected warning about defaulting to chrome, got: %v", cand.Warnings)
	}
}

func TestParse_RealitySanitizeUnsafeFingerprint(t *testing.T) {
	// Reality link with fp=unsafe
	linkUnsafeFP := "vless://dd7429eb-b77b-4a8d-be86-c34cac86faf7@example.com:443?type=tcp&security=reality&sni=www.nvidia.com&fp=unsafe&pbk=MXOVePed0T2SVOfYSEtecn2yApnsVC12dJ6nkujWTVU&sid=14b0eb7919eb7d#test"

	cand, err := parser.Parse(linkUnsafeFP)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	if len(cand.Warnings) == 0 {
		t.Errorf("expected warning about normalized fingerprint")
	}

	hasNormalizeWarning := false
	for _, w := range cand.Warnings {
		if strings.Contains(w, "normalized uTLS fingerprint to chrome") {
			hasNormalizeWarning = true
			break
		}
	}
	if !hasNormalizeWarning {
		t.Errorf("expected warning about normalizing unsafe to chrome, got: %v", cand.Warnings)
	}
}

func TestParse_Hysteria2_Fixtures(t *testing.T) {
	t.Run("H1_StandardSalamanderInsecure", func(t *testing.T) {
		link := "hysteria2://80747420-96c4-4a2f-83e6-eea4e46beb09@drhystuichdfy.samanidempire.org:20335?insecure=1&sni=drhystuichdfy.samanidempire.org&obfs=salamander&obfs-password=U1wBrYQyFm#⚡ b2n.ir/v2ray-configs | 481"
		cand, err := parser.Parse(link)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cand.Outbound.Type != "hysteria2" {
			t.Fatalf("expected type hysteria2, got %q", cand.Outbound.Type)
		}
		if cand.Outbound.Tag != "probe" {
			t.Fatalf("expected tag probe, got %q", cand.Outbound.Tag)
		}
		opts, ok := cand.Outbound.Options.(*option.Hysteria2OutboundOptions)
		if !ok {
			t.Fatalf("options not *option.Hysteria2OutboundOptions, got %T", cand.Outbound.Options)
		}
		if opts.Server != "drhystuichdfy.samanidempire.org" {
			t.Errorf("expected server drhystuichdfy.samanidempire.org, got %q", opts.Server)
		}
		if opts.ServerPort != 20335 {
			t.Errorf("expected server port 20335, got %d", opts.ServerPort)
		}
		if len(opts.ServerPorts) != 0 {
			t.Errorf("expected empty ServerPorts, got %v", opts.ServerPorts)
		}
		if opts.Password != "80747420-96c4-4a2f-83e6-eea4e46beb09" {
			t.Errorf("expected password 80747420-96c4-4a2f-83e6-eea4e46beb09, got %q", opts.Password)
		}
		if opts.Obfs == nil {
			t.Fatalf("expected non-nil obfs")
		}
		if opts.Obfs.Type != "salamander" || opts.Obfs.Password != "U1wBrYQyFm" {
			t.Errorf("expected obfs salamander/U1wBrYQyFm, got %s/%s", opts.Obfs.Type, opts.Obfs.Password)
		}
		if opts.TLS == nil || !opts.TLS.Enabled {
			t.Fatalf("expected TLS enabled")
		}
		if opts.TLS.ServerName != "drhystuichdfy.samanidempire.org" {
			t.Errorf("expected SNI drhystuichdfy.samanidempire.org, got %q", opts.TLS.ServerName)
		}
		if !opts.TLS.Insecure {
			t.Errorf("expected TLS insecure true")
		}
	})

	t.Run("H2_Hy2AliasDefaultPort443NoObfs", func(t *testing.T) {
		link := "hy2://f317b5d6-d399-4d3d-a051-89d674ae953c@msk.frkn.org:443/#EPODONIOS"
		cand, err := parser.Parse(link)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cand.Outbound.Type != "hysteria2" {
			t.Fatalf("expected type hysteria2, got %q", cand.Outbound.Type)
		}
		opts, ok := cand.Outbound.Options.(*option.Hysteria2OutboundOptions)
		if !ok {
			t.Fatalf("options not *option.Hysteria2OutboundOptions, got %T", cand.Outbound.Options)
		}
		if opts.Server != "msk.frkn.org" {
			t.Errorf("expected server msk.frkn.org, got %q", opts.Server)
		}
		if opts.ServerPort != 443 {
			t.Errorf("expected server port 443, got %d", opts.ServerPort)
		}
		if opts.Password != "f317b5d6-d399-4d3d-a051-89d674ae953c" {
			t.Errorf("expected password f317b5d6-d399-4d3d-a051-89d674ae953c, got %q", opts.Password)
		}
		if opts.Obfs != nil {
			t.Errorf("expected nil obfs, got %+v", opts.Obfs)
		}
		if opts.TLS == nil || !opts.TLS.Enabled {
			t.Fatalf("expected TLS enabled")
		}
		if opts.TLS.ServerName != "msk.frkn.org" {
			t.Errorf("expected SNI msk.frkn.org, got %q", opts.TLS.ServerName)
		}
		if opts.TLS.Insecure {
			t.Errorf("expected TLS insecure false")
		}
	})

	t.Run("H3_HTMLEntityUnescaping", func(t *testing.T) {
		link := "hysteria2://H7mP2xY9kJ4nQ8wR5tF6vB3z@18.175.236.136:443/?insecure=1&amp;sni=vpn-uk-002.fastervpn.world# By EbraSha 🛜"
		cand, err := parser.Parse(link)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		opts, ok := cand.Outbound.Options.(*option.Hysteria2OutboundOptions)
		if !ok {
			t.Fatalf("options not *option.Hysteria2OutboundOptions, got %T", cand.Outbound.Options)
		}
		if opts.Server != "18.175.236.136" {
			t.Errorf("expected server 18.175.236.136, got %q", opts.Server)
		}
		if opts.ServerPort != 443 {
			t.Errorf("expected server port 443, got %d", opts.ServerPort)
		}
		if opts.TLS == nil || !opts.TLS.Enabled {
			t.Fatalf("expected TLS enabled")
		}
		if opts.TLS.ServerName != "vpn-uk-002.fastervpn.world" {
			t.Errorf("expected SNI vpn-uk-002.fastervpn.world, got %q", opts.TLS.ServerName)
		}
		if !opts.TLS.Insecure {
			t.Errorf("expected TLS insecure true")
		}
	})

	t.Run("H4_PortHoppingRangeNormalization", func(t *testing.T) {
		link := "hysteria2://password123@example.com:443,20000-30000/?sni=example.com&insecure=1"
		cand, err := parser.Parse(link)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		opts, ok := cand.Outbound.Options.(*option.Hysteria2OutboundOptions)
		if !ok {
			t.Fatalf("options not *option.Hysteria2OutboundOptions, got %T", cand.Outbound.Options)
		}
		if opts.Server != "example.com" {
			t.Errorf("expected server example.com, got %q", opts.Server)
		}
		if opts.ServerPort != 0 {
			t.Errorf("expected server port 0 when ServerPorts is set, got %d", opts.ServerPort)
		}
		expectedPorts := []string{"443", "20000:30000"}
		if len(opts.ServerPorts) != len(expectedPorts) {
			t.Fatalf("expected ServerPorts %v, got %v", expectedPorts, opts.ServerPorts)
		}
		for i, p := range expectedPorts {
			if opts.ServerPorts[i] != p {
				t.Errorf("expected ServerPorts[%d] == %q, got %q", i, p, opts.ServerPorts[i])
			}
		}
	})
}

func TestParse_Hysteria2_EdgeCases(t *testing.T) {
	t.Run("UserpassAuthentication", func(t *testing.T) {
		link := "hysteria2://alice:secret_pass@example.com:443/?sni=example.com"
		cand, err := parser.Parse(link)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		opts := cand.Outbound.Options.(*option.Hysteria2OutboundOptions)
		if opts.Password != "alice:secret_pass" {
			t.Errorf("expected password alice:secret_pass, got %q", opts.Password)
		}
	})

	t.Run("QueryMportNormalization", func(t *testing.T) {
		link := "hysteria2://password123@example.com:443/?mport=20000-50000"
		cand, err := parser.Parse(link)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		opts := cand.Outbound.Options.(*option.Hysteria2OutboundOptions)
		if len(opts.ServerPorts) != 1 || opts.ServerPorts[0] != "20000:50000" {
			t.Errorf("expected ServerPorts [20000:50000], got %v", opts.ServerPorts)
		}
		if opts.ServerPort != 0 {
			t.Errorf("expected ServerPort 0, got %d", opts.ServerPort)
		}
	})

	t.Run("QueryPortsAliasNormalization", func(t *testing.T) {
		link := "hysteria2://password@example.com:443/?ports=20000-30000"
		cand, err := parser.Parse(link)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		opts := cand.Outbound.Options.(*option.Hysteria2OutboundOptions)
		if len(opts.ServerPorts) != 1 || opts.ServerPorts[0] != "20000:30000" {
			t.Errorf("expected ServerPorts [20000:30000], got %v", opts.ServerPorts)
		}
		if opts.ServerPort != 0 {
			t.Errorf("expected ServerPort 0, got %d", opts.ServerPort)
		}
	})

	t.Run("CredentialPlusPreservation", func(t *testing.T) {
		tests := []struct {
			name     string
			link     string
			expected string
		}{
			{
				name:     "LiteralPlus",
				link:     "hysteria2://user+pass:secret+123@example.com:443",
				expected: "user+pass:secret+123",
			},
			{
				name:     "PercentEncodedPlus",
				link:     "hysteria2://user%2Bpass:secret%2B123@example.com:443",
				expected: "user+pass:secret+123",
			},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				cand, err := parser.Parse(tc.link)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				opts := cand.Outbound.Options.(*option.Hysteria2OutboundOptions)
				if opts.Password != tc.expected {
					t.Errorf("expected password %q, got %q", tc.expected, opts.Password)
				}
			})
		}
	})

	t.Run("OmittedPortDefaultsTo443", func(t *testing.T) {
		link := "hy2://password123@example.com"
		cand, err := parser.Parse(link)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		opts := cand.Outbound.Options.(*option.Hysteria2OutboundOptions)
		if opts.ServerPort != 443 {
			t.Errorf("expected default port 443, got %d", opts.ServerPort)
		}
	})

	t.Run("IPv6Host", func(t *testing.T) {
		link := "hysteria2://password123@[2001:db8::1]:443/?sni=example.com"
		cand, err := parser.Parse(link)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		opts := cand.Outbound.Options.(*option.Hysteria2OutboundOptions)
		if opts.Server != "2001:db8::1" {
			t.Errorf("expected server 2001:db8::1, got %q", opts.Server)
		}
		if opts.ServerPort != 443 {
			t.Errorf("expected server port 443, got %d", opts.ServerPort)
		}
	})

	t.Run("PinSHA256Hex", func(t *testing.T) {
		pinHex := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
		link := "hysteria2://password123@example.com:443/?pinSHA256=" + pinHex
		cand, err := parser.Parse(link)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		opts := cand.Outbound.Options.(*option.Hysteria2OutboundOptions)
		if len(opts.TLS.CertificatePublicKeySHA256) != 1 {
			t.Fatalf("expected 1 pinned cert hash, got %d", len(opts.TLS.CertificatePublicKeySHA256))
		}
		if len(opts.TLS.CertificatePublicKeySHA256[0]) != 32 {
			t.Errorf("expected 32 bytes SHA256, got %d", len(opts.TLS.CertificatePublicKeySHA256[0]))
		}
	})

	t.Run("ALPNCommaSeparated", func(t *testing.T) {
		link := "hysteria2://password123@example.com:443/?alpn=h3,h2"
		cand, err := parser.Parse(link)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		opts := cand.Outbound.Options.(*option.Hysteria2OutboundOptions)
		if len(opts.TLS.ALPN) != 2 || opts.TLS.ALPN[0] != "h3" || opts.TLS.ALPN[1] != "h2" {
			t.Errorf("expected ALPN [h3, h2], got %v", opts.TLS.ALPN)
		}
	})

	t.Run("InvalidObfs", func(t *testing.T) {
		link := "hysteria2://password123@example.com:443/?obfs=invalid&obfs-password=123"
		_, err := parser.Parse(link)
		if err == nil || !strings.Contains(err.Error(), "unsupported obfs type") {
			t.Errorf("expected unsupported obfs type error, got %v", err)
		}
	})
}

func TestParse_TUIC_Fixtures(t *testing.T) {
	t.Run("T1_StandardTUICv5Insecure", func(t *testing.T) {
		link := "tuic://56d8a8fd-68b8-47f8-8c7f-98cc675902db:56d8a8fd-68b8-47f8-8c7f-98cc675902db@us02.uzifan.monster:8443/?congestion_control=bbr&udp_relay_mode=native&sni=www.bing.com&alpn=h3&allow_insecure=1# By EbraSha 🌌"
		cand, err := parser.Parse(link)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cand.Outbound.Type != "tuic" {
			t.Fatalf("expected type tuic, got %q", cand.Outbound.Type)
		}
		if cand.Outbound.Tag != "probe" {
			t.Fatalf("expected tag probe, got %q", cand.Outbound.Tag)
		}
		opts, ok := cand.Outbound.Options.(*option.TUICOutboundOptions)
		if !ok {
			t.Fatalf("options not *option.TUICOutboundOptions, got %T", cand.Outbound.Options)
		}
		if opts.Server != "us02.uzifan.monster" {
			t.Errorf("expected server us02.uzifan.monster, got %q", opts.Server)
		}
		if opts.ServerPort != 8443 {
			t.Errorf("expected server port 8443, got %d", opts.ServerPort)
		}
		if opts.UUID != "56d8a8fd-68b8-47f8-8c7f-98cc675902db" {
			t.Errorf("expected UUID 56d8a8fd-68b8-47f8-8c7f-98cc675902db, got %q", opts.UUID)
		}
		if opts.Password != "56d8a8fd-68b8-47f8-8c7f-98cc675902db" {
			t.Errorf("expected password 56d8a8fd-68b8-47f8-8c7f-98cc675902db, got %q", opts.Password)
		}
		if opts.CongestionControl != "bbr" {
			t.Errorf("expected congestion control bbr, got %q", opts.CongestionControl)
		}
		if opts.UDPRelayMode != "native" {
			t.Errorf("expected udp relay mode native, got %q", opts.UDPRelayMode)
		}
		if opts.TLS == nil || !opts.TLS.Enabled {
			t.Fatalf("expected TLS enabled")
		}
		if opts.TLS.ServerName != "www.bing.com" {
			t.Errorf("expected SNI www.bing.com, got %q", opts.TLS.ServerName)
		}
		if !opts.TLS.Insecure {
			t.Errorf("expected TLS insecure true")
		}
		if len(opts.TLS.ALPN) != 1 || opts.TLS.ALPN[0] != "h3" {
			t.Errorf("expected ALPN [h3], got %v", opts.TLS.ALPN)
		}
	})

	t.Run("T2_StandardTUICv5StrictTLS", func(t *testing.T) {
		link := "tuic://9a198016-fdb7-4510-acca-e2c68c1083e5:9a198016-fdb7-4510-acca-e2c68c1083e5@singapore.ruixing.fun:58441/?congestion_control=bbr&udp_relay_mode=native&sni=singapore.ruixing.fun&alpn=h3# By EbraSha 🪐"
		cand, err := parser.Parse(link)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		opts, ok := cand.Outbound.Options.(*option.TUICOutboundOptions)
		if !ok {
			t.Fatalf("options not *option.TUICOutboundOptions, got %T", cand.Outbound.Options)
		}
		if opts.Server != "singapore.ruixing.fun" {
			t.Errorf("expected server singapore.ruixing.fun, got %q", opts.Server)
		}
		if opts.ServerPort != 58441 {
			t.Errorf("expected server port 58441, got %d", opts.ServerPort)
		}
		if opts.UUID != "9a198016-fdb7-4510-acca-e2c68c1083e5" {
			t.Errorf("expected UUID 9a198016-fdb7-4510-acca-e2c68c1083e5, got %q", opts.UUID)
		}
		if opts.Password != "9a198016-fdb7-4510-acca-e2c68c1083e5" {
			t.Errorf("expected password 9a198016-fdb7-4510-acca-e2c68c1083e5, got %q", opts.Password)
		}
		if opts.TLS == nil || !opts.TLS.Enabled {
			t.Fatalf("expected TLS enabled")
		}
		if opts.TLS.ServerName != "singapore.ruixing.fun" {
			t.Errorf("expected SNI singapore.ruixing.fun, got %q", opts.TLS.ServerName)
		}
		if opts.TLS.Insecure {
			t.Errorf("expected TLS insecure false")
		}
	})

	t.Run("T3_TUICv4TokenRejection", func(t *testing.T) {
		link := "tuic://token_only_secret@singapore.ruixing.fun:58441/?congestion_control=bbr"
		_, err := parser.Parse(link)
		if err == nil {
			t.Fatalf("expected error for TUIC v4 token-only link, got nil")
		}
		expectedErr := "tuic: invalid userinfo: expected <uuid>:<password>"
		if err.Error() != expectedErr {
			t.Errorf("expected exact error %q, got %q", expectedErr, err.Error())
		}
	})
}

func TestParse_TUIC_EdgeCases(t *testing.T) {
	t.Run("MissingPasswordRejection", func(t *testing.T) {
		link := "tuic://56d8a8fd-68b8-47f8-8c7f-98cc675902db@singapore.ruixing.fun:58441"
		_, err := parser.Parse(link)
		if err == nil {
			t.Fatalf("expected error for missing password")
		}
		expectedErr := "tuic: invalid userinfo: expected <uuid>:<password>"
		if err.Error() != expectedErr {
			t.Errorf("expected %q, got %q", expectedErr, err.Error())
		}
	})

	t.Run("InvalidUUIDFormat", func(t *testing.T) {
		link := "tuic://not-a-valid-uuid:mypassword@singapore.ruixing.fun:58441"
		_, err := parser.Parse(link)
		if err == nil {
			t.Fatalf("expected error for invalid UUID")
		}
		expectedErr := "tuic: invalid userinfo: expected <uuid>:<password>"
		if err.Error() != expectedErr {
			t.Errorf("expected %q, got %q", expectedErr, err.Error())
		}
	})

	t.Run("DisableSNI", func(t *testing.T) {
		link := "tuic://56d8a8fd-68b8-47f8-8c7f-98cc675902db:pass@singapore.ruixing.fun:58441/?sni=custom.com&disable_sni=1"
		cand, err := parser.Parse(link)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		opts := cand.Outbound.Options.(*option.TUICOutboundOptions)
		if opts.TLS.ServerName != "" {
			t.Errorf("expected empty SNI with disable_sni=1, got %q", opts.TLS.ServerName)
		}
	})

	t.Run("DefaultSNIWhenOmitted", func(t *testing.T) {
		link := "tuic://56d8a8fd-68b8-47f8-8c7f-98cc675902db:pass@singapore.ruixing.fun:58441"
		cand, err := parser.Parse(link)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		opts := cand.Outbound.Options.(*option.TUICOutboundOptions)
		if opts.TLS.ServerName != "singapore.ruixing.fun" {
			t.Errorf("expected default SNI singapore.ruixing.fun, got %q", opts.TLS.ServerName)
		}
	})

	t.Run("HeartbeatAndZeroRTT", func(t *testing.T) {
		link := "tuic://56d8a8fd-68b8-47f8-8c7f-98cc675902db:pass@singapore.ruixing.fun:58441/?heartbeat=15s&zero_rtt_handshake=1"
		cand, err := parser.Parse(link)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		opts := cand.Outbound.Options.(*option.TUICOutboundOptions)
		if opts.Heartbeat.Build() != 15*1000*1000*1000 { // 15s in nanoseconds
			t.Errorf("expected heartbeat 15s, got %v", opts.Heartbeat)
		}
		if !opts.ZeroRTTHandshake {
			t.Errorf("expected zero RTT true")
		}
	})

	t.Run("CredentialPlusPreservation", func(t *testing.T) {
		tests := []struct {
			name     string
			link     string
			expected string
		}{
			{
				name:     "LiteralPlus",
				link:     "tuic://56d8a8fd-68b8-47f8-8c7f-98cc675902db:secret+123@example.com:443",
				expected: "secret+123",
			},
			{
				name:     "PercentEncodedPlus",
				link:     "tuic://56d8a8fd-68b8-47f8-8c7f-98cc675902db:secret%2B123@example.com:443",
				expected: "secret+123",
			},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				cand, err := parser.Parse(tc.link)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				opts := cand.Outbound.Options.(*option.TUICOutboundOptions)
				if opts.Password != tc.expected {
					t.Errorf("expected password %q, got %q", tc.expected, opts.Password)
				}
			})
		}
	})

	t.Run("UDPOverStream", func(t *testing.T) {
		link := "tuic://56d8a8fd-68b8-47f8-8c7f-98cc675902db:pass@singapore.ruixing.fun:58441/?udp_over_stream=1"
		cand, err := parser.Parse(link)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		opts := cand.Outbound.Options.(*option.TUICOutboundOptions)
		if !opts.UDPOverStream {
			t.Errorf("expected UDPOverStream true")
		}
	})

	t.Run("UDPOverStreamConflictWithUDPRelayMode", func(t *testing.T) {
		link := "tuic://56d8a8fd-68b8-47f8-8c7f-98cc675902db:pass@singapore.ruixing.fun:58441/?udp_over_stream=1&udp_relay_mode=native"
		_, err := parser.Parse(link)
		if err == nil {
			t.Fatalf("expected error for conflicting udp_over_stream and udp_relay_mode")
		}
		expectedSubstr := "tuic: udp_over_stream conflicts with udp_relay_mode"
		if !strings.Contains(err.Error(), expectedSubstr) {
			t.Errorf("expected error containing %q, got %q", expectedSubstr, err.Error())
		}
	})
}

func TestParse_HysteriaRealm_Unsupported(t *testing.T) {
	realmLinks := []string{
		"hysteria2+realm://token@rendezvous.com/myrealm?auth=secret",
		"hy2+realm://token@rendezvous.com/myrealm?auth=secret",
	}
	for _, link := range realmLinks {
		_, err := parser.Parse(link)
		if err == nil {
			t.Errorf("expected error for realm link %q, got nil", link)
		}
		if !strings.Contains(err.Error(), "unsupported scheme") {
			t.Errorf("expected unsupported scheme error for %q, got: %v", link, err)
		}
	}
}

func TestParse_ExistingProtocols_Regression(t *testing.T) {
	links := []string{
		"vless://dd7429eb-b77b-4a8d-be86-c34cac86faf7@example.com:443?type=tcp&security=tls#vless",
		"vmess://eyJhZGQiOiIxMjcuMC4wLjEiLCJwb3J0IjoiNDQzIiwiaWQiOiJkZDc0MjllYi1iNzdiLTRhOGQtYmU4Ni1jMzRjYWM4NmZhZjciLCJhaWQiOiIwIiwibmV0IjoidGNwIiwidHlwZSI6Im5vbmUiLCJob3N0IjoiIiwicGF0aCI6IiIsInRscyI6InRscyIsInNuaSI6ImV4YW1wbGUuY29tIn0=",
		"trojan://password123@example.com:443?security=tls#trojan",
		"ss://YWVzLTEyOC1nY206c2VjcmV0@1.2.3.4:8388#ss",
	}
	for _, link := range links {
		cand, err := parser.Parse(link)
		if err != nil {
			t.Errorf("regression parse error for %q: %v", link, err)
		}
		if cand.Outbound.Tag != "probe" {
			t.Errorf("expected probe tag for %q, got %q", link, cand.Outbound.Tag)
		}
	}
}
