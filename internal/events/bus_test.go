package events_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"gemsub/internal/events"
	"gemsub/internal/parser"
	"gemsub/internal/store"
)

func TestEventBus_DeliveryAndOrdering(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	ch1 := bus.Subscribe()
	defer bus.Unsubscribe(ch1)

	ch2 := bus.Subscribe()
	defer bus.Unsubscribe(ch2)

	now := time.Now()
	testEvents := []any{
		events.CycleStarted{
			StartedAt: now,
		},
		events.CandidatesLoaded{
			Total: 2,
			Candidates: []parser.Candidate{
				{Link: "vless://node1"},
				{Link: "vless://node2"},
			},
		},
		events.ProbeCompleted{
			ProgressMetrics: events.ProgressMetrics{
				Completed:    1,
				Total:        2,
				Passed:       1,
				Failed:       0,
				Inconclusive: 0,
			},
			Result: store.Result{
				Link:   "vless://node1",
				Status: store.StatusPassed,
			},
		},
		events.ProbeCompleted{
			ProgressMetrics: events.ProgressMetrics{
				Completed:    2,
				Total:        2,
				Passed:       1,
				Failed:       1,
				Inconclusive: 0,
			},
			Result: store.Result{
				Link:   "vless://node2",
				Status: store.StatusFailed,
			},
		},
		events.CycleFinished{
			ProgressMetrics: events.ProgressMetrics{
				Completed:    2,
				Total:        2,
				Passed:       1,
				Failed:       1,
				Inconclusive: 0,
			},
			Duration:  50 * time.Millisecond,
			Cancelled: false,
			Servable:  1,
		},
	}

	for _, evt := range testEvents {
		bus.Publish(evt)
	}

	verifySubscriber := func(subName string, ch <-chan any) {
		for i, expected := range testEvents {
			select {
			case actual, ok := <-ch:
				if !ok {
					t.Fatalf("[%s] channel closed unexpectedly at index %d", subName, i)
				}
				switch exp := expected.(type) {
				case events.CycleStarted:
					act, ok := actual.(events.CycleStarted)
					if !ok {
						t.Fatalf("[%s] index %d: expected CycleStarted, got %T", subName, i, actual)
					}
					if !act.StartedAt.Equal(exp.StartedAt) {
						t.Errorf("[%s] index %d: StartedAt mismatch: %v != %v", subName, i, act.StartedAt, exp.StartedAt)
					}
				case events.CandidatesLoaded:
					act, ok := actual.(events.CandidatesLoaded)
					if !ok {
						t.Fatalf("[%s] index %d: expected CandidatesLoaded, got %T", subName, i, actual)
					}
					if act.Total != exp.Total || len(act.Candidates) != len(exp.Candidates) {
						t.Errorf("[%s] index %d: CandidatesLoaded mismatch: got %+v, exp %+v", subName, i, act, exp)
					}
				case events.ProbeCompleted:
					act, ok := actual.(events.ProbeCompleted)
					if !ok {
						t.Fatalf("[%s] index %d: expected ProbeCompleted, got %T", subName, i, actual)
					}
					if act.Completed != exp.Completed || act.Total != exp.Total ||
						act.Passed != exp.Passed || act.Failed != exp.Failed ||
						act.Inconclusive != exp.Inconclusive {
						t.Errorf("[%s] index %d: metrics mismatch: got %+v, exp %+v", subName, i, act.ProgressMetrics, exp.ProgressMetrics)
					}
					if act.Result.Link != exp.Result.Link || act.Result.Status != exp.Result.Status {
						t.Errorf("[%s] index %d: result mismatch: got %+v, exp %+v", subName, i, act.Result, exp.Result)
					}
				case events.CycleFinished:
					act, ok := actual.(events.CycleFinished)
					if !ok {
						t.Fatalf("[%s] index %d: expected CycleFinished, got %T", subName, i, actual)
					}
					if act.Completed != exp.Completed || act.Total != exp.Total ||
						act.Passed != exp.Passed || act.Failed != exp.Failed ||
						act.Inconclusive != exp.Inconclusive || act.Cancelled != exp.Cancelled ||
						act.Servable != exp.Servable {
						t.Errorf("[%s] index %d: CycleFinished mismatch: got %+v, exp %+v", subName, i, act, exp)
					}
				default:
					t.Fatalf("unexpected type: %T", expected)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("[%s] timed out waiting for event at index %d", subName, i)
			}
		}
	}

	verifySubscriber("sub1", ch1)
	verifySubscriber("sub2", ch2)
}

func TestEventBus_NonBlockingPublish(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	// Slow subscriber with a small channel buffer of 1 that doesn't read
	slowCh := bus.Subscribe(1)
	defer bus.Unsubscribe(slowCh)

	// Fast subscriber that reads continuously
	fastCh := bus.Subscribe(100)
	defer bus.Unsubscribe(fastCh)

	const numEvents = 50

	publishDone := make(chan struct{})
	go func() {
		defer close(publishDone)
		for i := 0; i < numEvents; i++ {
			bus.Publish(events.ProbeCompleted{
				ProgressMetrics: events.ProgressMetrics{
					Completed: i + 1,
					Total:     numEvents,
				},
			})
		}
	}()

	// Publishing all events must complete quickly without blocking on slowCh
	select {
	case <-publishDone:
		// Success: publish did not block
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Publish blocked on slow subscriber")
	}

	// Verify fast subscriber received all 50 events in order
	for i := 0; i < numEvents; i++ {
		select {
		case evt := <-fastCh:
			pc, ok := evt.(events.ProbeCompleted)
			if !ok {
				t.Fatalf("fastCh expected ProbeCompleted, got %T", evt)
			}
			if pc.Completed != i+1 {
				t.Fatalf("fastCh expected Completed=%d, got %d", i+1, pc.Completed)
			}
		case <-time.After(1 * time.Second):
			t.Fatalf("fastCh timed out waiting for event %d", i)
		}
	}

	// Verify slow subscriber receives all 50 events in order when reading
	for i := 0; i < numEvents; i++ {
		select {
		case evt := <-slowCh:
			pc, ok := evt.(events.ProbeCompleted)
			if !ok {
				t.Fatalf("slowCh expected ProbeCompleted, got %T", evt)
			}
			if pc.Completed != i+1 {
				t.Fatalf("slowCh expected Completed=%d, got %d", i+1, pc.Completed)
			}
		case <-time.After(1 * time.Second):
			t.Fatalf("slowCh timed out waiting for event %d", i)
		}
	}
}

func TestEventBus_Unsubscribe(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	ch := bus.Subscribe()
	bus.Publish(events.CycleStarted{})

	select {
	case <-ch:
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for event before unsubscribe")
	}

	bus.Unsubscribe(ch)

	// Channel should be closed upon unsubscribe
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel to be closed after Unsubscribe")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("channel not closed after Unsubscribe")
	}

	// New events should not cause panic or delivery
	bus.Publish(events.CycleStarted{})
}

func TestEventBus_Close(t *testing.T) {
	bus := events.New()

	ch1 := bus.Subscribe()
	ch2 := bus.Subscribe()

	bus.Publish(events.CycleStarted{})

	<-ch1
	<-ch2

	bus.Close()

	// Both channels should be closed
	select {
	case _, ok := <-ch1:
		if ok {
			t.Fatal("ch1 should be closed")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("ch1 was not closed")
	}

	select {
	case _, ok := <-ch2:
		if ok {
			t.Fatal("ch2 should be closed")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("ch2 was not closed")
	}

	// Calling Close again must be safe and idempotent
	bus.Close()

	// Publishing to closed bus must be safe
	bus.Publish(events.CycleStarted{})
}

func TestEventBus_ConcurrentPublish(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	ch := bus.Subscribe(500)
	defer bus.Unsubscribe(ch)

	const goroutines = 10
	const perGoroutine = 50
	const total = goroutines * perGoroutine

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				bus.Publish(events.ProbeCompleted{
					Result: store.Result{
						Link: fmt.Sprintf("vless://worker-%d-cand-%d", id, i),
					},
				})
			}
		}(g)
	}

	wg.Wait()

	received := 0
	for received < total {
		select {
		case _, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed prematurely at %d of %d", received, total)
			}
			received++
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for events, received %d of %d", received, total)
		}
	}

	if received != total {
		t.Fatalf("expected %d events, got %d", total, received)
	}
}
