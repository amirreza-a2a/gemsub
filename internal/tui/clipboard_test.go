package tui_test

import (
	"testing"

	"gemsub/internal/tui"
)

func TestCopyToClipboard_NonFatal(t *testing.T) {
	// Calling CopyToClipboard with empty string should return an error
	err := tui.CopyToClipboard("")
	if err == nil {
		t.Error("expected error when copying empty string, got nil")
	}

	// Calling CopyToClipboard with real text should either succeed (if tool present) or return error
	// but MUST NOT panic.
	_ = tui.CopyToClipboard("vless://test@1.1.1.1:443")
}
