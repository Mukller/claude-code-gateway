package core

import (
	"encoding/json"
	"strings"
)

// OAIInRequest is the OpenAI ChatCompletion request format.
type OAIInRequest struct {
	Model       string          `json:"model"`
	Messages    []OAIInMessage  `json:"messages"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stop        []string        `json:"stop,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	Tools       []OAIInTool     `json:"tools,omitempty"`
	ToolChoice  json.RawMessage `json:"tool_choice,omitempty"`
	User        string          `json:"user,omitempty"`
}

type OAIInMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []OAIToolCall   `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type OAIInTool struct {
	Type     string        `json:"type"`
	Function OAIInToolFunc `json:"function"`
}

type OAIInToolFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ToAnthropic converts an OpenAI ChatCompletion request to Anthropic MessagesRequest.
func (o *OAIInRequest) ToAnthropic() *MessagesRequest {
	mr := &MessagesRequest{
		Model:         o.Model,
		MaxTokens:     o.MaxTokens,
		Stream:        o.Stream,
		Temperature:   o.Temperature,
		TopP:          o.TopP,
		StopSequences: o.Stop,
	}
	if mr.MaxTokens == 0 {
		mr.MaxTokens = 4096
	}

	for _, msg := range o.Messages {
		switch msg.Role {
		case "system":
			mr.System = appendRawText(mr.System, msg.Content)
		case "assistant":
			var blocks []Block
			var text string
			if err := json.Unmarshal(msg.Content, &text); err == nil {
				if text != "" {
					blocks = append(blocks, Block{Type: "text", Text: text})
				}
			} else {
				var bs []Block
				if json.Unmarshal(msg.Content, &bs) == nil {
					blocks = bs
				}
			}
			for _, tc := range msg.ToolCalls {
				blocks = append(blocks, Block{
					Type: "tool_use", ID: tc.ID, Name: tc.Function.Name,
					Input: json.RawMessage(tc.Function.Arguments),
				})
			}
			mr.Messages = append(mr.Messages, ApiMessage{Role: "assistant", Content: marshalBlocks(blocks)})
		case "tool":
			blocks := []Block{{
				Type: "tool_result", ToolUseID: msg.ToolCallID,
				Content: msg.Content,
			}}
			mr.Messages = append(mr.Messages, ApiMessage{Role: "user", Content: marshalBlocks(blocks)})
		default: // user
			var blocks []Block
			var text string
			if err := json.Unmarshal(msg.Content, &text); err == nil {
				blocks = append(blocks, Block{Type: "text", Text: text})
			} else {
				var parts []ContentPart
				if err := json.Unmarshal(msg.Content, &parts); err == nil {
					for _, p := range parts {
						switch p.Type {
						case "text":
							blocks = append(blocks, Block{Type: "text", Text: p.Text})
						case "image_url":
							if p.ImageURL != nil {
								if data, mt, ok := parseDataURL(p.ImageURL.URL); ok {
									blocks = append(blocks, Block{Type: "image", Source: &ImageSource{
										Type: "base64", MediaType: mt, Data: data,
									}})
								} else {
									blocks = append(blocks, Block{Type: "image", Source: &ImageSource{Type: "url", URL: p.ImageURL.URL}})
								}
							}
						}
					}
				} else {
					var raw json.RawMessage
					json.Unmarshal(msg.Content, &raw)
					blocks = append(blocks, Block{Type: "text", Text: string(raw)})
				}
			}
			mr.Messages = append(mr.Messages, ApiMessage{Role: "user", Content: marshalBlocks(blocks)})
		}
	}

	for _, t := range o.Tools {
		mr.Tools = append(mr.Tools, ToolDef{
			Name: t.Function.Name, Description: t.Function.Description,
			InputSchema: t.Function.Parameters,
		})
	}
	if len(o.ToolChoice) > 0 {
		mr.ToolChoice = o.ToolChoice
	}
	return mr
}

func appendRawText(existing json.RawMessage, content json.RawMessage) json.RawMessage {
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		if existing != nil {
			var old string
			json.Unmarshal(existing, &old)
			b, _ := json.Marshal(old + "\n" + s)
			return b
		}
		b, _ := json.Marshal(s)
		return b
	}
	if existing != nil {
		return existing
	}
	return content
}

func marshalBlocks(blocks []Block) json.RawMessage {
	if len(blocks) == 0 {
		return json.RawMessage(`[]`)
	}
	b, _ := json.Marshal(blocks)
	return b
}

func jsonMarshalStrings(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func parseDataURL(url string) (data, mediaType string, ok bool) {
	const prefix = "data:"
	if !strings.HasPrefix(url, prefix) {
		return "", "", false
	}
	rest := url[len(prefix):]
	semi := strings.Index(rest, ";")
	if semi < 0 {
		return "", "", false
	}
	mediaType = rest[:semi]
	rest = rest[semi+1:]
	if !strings.HasPrefix(rest, "base64,") {
		return "", "", false
	}
	return rest[len("base64,"):], mediaType, true
}
