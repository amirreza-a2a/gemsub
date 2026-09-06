package clipboard

import (
	"testing"
)

func TestCopyToClipboard(t *testing.T) {
	// Empty text must return an error
	if err := CopyToClipboard(""); err == nil {
		t.Fatal("expected error for empty text, got nil")
	}

	// Non-empty text must not panic and must return nil or a non-fatal error
	_ = CopyToClipboard("vless://test@1.1.1.1:443#TestNode")
}
