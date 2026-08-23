package state

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Store interface {
	IncrWindow(key string, ttl time.Duration, delta int64) (int64, error)
	GetCounter(key string) (int64, error)
	IncrFloat(key string, delta float64) (float64, error)
	GetFloat(key string) (float64, error)
	SetTTLBytes(key string, val []byte, ttl time.Duration) error
	GetBytes(key string) ([]byte, bool, error)
	Healthy() bool
}

type MemoryStore struct {
	mu sync.Mutex
	m  map[string]memEntry
}

type memEntry struct {
	data   []byte
	f      float64
	isF    bool
	expire time.Time
}

func NewMemory() *MemoryStore {
	return &MemoryStore{m: map[string]memEntry{}}
}

func (m *MemoryStore) get(k string) (memEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.m[k]
	if !ok {
		return memEntry{}, false
	}
	if !e.expire.IsZero() && time.Now().After(e.expire) {
		delete(m.m, k)
		return memEntry{}, false
	}
	return e, true
}

func (m *MemoryStore) IncrWindow(key string, ttl time.Duration, delta int64) (int64, error) {
	e, ok := m.get(key)
	cur := int64(0)
	if ok && e.isF {
		cur = int64(e.f)
	}
	nv := cur + delta
	m.mu.Lock()
	m.m[key] = memEntry{f: float64(nv), isF: true, expire: time.Now().Add(ttl)}
	m.mu.Unlock()
	return nv, nil
}

func (e memEntry) fInt() int64 { return int64(e.f) }

func (m *MemoryStore) GetCounter(key string) (int64, error) {
	e, ok := m.get(key)
	if !ok {
		return 0, nil
	}
	return e.fInt(), nil
}

func (m *MemoryStore) IncrFloat(key string, delta float64) (float64, error) {
	e, ok := m.get(key)
	nv := 0.0
	if ok && e.isF {
		nv = e.f
	}
	nv += delta
	m.mu.Lock()
	m.m[key] = memEntry{f: nv, isF: true}
	m.mu.Unlock()
	return nv, nil
}

func (m *MemoryStore) GetFloat(key string) (float64, error) {
	e, ok := m.get(key)
	if !ok {
		return 0, nil
	}
	return e.f, nil
}

func (m *MemoryStore) SetTTLBytes(key string, val []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	m.m[key] = memEntry{data: val, expire: exp}
	return nil
}

func (m *MemoryStore) GetBytes(key string) ([]byte, bool, error) {
	e, ok := m.get(key)
	if !ok {
		return nil, false, nil
	}
	return e.data, true, nil
}

func (m *MemoryStore) Healthy() bool { return true }

type RedisStore struct {
	mu     sync.Mutex
	conn   net.Conn
	rd     *bufio.Reader
	addr   string
	prefix string
	pass   string
	db     int
	up     bool
}

func NewRedis(url, prefix string) (*RedisStore, error) {
	u := strings.TrimPrefix(url, "redis://")
	pass := ""
	if at := strings.Index(u, "@"); at >= 0 {
		cred := u[:at]
		u = u[at+1:]
		if c, _, found := strings.Cut(cred, ":"); found {
			pass = c
		} else {
			pass = cred
		}
	}
	db := 0
	if slash := strings.Index(u, "/"); slash >= 0 {
		dbStr := u[slash+1:]
		u = u[:slash]
		fmt.Sscanf(dbStr, "%d", &db)
	}
	if _, _, err := net.SplitHostPort(u); err != nil {
		u = u + ":6379"
	}
	r := &RedisStore{addr: u, prefix: prefix + ":", pass: pass, db: db}
	if err := r.connect(); err != nil {
		return r, err
	}
	return r, nil
}

func (r *RedisStore) connect() error {
	conn, err := net.DialTimeout("tcp", r.addr, 5*time.Second)
	if err != nil {
		return err
	}
	r.conn = conn
	r.rd = bufio.NewReader(conn)
	r.up = true
	if r.pass != "" {
		if _, err := r.cmd("AUTH", r.pass); err != nil {
			return err
		}
	}
	if r.db > 0 {
		if _, err := r.cmd("SELECT", strconv.Itoa(r.db)); err != nil {
			return err
		}
	}
	return nil
}

func (r *RedisStore) reconnectLocked() error {
	if r.conn != nil {
		r.conn.Close()
		r.conn = nil
	}
	return r.connect()
}

func (r *RedisStore) cmd(args ...string) (any, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if r.conn == nil {
			if err := r.reconnectLocked(); err != nil {
				return nil, err
			}
		}
		var b strings.Builder
		fmt.Fprintf(&b, "*%d\r\n", len(args))
		for _, a := range args {
			fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
		}
		r.conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := r.conn.Write([]byte(b.String())); err != nil {
			r.up = false
			r.conn.Close()
			r.conn = nil
			continue
		}
		reply, err := readReply(r.rd)
		if err != nil {
			r.up = false
			r.conn.Close()
			r.conn = nil
			continue
		}
		if e, ok := reply.(replyError); ok {
			return reply, errors.New(string(e))
		}
		return reply, nil
	}
	return nil, fmt.Errorf("redis: connection failed")
}

type replyError string

func (e replyError) Error() string { return string(e) }

func readReply(rd *bufio.Reader) (any, error) {
	line, err := rd.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return nil, fmt.Errorf("redis: empty reply")
	}
	switch line[0] {
	case '+':
		return line[1:], nil
	case '-':
		return replyError(line[1:]), nil
	case ':':
		n, _ := strconv.ParseInt(line[1:], 10, 64)
		return n, nil
	case '$':
		n, _ := strconv.Atoi(line[1:])
		if n < 0 {
			return nil, nil
		}
		buf := make([]byte, n+2)
		if _, err := ioReadFull(rd, buf); err != nil {
			return nil, err
		}
		return buf[:n], nil
	case '*':
		n, _ := strconv.Atoi(line[1:])
		if n < 0 {
			return nil, nil
		}
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			item, err := readReply(rd)
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("redis: unknown reply %q", line)
	}
}

func ioReadFull(rd *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := rd.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func (r *RedisStore) SetPrefix(p string) { r.prefix = p }
func (r *RedisStore) GetPrefix() string  { return r.prefix }

func (r *RedisStore) k(key string) string { return r.prefix + key }

func (r *RedisStore) do(fn func() (any, error)) (any, error) {
	res, err := fn()
	r.mu.Lock()
	r.up = err == nil
	r.mu.Unlock()
	return res, err
}

func (r *RedisStore) IncrWindow(key string, ttl time.Duration, delta int64) (int64, error) {
	res, err := r.do(func() (any, error) { return r.cmd("INCRBY", r.k(key), strconv.FormatInt(delta, 10)) })
	if err != nil {
		return 0, err
	}
	n, _ := res.(int64)
	ttlMs := ttl.Milliseconds()
	if ttlMs <= 0 {
		ttlMs = 60000
	}
	_, _ = r.do(func() (any, error) { return r.cmd("PEXPIRE", r.k(key), strconv.FormatInt(ttlMs, 10)) })
	return n, nil
}

func (r *RedisStore) GetCounter(key string) (int64, error) {
	res, err := r.do(func() (any, error) { return r.cmd("GET", r.k(key)) })
	if err != nil || res == nil {
		return 0, err
	}
	if b, ok := res.([]byte); ok {
		n, _ := strconv.ParseInt(string(b), 10, 64)
		return n, nil
	}
	return 0, nil
}

func (r *RedisStore) IncrFloat(key string, delta float64) (float64, error) {
	res, err := r.do(func() (any, error) {
		return r.cmd("INCRBYFLOAT", r.k(key), strconv.FormatFloat(delta, 'f', -1, 64))
	})
	if err != nil {
		return 0, err
	}
	if b, ok := res.([]byte); ok {
		f, _ := strconv.ParseFloat(string(b), 64)
		return f, nil
	}
	return 0, nil
}

func (r *RedisStore) GetFloat(key string) (float64, error) {
	res, err := r.do(func() (any, error) { return r.cmd("GET", r.k(key)) })
	if err != nil || res == nil {
		return 0, err
	}
	if b, ok := res.([]byte); ok {
		f, _ := strconv.ParseFloat(string(b), 64)
		return f, nil
	}
	return 0, nil
}

func (r *RedisStore) SetTTLBytes(key string, val []byte, ttl time.Duration) error {
	ms := ttl.Milliseconds()
	_, err := r.do(func() (any, error) {
		if ms > 0 {
			return r.cmd("SET", r.k(key), string(val), "PX", strconv.FormatInt(ms, 10))
		}
		return r.cmd("SET", r.k(key), string(val))
	})
	return err
}

func (r *RedisStore) GetBytes(key string) ([]byte, bool, error) {
	res, err := r.do(func() (any, error) { return r.cmd("GET", r.k(key)) })
	if err != nil || res == nil {
		return nil, false, err
	}
	if b, ok := res.([]byte); ok {
		return b, true, nil
	}
	return nil, false, nil
}

func (r *RedisStore) Healthy() bool {
	_, err := r.do(func() (any, error) { return r.cmd("PING") })
	return err == nil
}
