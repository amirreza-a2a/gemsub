//go:build !android

package source

import (
	"testing"
	"time"
)

func TestClientDefault_LinuxBehaviorPreserved(t *testing.T) {
	c := newHTTPClient()
	if c.Timeout != 20*time.Second {
		t.Errorf("expected timeout 20s, got %v", c.Timeout)
	}
	if c.Transport != nil {
		t.Errorf("expected nil Transport on non-Android build, got %v", c.Transport)
	}
}
