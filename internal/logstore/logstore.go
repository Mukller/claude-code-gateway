package logstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Record struct {
	Time        time.Time `json:"time"`
	Token       string    `json:"token,omitempty"`
	Model       string    `json:"model"`
	TargetModel string    `json:"target_model,omitempty"`
	Provider    string    `json:"provider,omitempty"`
	Status      int       `json:"status"`
	LatencyMs   int64     `json:"latency_ms"`
	Stream      bool      `json:"stream"`
	Cached      bool      `json:"cached,omitempty"`
	InTok       int64     `json:"input_tokens"`
	OutTok      int64     `json:"output_tokens"`
	CacheRead   int64     `json:"cache_read_tokens,omitempty"`
	CacheWrite  int64     `json:"cache_write_tokens,omitempty"`
	CostUSD     float64   `json:"cost_usd"`
	Error       string    `json:"error,omitempty"`
}

type Stat struct {
	Requests int64   `json:"requests"`
	Errors   int64   `json:"errors"`
	InTok    int64   `json:"input_tokens"`
	OutTok   int64   `json:"output_tokens"`
	CostUSD  float64 `json:"cost_usd"`
}

func (s *Stat) add(r Record) {
	s.Requests++
	if r.Error != "" || r.Status >= 400 || r.Status == 0 {
		s.Errors++
	}
	s.InTok += r.InTok + r.CacheRead + r.CacheWrite
	s.OutTok += r.OutTok
	s.CostUSD += r.CostUSD
}

type Store struct {
	mu      sync.Mutex
	f       *os.File
	ringMax int
	ring    []Record
	days    map[string]*Stat
	hours   map[string]*Stat
	models  map[string]*Stat
	provs   map[string]*Stat
	total   Stat
	started time.Time
}

type View struct {
	UptimeSeconds float64         `json:"uptime_seconds"`
	Today         Stat            `json:"today"`
	Total         Stat            `json:"total"`
	ByDay         map[string]Stat `json:"by_day"`
	ByHour        map[string]Stat `json:"by_hour"`
	ByModel       map[string]Stat `json:"by_model"`
	ByProvider    map[string]Stat `json:"by_provider"`
	Recent        []Record        `json:"recent"`
}

func New(path string, ringSize int) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	if ringSize <= 0 {
		ringSize = 500
	}
	return &Store{
		f:       f,
		ringMax: ringSize,
		ring:    make([]Record, 0, ringSize),
		days:    map[string]*Stat{},
		hours:   map[string]*Stat{},
		models:  map[string]*Stat{},
		provs:   map[string]*Stat{},
		started: time.Now(),
	}, nil
}

func (s *Store) Add(r Record) {
	b, err := json.Marshal(r)
	if err == nil {
		b = append(b, '\n')
		s.mu.Lock()
		s.f.Write(b)
		s.mu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	day := r.Time.Format("2006-01-02")
	get := func(m map[string]*Stat, k string) *Stat {
		st, ok := m[k]
		if !ok {
			st = &Stat{}
			m[k] = st
		}
		return st
	}
	get(s.days, day).add(r)
	get(s.hours, r.Time.UTC().Format("2006-01-02T15")).add(r)
	if r.Model != "" {
		get(s.models, r.Model).add(r)
	}
	if r.Provider != "" {
		get(s.provs, r.Provider).add(r)
	}
	s.total.add(r)
	s.ring = append(s.ring, r)
	if len(s.ring) > s.ringMax {
		s.ring = s.ring[len(s.ring)-s.ringMax:]
	}
}

func copyStat(m map[string]*Stat) map[string]Stat {
	out := make(map[string]Stat, len(m))
	for k, v := range m {
		out[k] = *v
	}
	return out
}

func (s *Store) Snapshot() View {
	s.mu.Lock()
	defer s.mu.Unlock()
	recent := make([]Record, len(s.ring))
	copy(recent, s.ring)
	v := View{
		UptimeSeconds: time.Since(s.started).Seconds(),
		ByDay:         copyStat(s.days),
		ByHour:        copyStat(s.hours),
		ByModel:       copyStat(s.models),
		ByProvider:    copyStat(s.provs),
		Total:         s.total,
		Recent:        recent,
	}
	v.Today = v.ByDay[time.Now().Format("2006-01-02")]
	return v
}
