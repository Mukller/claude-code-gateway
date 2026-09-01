package server

import (
	"context"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// defaultMaxAdminPerMin is the default per-IP cap for admin endpoints.
	defaultMaxAdminPerMin = 30
)

// limiter is a tiny token-bucket per remote-addr. Sufficient for protecting
// admin endpoints from brute force or runaway scripts.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // tokens per second
	burst   float64
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(rpm int) *limiter {
	if rpm <= 0 {
		rpm = defaultMaxAdminPerMin
	}
	rate := float64(rpm) / 60.0
	return &limiter{
		buckets: map[string]*bucket{},
		rate:    rate,
		burst:   float64(rpm),
	}
}

func (l *limiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// inFlight tracks active long-running requests for graceful shutdown.
type inFlight struct {
	count   atomic.Int64
	done    chan struct{}
	closing atomic.Bool
}

func newInFlight() *inFlight {
	return &inFlight{done: make(chan struct{})}
}

func (i *inFlight) begin() func() {
	i.count.Add(1)
	return i.end
}

func (i *inFlight) end() {
	if i.count.Add(-1) == 0 && i.closing.Load() {
		select {
		case <-i.done:
		default:
			close(i.done)
		}
	}
}

// wait blocks until all in-flight requests complete or the context expires.
func (i *inFlight) wait(ctx context.Context) error {
	i.closing.Store(true)
	if i.count.Load() == 0 {
		return nil
	}
	select {
	case <-i.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// withInFlight wraps a handler and tracks it for graceful shutdown.
// It also applies the per-IP rate limiter for admin endpoints.
func (s *Server) withInFlight(lim *limiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lim != nil {
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			if host == "" {
				host = r.RemoteAddr
			}
			if !lim.allow(host) {
				http.Error(w, "rate limited", http.StatusTooManyRequests)
				return
			}
		}
		release := s.inflight.begin()
		defer release()
		next.ServeHTTP(w, r)
	})
}
