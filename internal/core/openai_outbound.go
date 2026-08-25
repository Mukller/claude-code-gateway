package core

import "strings"

// AnthropicToOpenAIResponse converts an Anthropic MessageResponse to an OpenAI ChatCompletion response.
func AnthropicToOpenAIResponse(mr *MessageResponse, requestedModel string) map[string]any {
	var content string
	var toolCalls []map[string]any
	for _, b := range mr.Content {
		switch b.Type {
		case "text":
			if b.Text != "" {
				if content != "" {
					content += "\n"
				}
				content += b.Text
			}
		case "tool_use":
			args := string(b.Input)
			if args == "" || args == "null" {
				args = "{}"
			}
			toolCalls = append(toolCalls, map[string]any{
				"id": b.ID, "type": "function",
				"function": map[string]any{"name": b.Name, "arguments": args},
			})
		}
	}

	msg := map[string]any{"role": "assistant"}
	if content != "" {
		msg["content"] = content
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}

	finish := "stop"
	switch mr.StopReason {
	case "tool_use":
		finish = "tool_calls"
	case "max_tokens":
		finish = "length"
	}

	return map[string]any{
		"id":      strings.Replace(mr.ID, "msg_", "chatcmpl-", 1),
		"object":  "chat.completion",
		"model":   mr.Model,
		"choices": []map[string]any{{"index": 0, "message": msg, "finish_reason": finish}},
		"usage": map[string]any{
			"prompt_tokens":     mr.Usage.InputTokens,
			"completion_tokens": mr.Usage.OutputTokens,
			"total_tokens":      mr.Usage.InputTokens + mr.Usage.OutputTokens,
		},
	}
}
