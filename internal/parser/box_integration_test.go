package parser_test

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"

	"gemsub/internal/parser"
)

// TestSingBox_ConstructRealBox_Hysteria2AndTUIC verifies that parsed Hysteria2
// and TUIC outbounds can be successfully instantiated in a real sing-box instance.
// This proves that the outbound constructors are registered and functional under
// the -tags "with_utls with_quic" compiler flags.
func TestSingBox_ConstructRealBox_Hysteria2AndTUIC(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fixtures := []struct {
		name string
		link string
	}{
		{
			name: "Hysteria2_H1",
			link: "hysteria2://80747420-96c4-4a2f-83e6-eea4e46beb09@drhystuichdfy.samanidempire.org:20335?insecure=1&sni=drhystuichdfy.samanidempire.org&obfs=salamander&obfs-password=U1wBrYQyFm#H1",
		},
		{
			name: "Hysteria2_H2_Hy2Alias",
			link: "hy2://f317b5d6-d399-4d3d-a051-89d674ae953c@msk.frkn.org:443/#H2",
		},
		{
			name: "TUIC_T1",
			link: "tuic://56d8a8fd-68b8-47f8-8c7f-98cc675902db:56d8a8fd-68b8-47f8-8c7f-98cc675902db@us02.uzifan.monster:8443/?congestion_control=bbr&udp_relay_mode=native&sni=www.bing.com&alpn=h3&allow_insecure=1#T1",
		},
		{
			name: "TUIC_T2",
			link: "tuic://9a198016-fdb7-4510-acca-e2c68c1083e5:9a198016-fdb7-4510-acca-e2c68c1083e5@singapore.ruixing.fun:58441/?congestion_control=bbr&udp_relay_mode=native&sni=singapore.ruixing.fun&alpn=h3#T2",
		},
	}

	for _, tc := range fixtures {
		t.Run(tc.name, func(t *testing.T) {
			cand, err := parser.Parse(tc.link)
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}

			boxCtx := include.Context(ctx)
			instance, err := box.New(box.Options{
				Context: boxCtx,
				Options: option.Options{
					Log:       &option.LogOptions{Disabled: true},
					Route:     &option.RouteOptions{AutoDetectInterface: false},
					Outbounds: []option.Outbound{cand.Outbound},
				},
			})
			if err != nil {
				t.Fatalf("box.New failed to construct real Box: %v", err)
			}
			defer instance.Close()

			ob, loaded := instance.Outbound().Outbound(cand.Outbound.Tag)
			if !loaded || ob == nil {
				t.Fatalf("outbound tag %q not found in box", cand.Outbound.Tag)
			}
			if ob.Type() != cand.Outbound.Type {
				t.Errorf("expected outbound type %q, got %q", cand.Outbound.Type, ob.Type())
			}
		})
	}
}
