package ratelimit

import (
	"sync"
	"time"
)

type bucket struct {
	win int64
	n   int
}

type Limiter struct {
	mu      sync.Mutex
	rpm     int
	buckets map[string]*bucket
}

func New(rpm int) *Limiter {
	return &Limiter{rpm: rpm, buckets: map[string]*bucket{}}
}

func (l *Limiter) Allow(key string, now time.Time) bool {
	if l.rpm <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	w := now.Unix() / 60
	b, ok := l.buckets[key]
	if !ok || b.win != w {
		if len(l.buckets) > 4096 {
			l.buckets = map[string]*bucket{}
		}
		b = &bucket{win: w}
		l.buckets[key] = b
	}
	b.n++
	return b.n <= l.rpm
}
