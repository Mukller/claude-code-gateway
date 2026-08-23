package server

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"

	"claude-code-gateway/internal/config"
	"claude-code-gateway/internal/core"
)

type guardrails struct {
	maxInput int64
	reqRe    []*regexp.Regexp
	denied   map[string]bool
	respRe   []*regexp.Regexp
}

func compileGuardrails(cfg config.Guardrails) *guardrails {
	g := &guardrails{maxInput: cfg.Request.MaxInputTokens, denied: map[string]bool{}}
	for _, p := range cfg.Request.BlockedPatterns {
		if re, err := regexp.Compile(p); err == nil {
			g.reqRe = append(g.reqRe, re)
		} else {
			log.Printf("[guardrails] bad request pattern %q: %v", p, err)
		}
	}
	for _, t := range cfg.Request.DeniedTools {
		g.denied[t] = true
	}
	for _, p := range cfg.Response.BlockedPatterns {
		if re, err := regexp.Compile(p); err == nil {
			g.respRe = append(g.respRe, re)
		} else {
			log.Printf("[guardrails] bad response pattern %q: %v", p, err)
		}
	}
	return g
}

func (g *guardrails) checkRequest(mreq *core.MessagesRequest, estTokens int64) (blocked string) {
	if g == nil {
		return ""
	}
	if g.maxInput > 0 && estTokens > g.maxInput {
		return fmt.Sprintf("input exceeds max_input_tokens (%d > %d)", estTokens, g.maxInput)
	}
	for _, t := range mreq.Tools {
		if g.denied[t.Name] {
			return fmt.Sprintf("tool %q is not allowed", t.Name)
		}
	}
	text := core.RequestFlatText(mreq)
	for _, re := range g.reqRe {
		if loc := re.FindStringIndex(text); loc != nil {
			return fmt.Sprintf("request matches blocked pattern %q", re.String())
		}
	}
	return ""
}

type respView struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func (g *guardrails) checkResponse(data []byte) (blocked string) {
	if g == nil || len(g.respRe) == 0 || len(data) == 0 {
		return ""
	}
	var v respView
	if json.Unmarshal(data, &v) != nil {
		return ""
	}
	for _, b := range v.Content {
		if b.Type != "text" || b.Text == "" {
			continue
		}
		for _, re := range g.respRe {
			if re.MatchString(b.Text) {
				return fmt.Sprintf("response matches blocked pattern %q", re.String())
			}
		}
	}
	return ""
}
