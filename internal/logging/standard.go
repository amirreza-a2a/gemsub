package logging

import (
	"io"
	"log/slog"
	"os"
)

// NewStandardHandler returns an slog.Handler that writes standard timestamped log records
// to w (or os.Stderr if w is nil) using slog's TextHandler.
func NewStandardHandler(w io.Writer, opts *slog.HandlerOptions) slog.Handler {
	if w == nil {
		w = os.Stderr
	}
	if opts == nil {
		opts = &slog.HandlerOptions{
			Level: slog.LevelInfo,
		}
	}
	return slog.NewTextHandler(w, opts)
}

// Setup configures slog's default logger based on headless mode.
// In headless mode, it sets up standard timestamped terminal logging to w (os.Stderr if nil).
// In TUI mode, it sets up an in-memory RingLogHandler and returns it.
func Setup(headless bool, ringCapacity int, w io.Writer) *RingLogHandler {
	if headless {
		h := NewStandardHandler(w, nil)
		slog.SetDefault(slog.New(h))
		return nil
	}
	ring := NewRingLogHandler(ringCapacity)
	slog.SetDefault(slog.New(ring))
	return ring
}
