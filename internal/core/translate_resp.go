package core

import (
	"encoding/json"
	"strings"
)

func StopFromFinish(f *string) string {
	if f == nil {
		return "end_turn"
	}
	switch *f {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	default:
		return "end_turn"
	}
}

func TranslateResponse(cr *ChatResponse, requestedModel string) *MessageResponse {
	mr := &MessageResponse{
		ID:      "msg_" + RandHex(12),
		Type:    "message",
		Role:    "assistant",
		Model:   FirstNonEmpty(cr.Model, requestedModel),
		Content: []Block{},
	}
	if cr != nil {
		if cr.Usage != nil {
			mr.Usage.InputTokens = cr.Usage.PromptTokens
			mr.Usage.OutputTokens = cr.Usage.CompletionTokens
		}
		if len(cr.Choices) > 0 {
			c := cr.Choices[0]
			if c.Message != nil {
				if s, ok := c.Message.Content.(string); ok && s != "" {
					mr.Content = append(mr.Content, Block{Type: "text", Text: s})
				} else if parts, ok := c.Message.Content.([]interface{}); ok {
					for _, p := range parts {
						if pm, ok := p.(map[string]interface{}); ok {
							if t, _ := pm["text"].(string); t != "" {
								mr.Content = append(mr.Content, Block{Type: "text", Text: t})
							}
						}
					}
				}
				for _, tc := range c.Message.ToolCalls {
					in := json.RawMessage(tc.Function.Arguments)
					if len(in) == 0 || string(in) == "null" {
						in = json.RawMessage("{}")
					}
					mr.Content = append(mr.Content, Block{
						Type:  "tool_use",
						ID:    FirstNonEmpty(tc.ID, "toolu_"+RandHex(10)),
						Name:  tc.Function.Name,
						Input: in,
					})
				}
			}
			mr.StopReason = StopFromFinish(c.FinishReason)
		}
	}
	if len(mr.Content) == 0 {
		mr.Content = append(mr.Content, Block{Type: "text", Text: ""})
	}
	if mr.StopReason == "" {
		mr.StopReason = "end_turn"
	}
	return mr
}

func EstimateTextTokens(s string) int64 {
	n := len(s)
	t := n / 4
	if n%4 > 0 {
		t++
	}
	return int64(t)
}

func TrimString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "..."
}
