package fanout

import (
	"context"
	"sync"
	"testing"
	"time"
)

func drain(ch <-chan int, d time.Duration) []int {
	var out []int
	timer := time.After(d)
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, v)
		case <-timer:
			return out
		}
	}
}

func TestReplayThenLiveThenClose(t *testing.T) {
	l := New[int]()
	l.Emit(1)
	l.Emit(2)
	ch := l.Subscribe(context.Background())
	l.Emit(3)
	l.Finish()
	got := drain(ch, time.Second)
	if len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("got %v", got)
	}
	// A subscriber after Finish still replays everything and closes.
	if got := drain(l.Subscribe(context.Background()), time.Second); len(got) != 3 {
		t.Errorf("late replay = %v", got)
	}
}

func TestConcurrentEmitAndFinishNeverPanics(t *testing.T) {
	for i := 0; i < 50; i++ {
		l := New[int]()
		var wg sync.WaitGroup
		for s := 0; s < 5; s++ {
			ch := l.Subscribe(context.Background())
			wg.Add(1)
			go func() {
				defer wg.Done()
				n := 0
				for range ch {
					n++
				}
				if n != 100 {
					t.Errorf("subscriber saw %d of 100", n)
				}
			}()
		}
		for e := 0; e < 100; e++ {
			l.Emit(e)
		}
		l.Finish()
		wg.Wait()
	}
}

func TestSubscriberCancel(t *testing.T) {
	l := New[int]()
	ctx, cancel := context.WithCancel(context.Background())
	ch := l.Subscribe(ctx)
	cancel()
	if _, ok := <-ch; ok {
		// draining any buffered value is fine; the channel must close soon.
		select {
		case _, ok := <-ch:
			if ok {
				t.Error("channel still open after cancel")
			}
		case <-time.After(time.Second):
			t.Error("channel not closed after cancel")
		}
	}
	l.mu.Lock()
	n := len(l.subs)
	l.mu.Unlock()
	if n != 0 {
		t.Errorf("subscriber not removed: %d", n)
	}
}
