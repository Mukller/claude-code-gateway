package state

import (
	"database/sql"
	"fmt"
	"strconv"
	"sync"
	"time"

	_ "github.com/lib/pq"
)

type PGStore struct {
	db     *sql.DB
	mu     sync.Mutex
	prefix string
	up     bool
}

func NewPostgres(dsn, prefix string) (*PGStore, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	p := &PGStore{db: db, prefix: prefix + ":", up: false}
	if err := p.pingWithRetry(3); err != nil {
		return nil, err
	}
	schema := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %scounters (
	key TEXT PRIMARY KEY,
	num_val DOUBLE PRECISION NOT NULL DEFAULT 0,
	str_val TEXT,
	expires_at TIMESTAMPTZ
)`, p.prefix)
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return p, nil
}

func (p *PGStore) pingWithRetry(n int) error {
	var last error
	for i := 0; i < n; i++ {
		if err := p.db.Ping(); err == nil {
			p.up = true
			return nil
		} else {
			last = err
			time.Sleep(time.Duration(i+1) * time.Second)
		}
	}
	return last
}

func (p *PGStore) k(key string) string { return p.prefix + key }

func (p *PGStore) Healthy() bool { return p.up && p.db.Ping() == nil }

const upsertNum = `
INSERT INTO %scounters (key, num_val) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET
	num_val = CASE WHEN %scounters.expires_at IS NULL OR %scounters.expires_at > now()
		THEN %scounters.num_val + $2 ELSE $2 END,
	expires_at = CASE WHEN $3::BIGINT > 0 THEN now() + make_interval(secs => $3::FLOAT8 / 1000.0) ELSE NULL END
RETURNING num_val`

func (p *PGStore) IncrWindow(key string, ttl time.Duration, delta int64) (int64, error) {
	q := fmt.Sprintf(upsertNum, p.prefix, p.prefix, p.prefix, p.prefix)
	res, err := p.execFloat(q, p.k(key), float64(delta), ttl.Milliseconds())
	if err != nil {
		return 0, err
	}
	n, _ := strconv.ParseInt(res, 10, 64)
	return n, nil
}

func (p *PGStore) GetCounter(key string) (int64, error) {
	f, err := p.GetFloat(key)
	if err != nil {
		return 0, err
	}
	return int64(f), nil
}

func (p *PGStore) IncrFloat(key string, delta float64) (float64, error) {
	q := fmt.Sprintf(upsertNum, p.prefix, p.prefix, p.prefix, p.prefix)
	res, err := p.execFloat(q, p.k(key), delta, 0)
	if err != nil {
		return 0, err
	}
	f, _ := strconv.ParseFloat(res, 64)
	return f, nil
}

func (p *PGStore) GetFloat(key string) (float64, error) {
	row := p.db.QueryRow(fmt.Sprintf(
		`SELECT num_val FROM %scounters WHERE key=$1 AND (expires_at IS NULL OR expires_at > now())`, p.prefix), p.k(key))
	var f float64
	if err := row.Scan(&f); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	return f, nil
}

func (p *PGStore) SetTTLBytes(key string, val []byte, ttl time.Duration) error {
	var exp any
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	_, err := p.db.Exec(fmt.Sprintf(`
INSERT INTO %scounters (key, num_val, str_val, expires_at) VALUES ($1, 0, $2, $3)
ON CONFLICT (key) DO UPDATE SET str_val=EXCLUDED.str_val, expires_at=EXCLUDED.expires_at`, p.prefix),
		p.k(key), string(val), exp)
	return err
}

func (p *PGStore) GetBytes(key string) ([]byte, bool, error) {
	row := p.db.QueryRow(fmt.Sprintf(
		`SELECT str_val FROM %scounters WHERE key=$1 AND (expires_at IS NULL OR expires_at > now())`, p.prefix), p.k(key))
	var s sql.NullString
	if err := row.Scan(&s); err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !s.Valid {
		return nil, false, nil
	}
	return []byte(s.String), true, nil
}

func (p *PGStore) execFloat(q string, args ...any) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	row := p.db.QueryRow(q, args...)
	var res float64
	if err := row.Scan(&res); err != nil {
		return "", err
	}
	return strconv.FormatFloat(res, 'f', -1, 64), nil
}
