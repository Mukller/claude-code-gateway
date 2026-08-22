package ratelimit

import (
	"sync"
	"time"
)

type Limiter struct {
	mu  sync.Mutex
	rpm int
	win int64
	n   int
}

func New(rpm int) *Limiter {
	return &Limiter{rpm: rpm}
}

func (l *Limiter) Allow(now time.Time) bool {
	if l.rpm <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	w := now.Unix() / 60
	if w != l.win {
		l.win = w
		l.n = 0
	}
	l.n++
	return l.n <= l.rpm
}
