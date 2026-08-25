package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"claude-code-gateway/internal/core"
	"claude-code-gateway/internal/logstore"
	"claude-code-gateway/internal/provider"
)

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "use POST")
		return
	}
	token, ok := s.checkAuth(r)
	if !ok {
		writeOpenAIError(w, http.StatusUnauthorized, "invalid_api_key", "invalid or missing API token")
		return
	}
	if !s.limiter.Allow(token, time.Now()) {
		writeOpenAIError(w, http.StatusTooManyRequests, "rate_limit_exceeded", "rate limit exceeded")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	var oreq core.OAIInRequest
	if json.Unmarshal(body, &oreq) != nil || oreq.Model == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "body must be OpenAI ChatCompletion JSON with a model field")
		return
	}

	mreq := oreq.ToAnthropic()
	reqID := core.RandHex(8)
	w.Header().Set("X-Ccg-Request-Id", reqID)

	if blockedMsg := s.rails.checkRequest(mreq, core.EstimateRequestTokens(mreq)); blockedMsg != "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "blocked by guardrail: "+blockedMsg)
		return
	}

	est, hasImage, thinking := core.RequestTraits(mreq)
	if !s.budgets.allowsModel(token, mreq.Model) {
		writeOpenAIError(w, http.StatusForbidden, "model_not_allowed", fmt.Sprintf("model %q is not allowed", mreq.Model))
		return
	}
	ci, _, _ := s.budgets.exceeded(token, time.Now())
	if !s.limiter.AllowTokens(token, est, ci.TPM, time.Now()) {
		writeOpenAIError(w, http.StatusTooManyRequests, "rate_limit_exceeded", "TPM exceeded")
		return
	}

	targets, _ := s.reg.Resolve(mreq.Model, provider.ResolveInfo{EstTokens: est, HasImage: hasImage, Thinking: thinking})
	attemptsLeft := s.cfg.Retry.MaxAttempts
	lastProv, lastMsg := "", ""

	for _, tg := range targets {
		p := s.reg.Provider(tg.Name)
		if p == nil || p.Pool().Len() == 0 {
			continue
		}
		payload, upath, perr := buildPayload(p, tg.Model, body, mreq)
		if perr != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "api_error", perr.Error())
			return
		}

		triedKeys := map[string]bool{}
		for attemptsLeft > 0 && len(triedKeys) < p.Pool().Len() {
			attemptsLeft--
			key, kok := p.Pool().Pick()
			if !kok || triedKeys[key] {
				continue
			}
			triedKeys[key] = true
			p.BeginRequest()
			rec := logstore.Record{
				Time: time.Now(), Token: s.tokenLabel(token), RequestID: reqID,
				Model: oreq.Model, TargetModel: tg.Model, Provider: p.Name(), Stream: oreq.Stream,
			}
			started := time.Now()

			resp, uerr := p.Execute(r.Context(), key, upath, payload, oreq.Stream)
			if uerr != nil {
				p.EndRequest(time.Since(started).Milliseconds())
				p.Pool().Report(key, provider.SoftFail)
				p.NoteUpstream(false)
				rec.Error = core.TrimString(uerr.Error(), 400)
				s.logRecord(rec)
				lastProv, lastMsg = p.Name(), rec.Error
				continue
			}

			if resp.Status >= 400 {
				data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
				resp.Body.Close()
				p.EndRequest(time.Since(started).Milliseconds())
				kind := classifyStatus(resp.Status, s.cfg.Retry.RetryStatuses, s.cfg.Retry.KeyFailStatus)
				if kind == failHard {
					p.Pool().Report(key, provider.HardFail)
					p.NoteUpstream(false)
				} else if kind == failSoft {
					p.Pool().ReportHint(key, provider.SoftFail, parseRetryAfter(resp.Header.Get("Retry-After")))
					p.NoteUpstream(false)
				}
				rec.Status = resp.Status
				rec.Error = core.TrimString(FirstNonEmptyStr(resp.ErrMsg, string(data)), 400)
				s.logRecord(rec)
				lastProv, lastMsg = p.Name(), rec.Error
				if !kind.retryable() {
					writeOpenAIError(w, resp.Status, "api_error", rec.Error)
					return
				}
				continue
			}

			p.EndRequest(time.Since(started).Milliseconds())
			p.Pool().Report(key, provider.Success)
			rec.Status = resp.Status

			if oreq.Stream {
				s.streamOpenAI(w, p.Type(), resp, tg.Model, oreq.Model, rec, started, token)
				return
			}

			data, rerr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
			resp.Body.Close()
			if rerr != nil {
				rec.Error = rerr.Error()
				s.logRecord(rec)
				continue
			}
			rec.LatencyMs = time.Since(started).Milliseconds()

			var mr *core.MessageResponse
			ct := resp.Header.Get("Content-Type")
			switch {
			case isSSEContentType(ct):
				mr, rerr = core.CollectOpenAIStream(resp.Body)
				if rerr != nil && mr == nil {
					rec.Error = rerr.Error()
					s.logRecord(rec)
					lastProv, lastMsg = p.Name(), rec.Error
					continue
				}
			case p.Type() == "openai":
				var cr core.ChatResponse
				if jerr := json.Unmarshal(data, &cr); jerr != nil {
					rec.Error = jerr.Error()
					s.logRecord(rec)
					continue
				}
				mr = core.TranslateResponse(&cr, tg.Model)
			default:
				var mrA core.MessageResponse
				if jerr := json.Unmarshal(data, &mrA); jerr != nil {
					rec.Error = jerr.Error()
					s.logRecord(rec)
					continue
				}
				mr = &mrA
			}
			resp.Body.Close()

			oaiResp := core.AnthropicToOpenAIResponse(mr, oreq.Model)
			rec.InTok = mr.Usage.InputTokens
			rec.OutTok = mr.Usage.OutputTokens
			rec.CostUSD = s.costFor(tg.Model, rec.InTok, rec.OutTok, mr.Usage.CacheReadInputTokens, mr.Usage.CacheCreationInputTokens)
			s.budgets.add(token, rec.CostUSD, time.Now())
			s.logRecord(rec)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(oaiResp)
			return
		}
	}

	writeOpenAIError(w, http.StatusBadGateway, "api_error",
		fmt.Sprintf("all providers exhausted (last: %s: %s)", lastProv, lastMsg))
}

func (s *Server) streamOpenAI(w http.ResponseWriter, ptype string, resp *provider.ExecResult, targetModel, requestModel string, rec logstore.Record, started time.Time, token string) {
	flush := newFlusher(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flush()

	var mr *core.MessageResponse
	var serr error
	ct := resp.Header.Get("Content-Type")
	switch {
	case isSSEContentType(ct) && ptype != "openai":
		mr, serr = core.CollectAnthropicStream(resp.Body)
	default:
		mr, serr = core.CollectOpenAIStream(resp.Body)
	}
	resp.Body.Close()
	if serr != nil {
		fmt.Fprintf(w, "data: {\"error\": {\"message\": %q}}\n\n", serr.Error())
		flush()
		return
	}

	rec.LatencyMs = time.Since(started).Milliseconds()
	rec.InTok = mr.Usage.InputTokens
	rec.OutTok = mr.Usage.OutputTokens
	rec.CostUSD = s.costFor(targetModel, rec.InTok, rec.OutTok, 0, 0)
	s.budgets.add(token, rec.CostUSD, time.Now())
	s.logRecord(rec)

	oaiID := fmt.Sprintf("chatcmpl-%s", core.RandHex(12))
	first := true
	for _, b := range mr.Content {
		switch b.Type {
		case "text":
			chunk := map[string]any{
				"id": oaiID, "object": "chat.completion.chunk", "model": targetModel,
				"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": b.Text}}},
			}
			if first {
				chunk["choices"].([]map[string]any)[0]["delta"] = map[string]any{"role": "assistant", "content": b.Text}
				first = false
			}
			writeSSEChunk(w, flush, chunk)
		case "tool_use":
			writeSSEChunk(w, flush, map[string]any{
				"id": oaiID, "object": "chat.completion.chunk", "model": targetModel,
				"choices": []map[string]any{{"index": 0, "delta": map[string]any{
					"tool_calls": []map[string]any{{"index": 0, "id": b.ID, "type": "function",
						"function": map[string]any{"name": b.Name, "arguments": string(b.Input)}}},
				}}},
			})
		}
	}
	writeSSEChunk(w, flush, map[string]any{
		"id": oaiID, "object": "chat.completion.chunk", "model": targetModel,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": mapStopReason(mr.StopReason)}},
	})
	fmt.Fprint(w, "data: [DONE]\n\n")
	flush()
}

func mapStopReason(anthropicReason string) string {
	switch anthropicReason {
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	default:
		return "stop"
	}
}

func writeSSEChunk(w http.ResponseWriter, flush func(), chunk any) {
	b, _ := json.Marshal(chunk)
	fmt.Fprintf(w, "data: %s\n\n", b)
	flush()
}

func writeOpenAIError(w http.ResponseWriter, status int, errType, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": errType},
	})
}
