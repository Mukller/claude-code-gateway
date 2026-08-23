package server

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"

	"claude-code-gateway/internal/config"
	"claude-code-gateway/internal/core"
)

type guardrails struct {
	maxInput int64
	reqRe    []*regexp.Regexp
	denied   map[string]bool
	respRe   []*regexp.Regexp
	piiRe    []*regexp.Regexp
	injectRe []*regexp.Regexp
	injMode  string // off | flag | block
}

var piiPresets = map[string]string{
	"email": `[\w.+-]+@[\w-]+\.[\w.-]+`,
	"phone": `(?:\+7|8)[\s\-]?\(?\d{3}\)?[\s\-]?\d{3}[\s\-]?\d{2}[\s\-]?\d{2}`,
	"card":  `\b(?:\d[ -]?){13,16}\b`,
}

var injectionPatterns = []string{
	`(?i)ignore\s+(all\s+)?(previous|prior|above|earlier)\s+instructions`,
	`(?i)disregard\s+(all\s+)?(previous|prior|your)\s+(instructions|rules|prompt)`,
	`(?i)reveal\s+(your\s+)?(system\s+)?(prompt|instructions)`,
	`(?i)you\s+are\s+now\s+(a|an|no longer)`,
	`(?i)<\|im_start\|>`,
	`(?i)\bsystem\s*:\s*you\s+must\b`,
}

func compileGuardrails(cfg config.Guardrails) *guardrails {
	g := &guardrails{
		maxInput: cfg.Request.MaxInputTokens,
		denied:   map[string]bool{},
		injMode:  cfg.Request.InjectionDetection,
	}
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
	for _, preset := range cfg.Request.PIIPresets {
		pat, ok := piiPresets[strings.ToLower(preset)]
		if !ok {
			log.Printf("[guardrails] unknown pii preset %q (have: email, phone, card)", preset)
			continue
		}
		if re, err := regexp.Compile(pat); err == nil {
			g.piiRe = append(g.piiRe, re)
		}
	}
	if g.injMode == "" {
		g.injMode = "off"
	}
	if g.injMode != "off" {
		for _, p := range injectionPatterns {
			if re, err := regexp.Compile(p); err == nil {
				g.injectRe = append(g.injectRe, re)
			}
		}
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

func (g *guardrails) streamScanEnabled() bool {
	return g != nil && len(g.respRe) > 0
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
	if g.injMode != "off" {
		for _, re := range g.injectRe {
			if re.MatchString(text) {
				msg := fmt.Sprintf("prompt injection detected (%q)", re.String())
				if g.injMode == "block" {
					return msg
				}
				log.Printf("[guardrails] FLAG: %s", msg)
				break
			}
		}
	}
	return ""
}

func (g *guardrails) redactPII(mreq *core.MessagesRequest) {
	if g == nil || len(g.piiRe) == 0 {
		return
	}
	core.RedactRequestText(mreq, func(s string) string {
		for _, re := range g.piiRe {
			s = re.ReplaceAllString(s, "[REDACTED]")
		}
		return s
	})
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
