package events

import (
	"sync"
)

// DefaultChannelBuffer is the default buffer size for subscriber channels.
const DefaultChannelBuffer = 64

type subscriber struct {
	ch     chan any
	queue  []any
	cond   *sync.Cond
	mu     sync.Mutex
	closed bool
	done   chan struct{}
}

func newSubscriber(bufSize int) *subscriber {
	s := &subscriber{
		ch:   make(chan any, bufSize),
		done: make(chan struct{}),
	}
	s.cond = sync.NewCond(&s.mu)
	s.start()
	return s
}

func (s *subscriber) start() {
	go func() {
		defer close(s.ch)
		for {
			s.mu.Lock()
			for len(s.queue) == 0 && !s.closed {
				s.cond.Wait()
			}
			if len(s.queue) == 0 && s.closed {
				s.mu.Unlock()
				return
			}
			evt := s.queue[0]
			s.queue[0] = nil
			s.queue = s.queue[1:]
			s.mu.Unlock()

			select {
			case s.ch <- evt:
			case <-s.done:
				return
			}
		}
	}()
}

func (s *subscriber) close() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.done)
		s.cond.Broadcast()
	}
	s.mu.Unlock()
}

// EventBus provides thread-safe, non-blocking pub/sub broadcasting of typed events.
type EventBus struct {
	mu          sync.RWMutex
	subscribers []*subscriber
	closed      bool
}

// New creates and returns an initialized EventBus.
func New() *EventBus {
	return &EventBus{}
}

// NewBus is an alias for New.
func NewBus() *EventBus {
	return New()
}

// Subscribe registers a new subscriber channel.
// An optional channel buffer size can be supplied.
func (b *EventBus) Subscribe(bufSize ...int) <-chan any {
	if b == nil {
		return nil
	}
	size := DefaultChannelBuffer
	if len(bufSize) > 0 && bufSize[0] >= 0 {
		size = bufSize[0]
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		closedCh := make(chan any)
		close(closedCh)
		return closedCh
	}

	sub := newSubscriber(size)
	b.subscribers = append(b.subscribers, sub)
	return sub.ch
}

// Unsubscribe removes a subscriber channel and closes it.
func (b *EventBus) Unsubscribe(ch <-chan any) {
	if b == nil || ch == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	for i, sub := range b.subscribers {
		if sub.ch == ch {
			b.subscribers = append(b.subscribers[:i], b.subscribers[i+1:]...)
			sub.close()
			break
		}
	}
}

// Publish broadcasts an event to all subscribers without blocking the caller.
func (b *EventBus) Publish(event any) {
	if b == nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return
	}

	for _, sub := range b.subscribers {
		sub.mu.Lock()
		if !sub.closed {
			sub.queue = append(sub.queue, event)
			sub.cond.Signal()
		}
		sub.mu.Unlock()
	}
}

// Close shuts down the event bus, closes all subscriber channels, and prevents further publishes.
func (b *EventBus) Close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return
	}
	b.closed = true

	for _, sub := range b.subscribers {
		sub.close()
	}
	b.subscribers = nil
}

// SubscriberCount returns the current number of active subscribers.
func (b *EventBus) SubscriberCount() int {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}
