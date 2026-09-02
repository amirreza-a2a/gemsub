package parser_test

import (
	"strings"
	"testing"

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
