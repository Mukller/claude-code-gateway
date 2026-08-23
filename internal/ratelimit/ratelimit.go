package ratelimit

import (
	"sync"
	"time"

	"claude-code-gateway/internal/state"
)

type bucket struct {
	win int64
	n   int
	tok int64
}

type Limiter struct {
	mu      sync.Mutex
	rpm     int
	buckets map[string]*bucket
	store   state.Store
	prefix  string
}

func New(rpm int) *Limiter {
	return &Limiter{rpm: rpm, buckets: map[string]*bucket{}}
}

func (l *Limiter) SetStore(s state.Store, prefix string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.store = s
	l.prefix = prefix + ":rl"
}

func (l *Limiter) Allow(key string, now time.Time) bool {
	if l.rpm <= 0 {
		return true
	}
	if l.store != nil {
		if n, err := l.store.IncrWindow(l.prefix+":rpm:"+key, time.Minute, 1); err == nil {
			return n <= int64(l.rpm)
		}
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

func (l *Limiter) AllowTokens(key string, est int64, tpm int64, now time.Time) bool {
	if tpm <= 0 || est <= 0 {
		return true
	}
	k := l.prefix + ":tpm:" + key
	if l.store != nil {
		n, err := l.store.IncrWindow(k, time.Minute, est)
		if err == nil {
			if n <= tpm {
				return true
			}
			l.store.IncrFloat(k, -float64(est))
			return false
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	w := now.Unix() / 60
	bk := "tpm:" + key
	b, ok := l.buckets[bk]
	if !ok || b.win != w {
		b = &bucket{win: w}
		l.buckets[bk] = b
	}
	if b.tok+est > tpm {
		return false
	}
	b.tok += est
	return true
}
