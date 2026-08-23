package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"claude-code-gateway/internal/config"
)

type Embedder struct {
	endpoint string
	model    string
	key      string
	client   *http.Client

	mu   sync.Mutex
	vecs map[string][]float32
}

func newEmbedder(cfg config.SemanticCache) *Embedder {
	return &Embedder{
		endpoint: cfg.Endpoint,
		model:    cfg.Model,
		key:      cfg.Key,
		client:   &http.Client{Timeout: 30 * time.Second},
		vecs:     map[string][]float32{},
	}
}

func (e *Embedder) Embed(text string) ([]float32, error) {
	h := sha256.Sum256([]byte(text))
	k := hex.EncodeToString(h[:16])
	e.mu.Lock()
	if v, ok := e.vecs[k]; ok {
		e.mu.Unlock()
		return v, nil
	}
	e.mu.Unlock()

	payload, _ := json.Marshal(map[string]any{"model": e.model, "input": text})
	req, err := http.NewRequest(http.MethodPost, e.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.key != "" {
		req.Header.Set("Authorization", "Bearer "+e.key)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings: status %d", resp.StatusCode)
	}
	var er struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil || len(er.Data) == 0 {
		return nil, fmt.Errorf("embeddings: bad response")
	}
	sortByIndex(er.Data)
	vec := er.Data[0].Embedding
	if len(vec) == 0 {
		return nil, fmt.Errorf("embeddings: empty vector")
	}
	e.mu.Lock()
	e.vecs[k] = vec
	for len(e.vecs) > 4096 {
		for kk := range e.vecs {
			delete(e.vecs, kk)
			break
		}
	}
	e.mu.Unlock()
	return vec, nil
}

func sortByIndex(d []struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}) {
	for i := 1; i < len(d); i++ {
		for j := i; j > 0 && d[j-1].Index > d[j].Index; j-- {
			d[j-1], d[j] = d[j], d[j-1]
		}
	}
}
