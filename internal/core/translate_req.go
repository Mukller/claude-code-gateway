package core

import (
	"encoding/json"
	"fmt"
	"strings"
)

func BlocksFromRaw(raw json.RawMessage) ([]Block, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil, nil
		}
		return []Block{{Type: "text", Text: s}}, nil
	}
	var bs []Block
	if err := json.Unmarshal(raw, &bs); err != nil {
		return nil, fmt.Errorf("decode content: %w", err)
	}
	return bs, nil
}

func SystemText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	bs, err := BlocksFromRaw(raw)
	if err != nil {
		return ""
	}
	var out []string
	for _, b := range bs {
		if b.Type == "text" && b.Text != "" {
			out = append(out, b.Text)
		}
	}
	return strings.Join(out, "\n\n")
}

func FlattenContent(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	bs, err := BlocksFromRaw(raw)
	if err != nil {
		return ""
	}
	var out []string
	for _, b := range bs {
		switch b.Type {
		case "text":
			if b.Text != "" {
				out = append(out, b.Text)
			}
		default:
			out = append(out, "["+b.Type+"]")
		}
	}
	return strings.Join(out, "\n")
}

func TranslateRequest(m *MessagesRequest, targetModel string) (*ChatRequest, error) {
	out := &ChatRequest{
		Model:       targetModel,
		Messages:    []ChatMessage{},
		MaxTokens:   m.MaxTokens,
		Temperature: m.Temperature,
		TopP:        m.TopP,
		Stream:      m.Stream,
		Stop:        m.StopSequences,
	}
	if sys := SystemText(m.System); sys != "" {
		out.Messages = append(out.Messages, ChatMessage{Role: "system", Content: sys})
	}
	for _, am := range m.Messages {
		bs, err := BlocksFromRaw(am.Content)
		if err != nil {
			return nil, fmt.Errorf("message %q: %w", am.Role, err)
		}
		switch am.Role {
		case "assistant":
			var texts []string
			var tcs []OAIToolCall
			for _, b := range bs {
				switch b.Type {
				case "text":
					if b.Text != "" {
						texts = append(texts, b.Text)
					}
				case "tool_use":
					args := string(b.Input)
					if args == "" || args == "null" {
						args = "{}"
					}
					tcs = append(tcs, OAIToolCall{
						ID:       FirstNonEmpty(b.ID, "call_"+RandHex(8)),
						Type:     "function",
						Function: OAIFunc{Name: b.Name, Arguments: args},
					})
				}
			}
			cm := ChatMessage{Role: "assistant"}
			if len(texts) > 0 {
				cm.Content = strings.Join(texts, "\n\n")
			}
			if len(tcs) > 0 {
				cm.ToolCalls = tcs
			}
			if cm.Content != nil || len(cm.ToolCalls) > 0 {
				out.Messages = append(out.Messages, cm)
			}
		default:
			var parts []ContentPart
			flush := func() {
				if len(parts) == 0 {
					return
				}
				p := parts
				parts = nil
				var content interface{}
				if len(p) == 1 && p[0].Type == "text" && p[0].ImageURL == nil {
					content = p[0].Text
				} else {
					content = p
				}
				out.Messages = append(out.Messages, ChatMessage{Role: "user", Content: content})
			}
			for _, b := range bs {
				switch b.Type {
				case "text":
					if b.Text != "" {
						parts = append(parts, ContentPart{Type: "text", Text: b.Text})
					}
				case "image":
					if b.Source != nil {
						u := b.Source.URL
						if u == "" && b.Source.Data != "" {
							u = "data:" + b.Source.MediaType + ";base64," + b.Source.Data
						}
						if u != "" {
							parts = append(parts, ContentPart{Type: "image_url", ImageURL: &ImgURL{URL: u}})
						}
					}
				case "tool_result":
					flush()
					out.Messages = append(out.Messages, ChatMessage{
						Role:       "tool",
						ToolCallID: b.ToolUseID,
						Content:    FlattenContent(b.Content),
					})
				}
			}
			flush()
		}
	}
	for _, t := range m.Tools {
		td := OAIToolDef{Type: "function"}
		td.Function.Name = t.Name
		td.Function.Description = t.Description
		if len(t.InputSchema) == 0 || string(t.InputSchema) == "null" {
			td.Function.Parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		} else {
			td.Function.Parameters = t.InputSchema
		}
		out.Tools = append(out.Tools, td)
	}
	if len(m.ToolChoice) > 0 && string(m.ToolChoice) != "null" {
		var tc struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if json.Unmarshal(m.ToolChoice, &tc) == nil && tc.Type != "" {
			switch tc.Type {
			case "auto":
				out.ToolChoice = "auto"
			case "any":
				out.ToolChoice = "required"
			case "tool":
				out.ToolChoice = map[string]any{
					"type":     "function",
					"function": map[string]string{"name": tc.Name},
				}
			}
		}
	}
	return out, nil
}

func RequestTraits(m *MessagesRequest) (estTokens int64, hasImage, thinking bool) {
	estTokens = EstimateRequestTokens(m)
	for _, am := range m.Messages {
		bs, _ := BlocksFromRaw(am.Content)
		for _, b := range bs {
			if b.Type == "image" && b.Source != nil {
				hasImage = true
			}
		}
	}
	if len(m.Thinking) > 0 {
		var th struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Thinking, &th) == nil {
			switch th.Type {
			case "enabled", "adaptive":
				thinking = true
			}
		} else {
			thinking = true
		}
	}
	return estTokens, hasImage, thinking
}

func EstimateRequestTokens(m *MessagesRequest) int64 {
	var sb strings.Builder
	sb.WriteString(SystemText(m.System))
	sb.WriteString(" ")
	for _, am := range m.Messages {
		sb.WriteString(am.Role)
		sb.WriteString(" ")
		bs, _ := BlocksFromRaw(am.Content)
		for _, b := range bs {
			switch b.Type {
			case "text":
				sb.WriteString(b.Text)
			case "tool_use":
				sb.WriteString(b.Name)
				sb.Write(b.Input)
			case "tool_result":
				sb.WriteString(FlattenContent(b.Content))
			case "image":
				sb.WriteString(strings.Repeat("x", 4000))
			}
			sb.WriteString(" ")
		}
	}
	for _, t := range m.Tools {
		sb.WriteString(t.Name)
		sb.WriteString(t.Description)
		sb.Write(t.InputSchema)
	}
	tokens := int64(sb.Len())/4 + 3
	tokens += int64(len(m.Messages))*4 + int64(len(m.Tools))*40
	return tokens
}
