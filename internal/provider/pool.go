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
	picks     int64
	fails     int64
	successes int64
}

type Pool struct {
	mu        sync.Mutex
	keys      []*keyState
	idx       int
	weight    int
	fillFirst bool
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

func (p *Pool) SetWeight(w int) {
	if w <= 0 {
		w = 1
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.weight = w
}

func (p *Pool) SetFillFirst(v bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fillFirst = v
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
		k.picks++
		if now.After(k.coolUntil) {
			p.idx = (p.idx + i + 1) % n
			return k.value, true
		}
		if fallback == nil || k.coolUntil.Before(fallback.coolUntil) {
			fallback = k
		}
		if p.fillFirst && i == 0 && now.After(k.coolUntil) {
			break
		}
	}
	if p.fillFirst {
		for _, k := range p.keys {
			if now.After(k.coolUntil) {
				return k.value, true
			}
		}
	}
	return fallback.value, true
}

func (p *Pool) Report(key string, kind FailKind) {
	p.ReportHint(key, kind, 0)
}

func (p *Pool) ReportHint(key string, kind FailKind, minWait time.Duration) {
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
			k.successes++
		case SoftFail:
			k.fails++
			k.softFails++
			backoff := time.Duration(1<<uint(min(k.softFails-1, 5))) * 10 * time.Second
			if backoff > 5*time.Minute {
				backoff = 5 * time.Minute
			}
			if minWait > backoff {
				backoff = minWait
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

type KeyStat struct {
	Key       string    `json:"key"`
	Picks     int64     `json:"attempts"`
	Fails     int64     `json:"fails"`
	Successes int64     `json:"successes"`
	CoolUntil time.Time `json:"cool_until,omitempty"`
}

func (p *Pool) Stats() []KeyStat {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]KeyStat, 0, len(p.keys))
	for _, k := range p.keys {
		v := k.value
		if len(v) > 8 {
			v = v[:8] + "..."
		}
		out = append(out, KeyStat{
			Key: v, Picks: k.picks, Fails: k.fails,
			Successes: k.successes, CoolUntil: k.coolUntil,
		})
	}
	return out
}

func (p *Pool) Weight() int { return p.weight }
