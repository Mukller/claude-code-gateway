package core

import (
	"encoding/json"
)

type ImgURL struct {
	URL string `json:"url"`
}

type ContentPart struct {
	Type     string  `json:"type"`
	Text     string  `json:"text,omitempty"`
	ImageURL *ImgURL `json:"image_url,omitempty"`
}

type OAIFunc struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type OAIToolCall struct {
	ID       string  `json:"id"`
	Type     string  `json:"type"`
	Function OAIFunc `json:"function"`
}

type OAIToolCallDelta struct {
	Index    int     `json:"index"`
	ID       string  `json:"id,omitempty"`
	Type     string  `json:"type,omitempty"`
	Function OAIFunc `json:"function"`
}

type ChatMessage struct {
	Role       string        `json:"role"`
	Content    interface{}   `json:"content,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	ToolCalls  []OAIToolCall `json:"tool_calls,omitempty"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type OAIFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type OAIToolDef struct {
	Type     string         `json:"type"`
	Function OAIFunctionDef `json:"function"`
}

type ChatRequest struct {
	Model         string         `json:"model"`
	Messages      []ChatMessage  `json:"messages"`
	MaxTokens     int            `json:"max_tokens,omitempty"`
	Temperature   *float64       `json:"temperature,omitempty"`
	TopP          *float64       `json:"top_p,omitempty"`
	Stop          []string       `json:"stop,omitempty"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *StreamOptions `json:"stream_options,omitempty"`
	Tools         []OAIToolDef   `json:"tools,omitempty"`
	ToolChoice    interface{}    `json:"tool_choice,omitempty"`
	User          string         `json:"user,omitempty"`
}

type ChatDelta struct {
	Role      string             `json:"role,omitempty"`
	Content   string             `json:"content,omitempty"`
	ToolCalls []OAIToolCallDelta `json:"tool_calls,omitempty"`
}

type Choice struct {
	Index        int          `json:"index"`
	Message      *ChatMessage `json:"message,omitempty"`
	Delta        *ChatDelta   `json:"delta,omitempty"`
	FinishReason *string      `json:"finish_reason,omitempty"`
}

type OAIUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens,omitempty"`
}

type ChatResponse struct {
	ID      string    `json:"id"`
	Object  string    `json:"object,omitempty"`
	Created int64     `json:"created,omitempty"`
	Model   string    `json:"model,omitempty"`
	Choices []Choice  `json:"choices"`
	Usage   *OAIUsage `json:"usage,omitempty"`
}

type OAIChunk struct {
	ID      string    `json:"id"`
	Object  string    `json:"object,omitempty"`
	Model   string    `json:"model,omitempty"`
	Choices []Choice  `json:"choices"`
	Usage   *OAIUsage `json:"usage,omitempty"`
}

type OAIErrorBody struct {
	Message string      `json:"message,omitempty"`
	Type    string      `json:"type,omitempty"`
	Code    interface{} `json:"code,omitempty"`
}

type OAIError struct {
	Error OAIErrorBody `json:"error"`
}

type ModelList struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}
