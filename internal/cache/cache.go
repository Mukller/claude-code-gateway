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
	e  Entry
	at time.Time
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
	if time.Since(meta.at) > c.ttl {
		delete(c.m, k)
		return Entry{}, false
	}
	return meta.e, true
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
