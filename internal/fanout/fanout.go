// Package fanout delivers an append-only event history to any number of
// subscribers: late joiners replay from the start, everyone sees events in
// order, and only the subscriber's own goroutine ever closes its channel.
package fanout

import (
	"context"
	"sync"
)

// Log is an ordered event history with subscribers.
type Log[T any] struct {
	mu     sync.Mutex
	events []T
	done   bool
	subs   map[*sub]struct{}
}

type sub struct {
	wake chan struct{}
}

// New returns an empty log.
func New[T any]() *Log[T] {
	return &Log[T]{subs: map[*sub]struct{}{}}
}

// Emit appends an event and wakes every subscriber. Safe after Finish (the
// event is recorded but subscribers have already been told the log ended).
func (l *Log[T]) Emit(ev T) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
	l.wakeAll()
}

// Finish marks the log complete; subscribers close after draining.
func (l *Log[T]) Finish() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.done = true
	l.wakeAll()
}

// Done reports whether Finish has been called.
func (l *Log[T]) Done() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.done
}

// Events returns a copy of the history so far.
func (l *Log[T]) Events() []T {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]T(nil), l.events...)
}

// wakeAll nudges every subscriber without blocking; wake channels are
// buffered so a pending nudge coalesces.
func (l *Log[T]) wakeAll() {
	for s := range l.subs {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

// Subscribe returns a channel that replays the full history, then follows
// new events, closing when the log finishes or ctx ends.
func (l *Log[T]) Subscribe(ctx context.Context) <-chan T {
	out := make(chan T, 64)
	s := &sub{wake: make(chan struct{}, 1)}
	l.mu.Lock()
	l.subs[s] = struct{}{}
	l.mu.Unlock()
	go func() {
		defer close(out)
		defer func() {
			l.mu.Lock()
			delete(l.subs, s)
			l.mu.Unlock()
		}()
		cursor := 0
		for {
			l.mu.Lock()
			if cursor < len(l.events) {
				ev := l.events[cursor]
				cursor++
				l.mu.Unlock()
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
				continue
			}
			done := l.done
			l.mu.Unlock()
			if done {
				return
			}
			select {
			case <-s.wake:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}
