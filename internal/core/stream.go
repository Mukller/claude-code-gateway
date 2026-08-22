package core

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type StreamTranslator struct {
	w          io.Writer
	flush      func()
	model      string
	msgID      string
	nextIdx    int
	openText   bool
	textIdx    int
	textChars  int
	tools      map[int]*streamTool
	usageIn    int64
	usageOut   int64
	stopReason string
	started    bool
}

type streamTool struct {
	aIdx    int
	started bool
	id      string
	name    string
}

func NewStreamTranslator(w io.Writer, flush func(), model string) *StreamTranslator {
	return &StreamTranslator{
		w:     w,
		flush: flush,
		model: model,
		msgID: "msg_" + RandHex(12),
		tools: map[int]*streamTool{},
	}
}

func (t *StreamTranslator) emit(event string, payload any) {
	b, _ := json.Marshal(payload)
	fmt.Fprintf(t.w, "event: %s\ndata: %s\n\n", event, b)
	if t.flush != nil {
		t.flush()
	}
}

func (t *StreamTranslator) Start() {
	if t.started {
		return
	}
	t.started = true
	t.emit("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            t.msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         t.model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]int64{"input_tokens": 0, "output_tokens": 1},
		},
	})
}

func (t *StreamTranslator) ensureText() {
	if t.openText {
		return
	}
	t.openText = true
	t.textIdx = t.nextIdx
	t.nextIdx++
	t.emit("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         t.textIdx,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
}

func (t *StreamTranslator) closeText() {
	if !t.openText {
		return
	}
	t.openText = false
	t.emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": t.textIdx})
}

func (t *StreamTranslator) tool(i int) *streamTool {
	st, ok := t.tools[i]
	if !ok {
		st = &streamTool{}
		t.tools[i] = st
	}
	return st
}

func (t *StreamTranslator) Chunk(line []byte) {
	line = []byte(strings.TrimSpace(string(line)))
	if len(line) == 0 || string(line) == "[DONE]" {
		return
	}
	var ch OAIChunk
	if err := json.Unmarshal(line, &ch); err != nil {
		return
	}
	if ch.Usage != nil {
		t.usageIn = ch.Usage.PromptTokens
		t.usageOut = ch.Usage.CompletionTokens
	}
	if len(ch.Choices) == 0 {
		return
	}
	c := ch.Choices[0]
	d := c.Delta
	if d != nil {
		if d.Content != "" {
			t.ensureText()
			t.textChars += len(d.Content)
			t.emit("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": t.textIdx,
				"delta": map[string]string{"type": "text_delta", "text": d.Content},
			})
		}
		for _, tc := range d.ToolCalls {
			st := t.tool(tc.Index)
			if !st.started {
				t.closeText()
				st.started = true
				st.aIdx = t.nextIdx
				t.nextIdx++
				st.id = FirstNonEmpty(tc.ID, "toolu_"+RandHex(10))
				st.name = tc.Function.Name
				t.emit("content_block_start", map[string]any{
					"type":          "content_block_start",
					"index":         st.aIdx,
					"content_block": map[string]any{"type": "tool_use", "id": st.id, "name": st.name},
				})
			} else if tc.Function.Name != "" && st.name == "" {
				st.name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				t.emit("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": st.aIdx,
					"delta": map[string]string{"type": "input_json_delta", "partial_json": tc.Function.Arguments},
				})
			}
		}
	}
	if c.FinishReason != nil && *c.FinishReason != "" {
		t.stopReason = StopFromFinish(c.FinishReason)
	}
}

func (t *StreamTranslator) Finalize() Usage {
	t.closeText()
	idxs := make([]int, 0, len(t.tools))
	for i := range t.tools {
		idxs = append(idxs, i)
	}
	sortInts(idxs)
	for _, i := range idxs {
		st := t.tools[i]
		if st.started {
			t.emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": st.aIdx})
		}
	}
	in := t.usageIn
	out := t.usageOut
	if out == 0 {
		out = EstimateTextTokens(strings.Repeat("x", t.textChars)) + int64(len(t.tools))*60
	}
	sr := t.stopReason
	if sr == "" {
		sr = "end_turn"
	}
	t.emit("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": sr, "stop_sequence": nil},
		"usage": map[string]int64{"output_tokens": out, "input_tokens": in},
	})
	t.emit("message_stop", map[string]any{"type": "message_stop"})
	return Usage{InputTokens: in, OutputTokens: out}
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}

func TranslateOpenAIStream(dst io.Writer, flush func(), src io.Reader, model string) (Usage, error) {
	tr := NewStreamTranslator(dst, flush, model)
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	first := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		if first {
			tr.Start()
			first = false
		}
		tr.Chunk([]byte(payload))
	}
	if first && sc.Err() == nil {
		tr.Start()
	}
	u := tr.Finalize()
	return u, sc.Err()
}

type usageSniffer struct {
	Message *struct {
		Usage Usage `json:"usage"`
	} `json:"message"`
	Usage *Usage `json:"usage"`
	Type  string `json:"type"`
}

func PassthroughAnthropicStream(dst io.Writer, flush func(), src io.Reader) (Usage, error) {
	var u Usage
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		raw := sc.Text() + "\n"
		dst.Write([]byte(raw))
		flush()
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimPrefix(line, "data:")
		var sn usageSniffer
		if err := json.Unmarshal([]byte(payload), &sn); err != nil {
			continue
		}
		if sn.Message != nil {
			u.InputTokens = sn.Message.Usage.InputTokens
			u.CacheReadInputTokens = sn.Message.Usage.CacheReadInputTokens
			u.CacheCreationInputTokens = sn.Message.Usage.CacheCreationInputTokens
		}
		if sn.Usage != nil {
			if sn.Type == "message_start" {
				u.InputTokens = sn.Usage.InputTokens
			} else if sn.Usage.OutputTokens > 0 {
				u.OutputTokens = sn.Usage.OutputTokens
			}
		}
	}
	return u, sc.Err()
}

func ExtractAnthropicUsage(data []byte) Usage {
	var top struct {
		Usage Usage `json:"usage"`
	}
	json.Unmarshal(data, &top)
	return top.Usage
}
