package provider

import (
	"sync"
	"time"
)

type FailKind int

const (
	Success FailKind = iota
	SoftFail
	HardFail
)

type keyState struct {
	value     string
	coolUntil time.Time
	softFails int
}

type Pool struct {
	mu   sync.Mutex
	keys []*keyState
	idx  int
}

func NewPool(keys []string) *Pool {
	p := &Pool{}
	for _, k := range keys {
		if k != "" {
			p.keys = append(p.keys, &keyState{value: k})
		}
	}
	return p
}

func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.keys)
}

func (p *Pool) KeyHashes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.keys))
	for i, k := range p.keys {
		v := k.value
		if len(v) > 8 {
			out[i] = v[:8] + "..."
		} else {
			out[i] = v
		}
	}
	return out
}

func (p *Pool) Pick() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.keys)
	if n == 0 {
		return "", false
	}
	now := time.Now()
	var fallback *keyState
	for i := 0; i < n; i++ {
		k := p.keys[(p.idx+i)%n]
		if now.After(k.coolUntil) {
			p.idx = (p.idx + i + 1) % n
			return k.value, true
		}
		if fallback == nil || k.coolUntil.Before(fallback.coolUntil) {
			fallback = k
		}
	}
	return fallback.value, true
}

func (p *Pool) Report(key string, kind FailKind) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for _, k := range p.keys {
		if k.value != key {
			continue
		}
		switch kind {
		case Success:
			k.softFails = 0
			k.coolUntil = time.Time{}
		case SoftFail:
			k.softFails++
			backoff := time.Duration(1<<uint(min(k.softFails-1, 5))) * 10 * time.Second
			if backoff > 5*time.Minute {
				backoff = 5 * time.Minute
			}
			if until := now.Add(backoff); until.After(k.coolUntil) {
				k.coolUntil = until
			}
		case HardFail:
			k.coolUntil = now.Add(10 * time.Minute)
		}
		return
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
