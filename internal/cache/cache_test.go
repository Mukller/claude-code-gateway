package cache

import (
	"strings"
	"testing"
	"time"
)

func TestCachePutGetTTL(t *testing.T) {
	c := New(50*time.Millisecond, 10)
	k := Key("prov", "model", []byte("payload"))
	if _, ok := c.Get(k); ok {
		t.Fatal("expected miss")
	}
	c.Put(k, Entry{Body: []byte("hello"), In: 1, Out: 2})
	e, ok := c.Get(k)
	if !ok || string(e.Body) != "hello" {
		t.Fatalf("expected hit, got %+v", e)
	}
	time.Sleep(80 * time.Millisecond)
	if _, ok := c.Get(k); ok {
		t.Fatal("expected expiry")
	}
}

func TestCacheEviction(t *testing.T) {
	c := New(time.Minute, 3)
	for i := 0; i < 5; i++ {
		k := Key("p", "m", []byte{byte('a' + i)})
		c.Put(k, Entry{Body: []byte(strings.Repeat("x", i+1))})
	}
	if c.Len() != 3 {
		t.Fatalf("len = %d, want 3", c.Len())
	}
	if _, ok := c.Get(Key("p", "m", []byte("a"))); ok {
		t.Fatal("oldest entry must be evicted first")
	}
	if _, ok := c.Get(Key("p", "m", []byte("e"))); !ok {
		t.Fatal("newest entry must survive")
	}
}

func TestKeyDistinctness(t *testing.T) {
	a := Key("p1", "m", []byte("x"))
	b := Key("p2", "m", []byte("x"))
	d := Key("p1", "m2", []byte("x"))
	f := Key("p1", "m", []byte("y"))
	set := map[string]bool{a: true, b: true, d: true, f: true}
	if len(set) != 4 {
		t.Fatal("keys collide")
	}
}
