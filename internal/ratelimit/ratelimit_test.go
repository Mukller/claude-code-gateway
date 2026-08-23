package ratelimit

import (
	"sync"
	"testing"
	"time"
)

func TestAllowPerKey(t *testing.T) {
	l := New(2)
	now := time.Now()
	for i := 0; i < 2; i++ {
		if !l.Allow("tok-a", now) {
			t.Fatalf("request %d for tok-a denied", i+1)
		}
	}
	if l.Allow("tok-a", now) {
		t.Fatal("third request must be denied")
	}
	if !l.Allow("tok-b", now) {
		t.Fatal("tok-b must have its own budget")
	}
}

func TestWindowReset(t *testing.T) {
	l := New(1)
	now := time.Unix(120, 0)
	if !l.Allow("k", now) {
		t.Fatal("first allowed")
	}
	if l.Allow("k", now.Add(time.Second)) {
		t.Fatal("same window denied")
	}
	if !l.Allow("k", now.Add(61*time.Second)) {
		t.Fatal("next window allowed again")
	}
}

func TestDisabled(t *testing.T) {
	l := New(0)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); l.Allow("x", time.Now()) }()
	}
	wg.Wait()
	if !l.Allow("x", time.Now()) {
		t.Fatal("rpm=0 must be unlimited")
	}
}
