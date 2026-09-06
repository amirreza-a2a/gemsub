package logging

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
)

// ringBuffer stores a fixed-capacity circular buffer of slog.Record.
type ringBuffer struct {
	mu       sync.RWMutex
	records  []slog.Record
	capacity int
	start    int
	count    int
	minLevel slog.Level
}

// RingLogHandler is an slog.Handler that captures recent log records in memory
// in a fixed-size ring buffer for UI viewing.
type RingLogHandler struct {
	ring   *ringBuffer
	attrs  []slog.Attr
	groups []string
}

// NewRingLogHandler creates a new RingLogHandler with the specified capacity.
// If capacity <= 0, a default capacity of 1000 records is used.
// By default, it captures log records of LevelDebug and higher.
func NewRingLogHandler(capacity int) *RingLogHandler {
	return NewRingLogHandlerWithOptions(capacity, nil)
}

// NewRingLogHandlerWithOptions creates a RingLogHandler with custom slog.HandlerOptions.
func NewRingLogHandlerWithOptions(capacity int, opts *slog.HandlerOptions) *RingLogHandler {
	if capacity <= 0 {
		capacity = 1000
	}
	minLvl := slog.LevelDebug
	if opts != nil && opts.Level != nil {
		minLvl = opts.Level.Level()
	}
	return &RingLogHandler{
		ring: &ringBuffer{
			records:  make([]slog.Record, capacity),
			capacity: capacity,
			minLevel: minLvl,
		},
	}
}

// Enabled reports whether the handler handles records at the given level.
func (h *RingLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	h.ring.mu.RLock()
	defer h.ring.mu.RUnlock()
	return level >= h.ring.minLevel
}

// Handle handles the Record by copying it into the shared in-memory ring buffer.
func (h *RingLogHandler) Handle(_ context.Context, r slog.Record) error {
	h.ring.mu.RLock()
	enabled := r.Level >= h.ring.minLevel
	h.ring.mu.RUnlock()
	if !enabled {
		return nil
	}

	newRec := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)

	// Add handler-level attributes
	if len(h.attrs) > 0 {
		newRec.AddAttrs(h.attrs...)
	}

	// Collect record-level attributes
	var recAttrs []slog.Attr
	r.Attrs(func(a slog.Attr) bool {
		recAttrs = append(recAttrs, a)
		return true
	})

	// Wrap attributes in active groups if any
	if len(h.groups) > 0 && len(recAttrs) > 0 {
		nested := slog.Group(h.groups[len(h.groups)-1], attrsToAny(recAttrs)...)
		for i := len(h.groups) - 2; i >= 0; i-- {
			nested = slog.Group(h.groups[i], nested)
		}
		newRec.AddAttrs(nested)
	} else if len(recAttrs) > 0 {
		newRec.AddAttrs(recAttrs...)
	}

	h.ring.mu.Lock()
	if h.ring.count < h.ring.capacity {
		idx := (h.ring.start + h.ring.count) % h.ring.capacity
		h.ring.records[idx] = newRec
		h.ring.count++
	} else {
		// Buffer full: overwrite the oldest entry
		h.ring.records[h.ring.start] = newRec
		h.ring.start = (h.ring.start + 1) % h.ring.capacity
	}
	h.ring.mu.Unlock()

	return nil
}

// WithAttrs returns a new handler that shares the same underlying ring buffer,
// prepending the given attributes to all subsequently logged records.
func (h *RingLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}

	var newAttrs []slog.Attr
	if len(h.groups) > 0 {
		nested := slog.Group(h.groups[len(h.groups)-1], attrsToAny(attrs)...)
		for i := len(h.groups) - 2; i >= 0; i-- {
			nested = slog.Group(h.groups[i], nested)
		}
		newAttrs = append(slices.Clone(h.attrs), nested)
	} else {
		newAttrs = append(slices.Clone(h.attrs), attrs...)
	}

	return &RingLogHandler{
		ring:   h.ring,
		attrs:  newAttrs,
		groups: slices.Clone(h.groups),
	}
}

// WithGroup returns a new handler with the given group name appended to the receiver's groups.
func (h *RingLogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &RingLogHandler{
		ring:   h.ring,
		attrs:  slices.Clone(h.attrs),
		groups: append(slices.Clone(h.groups), name),
	}
}

// Records returns a copy of all buffered log records in chronological order (oldest first).
func (h *RingLogHandler) Records() []slog.Record {
	h.ring.mu.RLock()
	defer h.ring.mu.RUnlock()

	result := make([]slog.Record, h.ring.count)
	for i := 0; i < h.ring.count; i++ {
		idx := (h.ring.start + i) % h.ring.capacity
		result[i] = h.ring.records[idx].Clone()
	}
	return result
}

// RecordsByLevel returns all buffered log records with level >= minLevel in chronological order.
func (h *RingLogHandler) RecordsByLevel(minLevel slog.Level) []slog.Record {
	h.ring.mu.RLock()
	defer h.ring.mu.RUnlock()

	var result []slog.Record
	for i := 0; i < h.ring.count; i++ {
		idx := (h.ring.start + i) % h.ring.capacity
		rec := h.ring.records[idx]
		if rec.Level >= minLevel {
			result = append(result, rec.Clone())
		}
	}
	return result
}

// Formatted returns all buffered records formatted as human-readable strings.
func (h *RingLogHandler) Formatted() []string {
	records := h.Records()
	lines := make([]string, len(records))
	for i, r := range records {
		lines[i] = FormatRecord(r)
	}
	return lines
}

// Len returns the current number of log records stored in the ring buffer.
func (h *RingLogHandler) Len() int {
	h.ring.mu.RLock()
	defer h.ring.mu.RUnlock()
	return h.ring.count
}

// Cap returns the capacity of the ring buffer.
func (h *RingLogHandler) Cap() int {
	return h.ring.capacity
}

// Last returns the most recent log record added to the buffer, or false if empty.
func (h *RingLogHandler) Last() (slog.Record, bool) {
	h.ring.mu.RLock()
	defer h.ring.mu.RUnlock()
	if h.ring.count == 0 {
		return slog.Record{}, false
	}
	lastIdx := (h.ring.start + h.ring.count - 1) % h.ring.capacity
	return h.ring.records[lastIdx].Clone(), true
}

// Clear empties all log records from the ring buffer.
func (h *RingLogHandler) Clear() {
	h.ring.mu.Lock()
	defer h.ring.mu.Unlock()
	h.ring.records = make([]slog.Record, h.ring.capacity)
	h.ring.start = 0
	h.ring.count = 0
}

// SetLevel dynamically updates the minimum log level handled by the ring buffer.
func (h *RingLogHandler) SetLevel(l slog.Level) {
	h.ring.mu.Lock()
	defer h.ring.mu.Unlock()
	h.ring.minLevel = l
}

// Level returns the current minimum log level handled by the ring buffer.
func (h *RingLogHandler) Level() slog.Level {
	h.ring.mu.RLock()
	defer h.ring.mu.RUnlock()
	return h.ring.minLevel
}

// FormatRecord formats an slog.Record into a clean, human-readable string suitable for UI display.
func FormatRecord(r slog.Record) string {
	var sb strings.Builder

	if !r.Time.IsZero() {
		sb.WriteString(r.Time.Format("2006-01-02 15:04:05"))
		sb.WriteString(" ")
	}

	sb.WriteString("[")
	sb.WriteString(r.Level.String())
	sb.WriteString("] ")
	sb.WriteString(r.Message)

	if r.NumAttrs() > 0 {
		r.Attrs(func(a slog.Attr) bool {
			appendAttr(&sb, a, "")
			return true
		})
	}

	return sb.String()
}

func appendAttr(sb *strings.Builder, a slog.Attr, prefix string) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	key := a.Key
	if prefix != "" {
		key = prefix + "." + key
	}
	if a.Value.Kind() == slog.KindGroup {
		attrs := a.Value.Group()
		for _, sub := range attrs {
			appendAttr(sb, sub, key)
		}
		return
	}
	sb.WriteString(" ")
	sb.WriteString(key)
	sb.WriteString("=")
	sb.WriteString(a.Value.String())
}

func attrsToAny(attrs []slog.Attr) []any {
	args := make([]any, len(attrs))
	for i, a := range attrs {
		args[i] = a
	}
	return args
}
