package core

import (
	"strings"
	"testing"
)

func TestTranslateRequestSimple(t *testing.T) {
	req := &MessagesRequest{
		Model:     "claude-sonnet-4-5",
		MaxTokens: 1000,
		System:    []byte(`"You are helpful."`),
		Messages: []ApiMessage{
			{Role: "user", Content: []byte(`"Hello"`)},
			{Role: "assistant", Content: []byte(`[{"type":"text","text":"Hi there"}]`)},
			{Role: "user", Content: []byte(`[{"type":"text","text":"Bye"}]`)},
		},
		Temperature: ptr(0.7),
	}
	out, err := TranslateRequest(req, "glm-5")
	if err != nil {
		t.Fatal(err)
	}
	if out.Model != "glm-5" || out.MaxTokens != 1000 {
		t.Fatalf("bad model/max_tokens: %+v", out)
	}
	if len(out.Messages) != 4 {
		t.Fatalf("want 4 messages, got %d", len(out.Messages))
	}
	if m := out.Messages[0]; m.Role != "system" || m.Content != "You are helpful." {
		t.Fatalf("bad system msg: %+v", m)
	}
	if m := out.Messages[1]; m.Role != "user" || m.Content != "Hello" {
		t.Fatalf("bad user msg: %+v", m)
	}
	if m := out.Messages[2]; m.Role != "assistant" || m.Content != "Hi there" {
		t.Fatalf("bad assistant msg: %+v", m)
	}
	if out.Temperature == nil || *out.Temperature != 0.7 {
		t.Fatal("temperature lost")
	}
}

func TestTranslateTools(t *testing.T) {
	req := &MessagesRequest{
		Model:     "m",
		MaxTokens: 100,
		Messages: []ApiMessage{
			{Role: "user", Content: []byte(`"what is the weather?"`)},
			{Role: "assistant", Content: []byte(`[
				{"type":"text","text":"Let me check"},
				{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Moscow"}}
			]`)},
			{Role: "user", Content: []byte(`[
				{"type":"tool_result","tool_use_id":"toolu_1","content":"+20C sunny"}
			]`)},
		},
		Tools: []ToolDef{{
			Name:        "get_weather",
			Description: "Get weather",
			InputSchema: []byte(`{"type":"object","properties":{"city":{"type":"string"}}}`),
		}},
		ToolChoice: []byte(`{"type":"auto"}`),
	}
	out, err := TranslateRequest(req, "target-model")
	if err != nil {
		t.Fatal(err)
	}
	var asst *ChatMessage
	var tool *ChatMessage
	for i := range out.Messages {
		switch out.Messages[i].Role {
		case "assistant":
			asst = &out.Messages[i]
		case "tool":
			tool = &out.Messages[i]
		}
	}
	if asst == nil || len(asst.ToolCalls) != 1 {
		t.Fatalf("expected assistant tool_calls, got %+v", out.Messages)
	}
	tc := asst.ToolCalls[0]
	if tc.ID != "toolu_1" || tc.Function.Name != "get_weather" {
		t.Fatalf("bad tool call: %+v", tc)
	}
	if !strings.Contains(tc.Function.Arguments, "Moscow") {
		t.Fatalf("bad args: %s", tc.Function.Arguments)
	}
	if tool == nil || tool.ToolCallID != "toolu_1" || tool.Content != "+20C sunny" {
		t.Fatalf("bad tool result msg: %+v", tool)
	}
	if len(out.Tools) != 1 || out.Tools[0].Function.Name != "get_weather" {
		t.Fatalf("bad tools: %+v", out.Tools)
	}
	if out.ToolChoice != "auto" {
		t.Fatalf("bad tool_choice: %v", out.ToolChoice)
	}
}

func TestTranslateResponse(t *testing.T) {
	finish := "tool_calls"
	cr := &ChatResponse{
		Model: "upstream-x",
		Choices: []Choice{{
			Message: &ChatMessage{
				Content: "partial answer",
				ToolCalls: []OAIToolCall{{
					ID:       "call_abc",
					Type:     "function",
					Function: OAIFunc{Name: "get_time", Arguments: `{"tz":"UTC"}`},
				}},
			},
			FinishReason: &finish,
		}},
		Usage: &OAIUsage{PromptTokens: 12, CompletionTokens: 34},
	}
	mr := TranslateResponse(cr, "req-model")
	if mr.Type != "message" || mr.Role != "assistant" {
		t.Fatalf("bad envelope: %+v", mr)
	}
	if mr.StopReason != "tool_use" {
		t.Fatalf("stop_reason = %s", mr.StopReason)
	}
	if mr.Usage.InputTokens != 12 || mr.Usage.OutputTokens != 34 {
		t.Fatalf("bad usage: %+v", mr.Usage)
	}
	var foundText, foundTool bool
	for _, b := range mr.Content {
		switch b.Type {
		case "text":
			foundText = b.Text == "partial answer"
		case "tool_use":
			foundTool = b.ID == "call_abc" && b.Name == "get_time" && string(b.Input) == `{"tz":"UTC"}`
		}
	}
	if !foundText || !foundTool {
		t.Fatalf("blocks missing: text=%v tool=%v content=%+v", foundText, foundTool, mr.Content)
	}
}

func TestStreamTranslator(t *testing.T) {
	tw := &testWriter{}
	tr := NewStreamTranslator(tw, func() {}, "test-model")
	chunks := []string{
		`{"id":"c1","model":"x","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"content":"Hel"}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"content":"lo"}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
	}
	tr.Start()
	for _, c := range chunks {
		tr.Chunk([]byte(c))
	}
	u := tr.Finalize()
	out := tw.buf.String()
	if !strings.Contains(out, `"type":"message_start"`) {
		t.Fatal("missing message_start")
	}
	if !strings.Contains(out, `"text_delta"`) || !strings.Contains(out, "Hel") || !strings.Contains(out, "lo") {
		t.Fatal("missing text deltas")
	}
	if !strings.Contains(out, `"stop_reason":"end_turn"`) {
		t.Fatal("missing stop reason")
	}
	if !strings.Contains(out, `"type":"message_stop"`) {
		t.Fatal("missing message_stop")
	}
	if u.InputTokens != 5 || u.OutputTokens != 2 {
		t.Fatalf("bad usage: %+v", u)
	}
	idx := strings.Index(out, "message_stop")
	if idx < strings.Index(out, "message_delta") {
		t.Fatal("message_stop must come after message_delta")
	}
}

func TestStreamTranslatorToolCalls(t *testing.T) {
	tw := &testWriter{}
	tr := NewStreamTranslator(tw, func() {}, "test-model")
	tr.Start()
	tr.Chunk([]byte(`{"choices":[{"index":0,"delta":{"content":"calling"}}]}`))
	tr.Chunk([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"f","arguments":""}}]}}]}`))
	tr.Chunk([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":1}"}}]}}]}`))
	tr.Chunk([]byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`))
	tr.Finalize()
	out := tw.buf.String()
	if !strings.Contains(out, `"type":"tool_use"`) || !strings.Contains(out, `"name":"f"`) {
		t.Fatal("missing tool_use block start")
	}
	if !strings.Contains(out, `input_json_delta`) || !strings.Contains(out, `{\"a\":1}`) {
		t.Fatal("missing input json delta")
	}
	if !strings.Contains(out, `"stop_reason":"tool_use"`) {
		t.Fatal("wrong stop reason")
	}
}

func TestEstimateTokens(t *testing.T) {
	req := &MessagesRequest{
		System:   []byte(`"sys"`),
		Messages: []ApiMessage{{Role: "user", Content: []byte(`"hello world this is a test"`)}},
	}
	n := EstimateRequestTokens(req)
	if n < 3 {
		t.Fatalf("estimate too small: %d", n)
	}
}

func TestCollectOpenAIStream(t *testing.T) {
	src := strings.Join([]string{
		`data: {"model":"up","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"content":"he"}}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":"llo"}}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f","arguments":""}}]}}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}],"usage":{"prompt_tokens":7,"completion_tokens":9}}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	mr, err := CollectOpenAIStream(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if mr.StopReason != "tool_use" {
		t.Fatalf("stop_reason = %s", mr.StopReason)
	}
	if mr.Usage.InputTokens != 7 || mr.Usage.OutputTokens != 9 {
		t.Fatalf("usage = %+v", mr.Usage)
	}
	var text, tool string
	for _, b := range mr.Content {
		if b.Type == "text" {
			text = b.Text
		}
		if b.Type == "tool_use" {
			tool = b.Name + ":" + string(b.Input)
		}
	}
	if text != "hello" {
		t.Fatalf("text = %q", text)
	}
	if tool != "f:{}" {
		t.Fatalf("tool = %q", tool)
	}
}

func TestCollectAnthropicStream(t *testing.T) {
	src := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":11}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"abc"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	mr, err := CollectAnthropicStream(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(mr.Content) != 1 || mr.Content[0].Text != "abc" {
		t.Fatalf("content = %+v", mr.Content)
	}
	if mr.Usage.InputTokens != 11 || mr.Usage.OutputTokens != 3 || mr.Model != "m1" {
		t.Fatalf("meta = %+v model=%s", mr.Usage, mr.Model)
	}
}

type testWriter struct{ buf strings.Builder }

func (w *testWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }

func ptr[T any](v T) *T { return &v }
