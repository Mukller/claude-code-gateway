package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

type Entry struct {
	Body []byte
	In   int64
	Out  int64
}

type Cache struct {
	mu    sync.Mutex
	ttl   time.Duration
	max   int
	m     map[string]entryMeta
	order []string
}

type entryMeta struct {
	e   Entry
	at  time.Time
	ttl time.Duration
}

func New(ttl time.Duration, maxEntries int) *Cache {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if maxEntries <= 0 {
		maxEntries = 128
	}
	return &Cache{
		ttl: ttl,
		max: maxEntries,
		m:   map[string]entryMeta{},
	}
}

func Key(provider, model string, payload []byte) string {
	h := sha256.New()
	h.Write([]byte(provider))
	h.Write([]byte{0})
	h.Write([]byte(model))
	h.Write([]byte{0})
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

func (c *Cache) Get(k string) (Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	meta, ok := c.m[k]
	if !ok {
		return Entry{}, false
	}
	ttl := meta.ttl
	if ttl <= 0 {
		ttl = c.ttl
	}
	if time.Since(meta.at) > ttl {
		delete(c.m, k)
		return Entry{}, false
	}
	return meta.e, true
}

func (c *Cache) PutWithTTL(k string, e Entry, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.m[k]; !exists {
		c.order = append(c.order, k)
	}
	c.m[k] = entryMeta{e: e, at: time.Now(), ttl: ttl}
	for len(c.order) > c.max {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.m, oldest)
	}
}

func (c *Cache) Flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m = map[string]entryMeta{}
	c.order = nil
}

func (c *Cache) Put(k string, e Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.m[k]; !exists {
		c.order = append(c.order, k)
	}
	c.m[k] = entryMeta{e: e, at: time.Now()}
	for len(c.order) > c.max {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.m, oldest)
	}
}

func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

type semEntry struct {
	vec []float32
	key string
	at  time.Time
}

type SemanticIndex struct {
	mu        sync.Mutex
	threshold float64
	max       int
	entries   []semEntry
}

func NewSemantic(threshold float64, max int) *SemanticIndex {
	if threshold <= 0 {
		threshold = 0.93
	}
	if max <= 0 {
		max = 1000
	}
	return &SemanticIndex{threshold: threshold, max: max}
}

func Cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (sqrtF(na) * sqrtF(nb))
}

func sqrtF(x float64) float64 {
	if x <= 0 {
		return 0
	}
	g := x
	for i := 0; i < 24; i++ {
		g = (g + x/g) / 2
	}
	return g
}

func (s *SemanticIndex) Add(key string, vec []float32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, semEntry{vec: vec, key: key, at: time.Now()})
	if len(s.entries) > s.max {
		s.entries = s.entries[len(s.entries)-s.max:]
	}
}

func (s *SemanticIndex) Search(vec []float32) (key string, sim float64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	best := -1.0
	var bestKey string
	for _, e := range s.entries {
		if c := Cosine(vec, e.vec); c > best {
			best = c
			bestKey = e.key
		}
	}
	if best >= s.threshold && bestKey != "" {
		return bestKey, best, true
	}
	return "", 0, false
}

func (s *SemanticIndex) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
