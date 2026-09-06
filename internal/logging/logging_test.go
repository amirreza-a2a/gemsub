package logging_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"gemsub/internal/logging"
)

func TestRingLogHandler_Basic(t *testing.T) {
	ring := logging.NewRingLogHandler(10)
	logger := slog.New(ring)

	logger.Info("hello world", "key", "val")
	logger.Warn("be careful", "code", 42)
	logger.Error("something failed", "err", "timeout")

	if ring.Len() != 3 {
		t.Fatalf("expected Len 3, got %d", ring.Len())
	}

	records := ring.Records()
	if len(records) != 3 {
		t.Fatalf("expected 3 records, got %d", len(records))
	}

	if records[0].Message != "hello world" || records[0].Level != slog.LevelInfo {
		t.Errorf("record 0 mismatch: %v", records[0])
	}
	if records[1].Message != "be careful" || records[1].Level != slog.LevelWarn {
		t.Errorf("record 1 mismatch: %v", records[1])
	}
	if records[2].Message != "something failed" || records[2].Level != slog.LevelError {
		t.Errorf("record 2 mismatch: %v", records[2])
	}

	last, ok := ring.Last()
	if !ok || last.Message != "something failed" {
		t.Errorf("expected last record 'something failed', got %v (ok=%v)", last, ok)
	}
}

func TestRingLogHandler_CapacityAndRingWrapping(t *testing.T) {
	const cap = 5
	ring := logging.NewRingLogHandler(cap)
	logger := slog.New(ring)

	if ring.Cap() != cap {
		t.Fatalf("expected Cap %d, got %d", cap, ring.Cap())
	}

	// Write 12 records into buffer of size 5
	for i := 1; i <= 12; i++ {
		logger.Info(fmt.Sprintf("msg-%d", i), "seq", i)
	}

	if ring.Len() != cap {
		t.Fatalf("expected Len %d after wrapping, got %d", cap, ring.Len())
	}

	records := ring.Records()
	if len(records) != cap {
		t.Fatalf("expected %d records, got %d", cap, len(records))
	}

	// Should contain records 8 through 12 in chronological order
	for i, r := range records {
		expectedMsg := fmt.Sprintf("msg-%d", i+8)
		if r.Message != expectedMsg {
			t.Errorf("record %d: expected message %q, got %q", i, expectedMsg, r.Message)
		}
	}

	last, ok := ring.Last()
	if !ok || last.Message != "msg-12" {
		t.Errorf("expected last record 'msg-12', got %v (ok=%v)", last, ok)
	}
}

func TestRingLogHandler_Clear(t *testing.T) {
	ring := logging.NewRingLogHandler(10)
	logger := slog.New(ring)

	logger.Info("one")
	logger.Info("two")

	if ring.Len() != 2 {
		t.Fatalf("expected Len 2, got %d", ring.Len())
	}

	ring.Clear()

	if ring.Len() != 0 {
		t.Fatalf("expected Len 0 after clear, got %d", ring.Len())
	}
	if len(ring.Records()) != 0 {
		t.Fatalf("expected 0 records after clear, got %d", len(ring.Records()))
	}
	if _, ok := ring.Last(); ok {
		t.Error("expected Last() to return ok=false after Clear()")
	}
}

func TestRingLogHandler_WithAttrsAndWithGroup(t *testing.T) {
	ring := logging.NewRingLogHandler(10)
	logger := slog.New(ring)

	subLogger := logger.With("component", "scheduler").WithGroup("details")
	subLogger.Info("cycle tick", "count", 100)

	if ring.Len() != 1 {
		t.Fatalf("expected 1 record, got %d", ring.Len())
	}

	formatted := ring.Formatted()
	if len(formatted) != 1 {
		t.Fatalf("expected 1 formatted line, got %d", len(formatted))
	}

	line := formatted[0]
	if !strings.Contains(line, "[INFO]") {
		t.Errorf("expected [INFO] in %q", line)
	}
	if !strings.Contains(line, "cycle tick") {
		t.Errorf("expected 'cycle tick' in %q", line)
	}
	if !strings.Contains(line, "component=scheduler") {
		t.Errorf("expected 'component=scheduler' in %q", line)
	}
	if !strings.Contains(line, "details.count=100") {
		t.Errorf("expected 'details.count=100' in %q", line)
	}
}

func TestRingLogHandler_Formatting(t *testing.T) {
	ring := logging.NewRingLogHandler(10)

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	rec := slog.NewRecord(now, slog.LevelWarn, "disk space low", 0)
	rec.Add("path", "/var/data", "percent", 95)

	_ = ring.Handle(context.Background(), rec)

	formatted := ring.Formatted()
	if len(formatted) != 1 {
		t.Fatalf("expected 1 formatted record, got %d", len(formatted))
	}

	expectedPrefix := "2026-09-06 12:00:00 [WARN] disk space low"
	if !strings.HasPrefix(formatted[0], expectedPrefix) {
		t.Errorf("expected formatted line to start with %q, got %q", expectedPrefix, formatted[0])
	}
	if !strings.Contains(formatted[0], "path=/var/data") || !strings.Contains(formatted[0], "percent=95") {
		t.Errorf("missing attributes in formatted line: %q", formatted[0])
	}
}

func TestRingLogHandler_LevelFilteringAndSetLevel(t *testing.T) {
	ring := logging.NewRingLogHandler(10)
	logger := slog.New(ring)

	logger.Debug("debug 1")
	logger.Info("info 1")
	logger.Warn("warn 1")
	logger.Error("error 1")

	if ring.Len() != 4 {
		t.Fatalf("expected 4 records initially, got %d", ring.Len())
	}

	warnAndAbove := ring.RecordsByLevel(slog.LevelWarn)
	if len(warnAndAbove) != 2 {
		t.Fatalf("expected 2 records >= WARN, got %d", len(warnAndAbove))
	}

	// Change minimum level dynamically to WARN
	ring.SetLevel(slog.LevelWarn)
	if ring.Level() != slog.LevelWarn {
		t.Errorf("expected Level WARN, got %v", ring.Level())
	}

	logger.Debug("debug 2 (should be dropped)")
	logger.Info("info 2 (should be dropped)")
	logger.Warn("warn 2 (should be kept)")

	if ring.Len() != 5 {
		t.Fatalf("expected 5 records after filtering, got %d", ring.Len())
	}

	last, ok := ring.Last()
	if !ok || last.Message != "warn 2 (should be kept)" {
		t.Errorf("unexpected last record: %v", last)
	}
}

func TestRingLogHandler_ConcurrentLogging(t *testing.T) {
	const cap = 50
	ring := logging.NewRingLogHandler(cap)
	logger := slog.New(ring)

	var wg sync.WaitGroup
	const numWriters = 20
	const msgsPerWriter = 50

	// Launch concurrent writers
	for w := 0; w < numWriters; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			sub := logger.With("worker", workerID)
			for m := 0; m < msgsPerWriter; m++ {
				sub.Info("tick", "iteration", m)
			}
		}(w)
	}

	// Launch concurrent readers
	stopReaders := make(chan struct{})
	var readWg sync.WaitGroup
	const numReaders = 5
	for r := 0; r < numReaders; r++ {
		readWg.Add(1)
		go func() {
			defer readWg.Done()
			for {
				select {
				case <-stopReaders:
					return
				default:
					_ = ring.Records()
					_ = ring.Formatted()
					_ = ring.Len()
					_, _ = ring.Last()
				}
			}
		}()
	}

	wg.Wait()
	close(stopReaders)
	readWg.Wait()

	if ring.Len() != cap {
		t.Errorf("expected ring buffer to be full (cap %d), got %d", cap, ring.Len())
	}
	if len(ring.Records()) != cap {
		t.Errorf("expected %d records, got %d", cap, len(ring.Records()))
	}
}

func TestNewStandardHandler(t *testing.T) {
	var buf bytes.Buffer
	handler := logging.NewStandardHandler(&buf, nil)
	logger := slog.New(handler)

	logger.Info("headless standard log message", "service", "gemsub")

	output := buf.String()
	if !strings.Contains(output, "time=") {
		t.Errorf("expected timestamp 'time=' in standard handler output, got: %q", output)
	}
	if !strings.Contains(output, "level=INFO") {
		t.Errorf("expected 'level=INFO' in output, got: %q", output)
	}
	if !strings.Contains(output, "headless standard log message") {
		t.Errorf("expected message in output, got: %q", output)
	}
	if !strings.Contains(output, "service=gemsub") {
		t.Errorf("expected attribute in output, got: %q", output)
	}
}

func TestSetup(t *testing.T) {
	// Test headless setup
	var buf bytes.Buffer
	ring := logging.Setup(true, 10, &buf)
	if ring != nil {
		t.Errorf("expected Setup(headless=true) to return nil ring, got %v", ring)
	}
	slog.Info("test headless log", "key", "val")
	if !strings.Contains(buf.String(), "test headless log") {
		t.Errorf("expected message in buffer, got: %q", buf.String())
	}

	// Test TUI setup
	ring = logging.Setup(false, 10, nil)
	if ring == nil {
		t.Fatal("expected Setup(headless=false) to return non-nil RingLogHandler")
	}
	slog.Info("test tui log", "tui_key", "tui_val")
	if ring.Len() != 1 {
		t.Errorf("expected 1 record in ring handler, got %d", ring.Len())
	}
	records := ring.Records()
	if len(records) != 1 || records[0].Message != "test tui log" {
		t.Errorf("unexpected record in ring handler: %v", records)
	}
}
