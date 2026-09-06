package tui_test

import (
	"bytes"
	"log/slog"
	"testing"

	"gemsub/internal/logging"
)

func TestHeadlessIsolation_NoRingLogHandler(t *testing.T) {
	origLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	var buf bytes.Buffer
	handler := logging.Setup(true, 1000, &buf)
	if handler != nil {
		t.Errorf("expected logging.Setup(true, ...) to return nil RingLogHandler in headless mode, got %v", handler)
	}

	// Logging in headless mode should write to the provided writer, not an in-memory ring buffer
	slog.Info("headless test message")
	if !bytes.Contains(buf.Bytes(), []byte("headless test message")) {
		t.Errorf("expected message written directly to writer in headless mode, got: %s", buf.String())
	}
}
