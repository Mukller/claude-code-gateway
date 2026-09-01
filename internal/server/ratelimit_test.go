package server

import (
	"testing"
	"time"
)

func TestRateLimiterAllowsBurst(t *testing.T) {
	l := newLimiter(60)
	if !l.allow("127.0.0.1") {
		t.Fatal("first request denied")
	}
}

func TestRateLimiterBlocks(t *testing.T) {
	l := newLimiter(1)
	if !l.allow("127.0.0.1") {
		t.Fatal("first request should be allowed")
	}
	if l.allow("127.0.0.1") {
		t.Fatal("second request should be denied at 1 rpm / 1 burst")
	}
}

func TestRateLimiterPerIP(t *testing.T) {
	l := newLimiter(2)
	if !l.allow("10.0.0.1") {
		t.Fatal("first IP should be allowed")
	}
	if !l.allow("10.0.0.2") {
		t.Fatal("second IP should be allowed (different bucket)")
	}
}

func TestInFlightDrains(t *testing.T) {
	ifl := newInFlight()
	release1 := ifl.begin()
	release2 := ifl.begin()
	if ifl.count.Load() != 2 {
		t.Fatalf("count = %d, want 2", ifl.count.Load())
	}
	release1()
	if ifl.count.Load() != 1 {
		t.Fatal("count did not decrement")
	}
	release2()
	if ifl.count.Load() != 0 {
		t.Fatal("count should be 0")
	}
	if ifl.closing.Load() {
		t.Fatal("closing should still be false without wait()")
	}
	bg, cancel := contextWithTimeout()
	defer cancel()
	done := make(chan struct{})
	go func() {
		ifl.wait(bg)
		close(done)
	}()
	ifl.begin()()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("wait should return on context cancel")
	}
}

func contextWithTimeout() (ctx contextLike, cancel func()) {
	return contextLike{0}, func() {}
}

type contextLike struct{ _ int }

func (contextLike) Deadline() (time.Time, bool) { return time.Now().Add(10 * time.Millisecond), true }
func (contextLike) Done() <-chan struct{} {
	ch := make(chan struct{})
	go func() { time.Sleep(10 * time.Millisecond); close(ch) }()
	return ch
}
func (contextLike) Err() error    { return nil }
func (contextLike) Value(any) any { return nil }
