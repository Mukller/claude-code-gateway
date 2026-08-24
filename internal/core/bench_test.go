package core

import (
	"encoding/json"
	"testing"
)

func BenchmarkTranslateRequestSimple(b *testing.B) {
	req := &MessagesRequest{
		Model:     "test",
		MaxTokens: 4096,
		Messages: []ApiMessage{
			{Role: "user", Content: []byte(`"Write a function to reverse a linked list in Go"`)},
		},
		System: []byte(`"You are a helpful coding assistant."`),
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := TranslateRequest(req, "target")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTranslateRequestTools(b *testing.B) {
	req := &MessagesRequest{
		Model:     "test",
		MaxTokens: 4096,
		Messages: []ApiMessage{
			{Role: "user", Content: []byte(`"what is the weather?"`)},
			{Role: "assistant", Content: []byte(`[{"type":"tool_use","id":"t1","name":"get_weather","input":{"city":"Moscow"}}]`)},
			{Role: "user", Content: []byte(`[{"type":"tool_result","tool_use_id":"t1","content":"+20C"}]`)},
		},
		Tools: []ToolDef{{
			Name:        "get_weather",
			Description: "Get weather for a city",
			InputSchema: []byte(`{"type":"object","properties":{"city":{"type":"string"}}}`),
		}},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := TranslateRequest(req, "target")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTranslateRequestLarge(b *testing.B) {
	long := make([]byte, 50000)
	for i := range long {
		long[i] = byte('a' + i%26)
	}
	longJSON, _ := json.Marshal(string(long))
	req := &MessagesRequest{
		Model:     "test",
		MaxTokens: 4096,
		Messages: []ApiMessage{
			{Role: "user", Content: longJSON},
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := TranslateRequest(req, "target")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTranslateResponse(b *testing.B) {
	finish := "stop"
	cr := &ChatResponse{
		Model: "test",
		Choices: []Choice{{
			Message:      &ChatMessage{Content: "Hello, world!", Role: "assistant"},
			FinishReason: &finish,
		}},
		Usage: &OAIUsage{PromptTokens: 100, CompletionTokens: 50},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		TranslateResponse(cr, "req-model")
	}
}

func BenchmarkTranslateStream(b *testing.B) {
	chunks := make([]string, 100)
	for i := range chunks {
		chunks[i] = `{"choices":[{"index":0,"delta":{"content":"chunk ` + jsonInt(i) + ` text here"}}]}`
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tw := &testWriter{}
		tr := NewStreamTranslator(tw, func() {}, "test")
		tr.Start()
		for _, c := range chunks {
			tr.Chunk([]byte(c))
		}
		tr.Finalize()
	}
}

func BenchmarkEstimateTokens(b *testing.B) {
	req := &MessagesRequest{
		System:   []byte(`"system prompt here"`),
		Messages: []ApiMessage{{Role: "user", Content: []byte(`"a reasonably long user message for token estimation"`)}},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EstimateRequestTokens(req)
	}
}

func jsonInt(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
