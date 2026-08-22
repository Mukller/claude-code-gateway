package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"claude-code-gateway/internal/core"
	"claude-code-gateway/internal/logstore"
	"claude-code-gateway/internal/pricing"
	"claude-code-gateway/internal/provider"
)

const maxBodyBytes = 64 << 20

type FailKind int

const (
	failFatal FailKind = iota
	failSoft
	failHard
)

func (k FailKind) retryable() bool { return k != failFatal }

func classifyStatus(status int, soft, hard []int) FailKind {
	for _, c := range hard {
		if status == c {
			return failHard
		}
	}
	for _, c := range soft {
		if status == c {
			return failSoft
		}
	}
	return failFatal
}

func buildPayload(ptype, targetModel string, body []byte, mreq *core.MessagesRequest) ([]byte, string, error) {
	switch ptype {
	case "anthropic", "anthropic-compat":
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, "", err
		}
		raw["model"], _ = json.Marshal(targetModel)
		out, err := json.Marshal(raw)
		return out, "/v1/messages", err
	default:
		cr, err := core.TranslateRequest(mreq, targetModel)
		if err != nil {
			return nil, "", err
		}
		out, err := json.Marshal(cr)
		return out, "/v1/chat/completions", err
	}
}

func isSSEContentType(h string) bool {
	return strings.Contains(strings.ToLower(h), "text/event-stream")
}

func (s *Server) costFor(model string, in, out, cr, cw int64) float64 {
	p, ok := s.prices.Match(model)
	if !ok {
		p2, ok2 := s.prices.Match("default")
		if !ok2 {
			return 0
		}
		p = p2
	}
	return pricing.Cost(p, in, out, cr, cw)
}

func newFlusher(w http.ResponseWriter) func() {
	if f, ok := w.(http.Flusher); ok {
		return f.Flush
	}
	return func() {}
}

func startSSEHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

func writeJSONMessage(w http.ResponseWriter, mr *core.MessageResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(mr)
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "use POST")
		return
	}
	token, ok := s.checkAuth(r)
	if !ok {
		writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "invalid or missing API token")
		return
	}
	if !s.limiter.Allow(time.Now()) {
		writeAnthropicError(w, http.StatusTooManyRequests, "rate_limit_error", "rate limit exceeded")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	var mreq core.MessagesRequest
	if jsonErr := json.Unmarshal(body, &mreq); jsonErr != nil || mreq.Model == "" {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "body must be Anthropic Messages JSON with a model field")
		return
	}
	stream := mreq.Stream

	targets, _ := s.reg.Resolve(mreq.Model)
	attemptsLeft := s.cfg.Retry.MaxAttempts
	lastProv, lastMsg := "", ""
	lastStatus := 0

	for _, tg := range targets {
		p := s.reg.Provider(tg.Name)
		if p == nil || p.Pool().Len() == 0 {
			continue
		}
		payload, upath, perr := buildPayload(p.Type(), tg.Model, body, &mreq)
		if perr != nil {
			writeAnthropicError(w, http.StatusInternalServerError, "api_error", "build upstream request: "+perr.Error())
			return
		}

		triedKeys := map[string]bool{}
		for attemptsLeft > 0 && len(triedKeys) < p.Pool().Len() {
			attemptsLeft--
			key, kok := p.Pool().Pick()
			if !kok {
				break
			}
			if triedKeys[key] {
				continue
			}
			triedKeys[key] = true

			rec := logstore.Record{
				Time:        time.Now(),
				Token:       maskToken(token),
				Model:       mreq.Model,
				TargetModel: tg.Model,
				Provider:    p.Name(),
				Stream:      stream,
			}
			started := time.Now()

			resp, uerr := p.Execute(r.Context(), key, upath, payload, stream)
			if uerr != nil {
				p.Pool().Report(key, provider.SoftFail)
				rec.Error = core.TrimString(uerr.Error(), 400)
				s.store.Add(rec)
				lastProv, lastMsg, lastStatus = p.Name(), rec.Error, 0
				continue
			}

			if resp.Status >= 400 {
				data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
				resp.Body.Close()
				kind := classifyStatus(resp.Status, s.cfg.Retry.RetryStatuses, s.cfg.Retry.KeyFailStatus)
				switch kind {
				case failHard:
					p.Pool().Report(key, provider.HardFail)
				case failSoft:
					p.Pool().Report(key, provider.SoftFail)
				}
				rec.Status = resp.Status
				rec.Error = core.TrimString(FirstNonEmptyStr(resp.ErrMsg, string(data)), 400)
				s.store.Add(rec)
				lastProv, lastMsg, lastStatus = p.Name(), rec.Error, resp.Status
				if !kind.retryable() {
					passThroughUpstreamError(w, resp.Status, data)
					return
				}
				continue
			}

			p.Pool().Report(key, provider.Success)
			rec.Status = resp.Status

			var u core.Usage
			var serr error
			flush := newFlusher(w)

			if stream {
				startSSEHeaders(w)
				flush()
				ct := resp.Header.Get("Content-Type")
				switch {
				case p.Type() == "openai" && isSSEContentType(ct):
					u, serr = core.TranslateOpenAIStream(w, flush, resp.Body, tg.Model)
				case p.Type() == "openai":
					var mr *core.MessageResponse
					mr, serr = core.CollectOpenAIStream(resp.Body)
					if serr == nil {
						core.WriteSyntheticStream(w, flush, mr)
						u = mr.Usage
					}
				case isSSEContentType(ct):
					u, serr = core.PassthroughAnthropicStream(w, flush, resp.Body)
				default:
					var mr core.MessageResponse
					data, rerr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
					if rerr != nil {
						serr = rerr
					} else if jerr := json.Unmarshal(data, &mr); jerr != nil {
						serr = jerr
					} else {
						core.WriteSyntheticStream(w, flush, &mr)
						u = mr.Usage
					}
				}
				resp.Body.Close()
			} else {
				ct := resp.Header.Get("Content-Type")
				switch {
				case p.Type() == "openai" && isSSEContentType(ct):
					var mr *core.MessageResponse
					mr, serr = core.CollectOpenAIStream(resp.Body)
					if serr == nil {
						writeJSONMessage(w, mr)
						u = mr.Usage
					}
				case p.Type() == "openai":
					var data []byte
					data, serr = readAllLimited(resp.Body)
					if serr == nil {
						var cr core.ChatResponse
						if jerr := json.Unmarshal(data, &cr); jerr != nil {
							serr = jerr
						} else {
							mr := core.TranslateResponse(&cr, tg.Model)
							writeJSONMessage(w, mr)
							u.InputTokens = mr.Usage.InputTokens
							u.OutputTokens = mr.Usage.OutputTokens
						}
					}
				case isSSEContentType(ct):
					var mr *core.MessageResponse
					mr, serr = core.CollectAnthropicStream(resp.Body)
					if serr == nil {
						writeJSONMessage(w, mr)
						u = mr.Usage
					}
				default:
					var data []byte
					data, serr = readAllLimited(resp.Body)
					if serr == nil {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(resp.Status)
						w.Write(data)
						u = core.ExtractAnthropicUsage(data)
					}
				}
				resp.Body.Close()
			}

			rec.LatencyMs = time.Since(started).Milliseconds()
			if serr != nil {
				rec.Error = core.TrimString(serr.Error(), 300)
				s.store.Add(rec)
				lastProv, lastMsg, lastStatus = p.Name(), rec.Error, resp.Status
				continue
			}

			rec.InTok = u.InputTokens
			rec.OutTok = u.OutputTokens
			rec.CacheRead = u.CacheReadInputTokens
			rec.CacheWrite = u.CacheCreationInputTokens
			if rec.InTok == 0 {
				rec.InTok = core.EstimateRequestTokens(&mreq)
			}
			rec.CostUSD = s.costFor(tg.Model, rec.InTok, rec.OutTok, rec.CacheRead, rec.CacheWrite)
			s.store.Add(rec)
			return
		}
	}

	msg := fmt.Sprintf("all providers exhausted (last: %s)", FirstNonEmptyStr(lastProv, "none"))
	if lastMsg != "" {
		msg += ": " + lastMsg
	}
	status := http.StatusBadGateway
	if lastStatus == 429 || lastStatus == 401 || lastStatus == 403 {
		status = lastStatus
	} else if lastStatus >= 500 {
		status = http.StatusServiceUnavailable
	}
	writeAnthropicError(w, status, "overloaded_error", msg)
	s.store.Add(logstore.Record{
		Time: time.Now(), Token: maskToken(token), Model: mreq.Model,
		Status: status, Error: core.TrimString(msg, 400),
	})
}

const maxResponseBytes = 128 << 20

func readAllLimited(body io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(body, maxResponseBytes))
}

func FirstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

var typeMap = map[string]string{
	"rate_limit_error":      "rate_limit_error",
	"authentication_error":  "authentication_error",
	"permission_error":      "permission_error",
	"not_found_error":       "not_found_error",
	"request_too_large":     "request_too_large",
	"invalid_request_error": "invalid_request_error",
}

func passThroughUpstreamError(w http.ResponseWriter, status int, data []byte) {
	var ae core.ErrorResp
	if json.Unmarshal(data, &ae) == nil && ae.Type == "error" && ae.Error.Message != "" {
		writeJSON(w, status, ae)
		return
	}
	var oe core.OAIError
	if json.Unmarshal(data, &oe) == nil && oe.Error.Message != "" {
		t := "api_error"
		if mapped, ok := typeMap[oe.Error.Type]; ok {
			t = mapped
		}
		writeAnthropicError(w, status, t, oe.Error.Message)
		return
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		raw = fmt.Sprintf("upstream returned %d", status)
	}
	if len(raw) > 500 {
		raw = raw[:500]
	}
	writeAnthropicError(w, status, "api_error", raw)
}

func (s *Server) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkAuth(r); !ok {
		writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "invalid or missing API token")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	var mreq core.MessagesRequest
	if json.Unmarshal(body, &mreq) != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "bad JSON body")
		return
	}
	n := core.EstimateRequestTokens(&mreq)
	writeJSON(w, http.StatusOK, map[string]int64{"input_tokens": n})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkAuth(r); !ok {
		writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "invalid or missing API token")
		return
	}
	catalog := s.reg.Catalog()
	type m struct {
		Type        string `json:"type"`
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		CreatedAt   string `json:"created_at"`
	}
	data := make([]m, 0, len(catalog))
	now := time.Now().UTC().Format(time.RFC3339)
	for _, e := range catalog {
		data = append(data, m{Type: "model", ID: e.ID, DisplayName: e.ID, CreatedAt: now})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "has_more": false})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	provs := []map[string]any{}
	for _, p := range s.reg.Providers() {
		provs = append(provs, map[string]any{
			"name": p.Name(),
			"type": p.Type(),
			"keys": p.Pool().Len(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"uptime_s":  int64(time.Since(s.started).Seconds()),
		"providers": provs,
		"models":    len(s.reg.Catalog()),
	})
}

func (s *Server) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Snapshot())
}

func (s *Server) handleAdminLogs(w http.ResponseWriter, r *http.Request) {
	v := s.store.Snapshot()
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	if limit > 0 && limit < len(v.Recent) {
		v.Recent = v.Recent[len(v.Recent)-limit:]
	}
	writeJSON(w, http.StatusOK, v.Recent)
}
