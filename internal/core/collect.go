package core

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

type streamToolAcc struct {
	id   string
	name string
	args strings.Builder
}

func scanSSEData(src io.Reader, handle func(payload string) bool) error {
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 0, 64*1024), 32*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			return nil
		}
		if !handle(payload) {
			break
		}
	}
	return sc.Err()
}

func CollectOpenAIStream(src io.Reader) (*MessageResponse, error) {
	mr := &MessageResponse{
		ID:      "msg_" + RandHex(12),
		Type:    "message",
		Role:    "assistant",
		Content: []Block{},
	}
	var text strings.Builder
	tools := map[int]*streamToolAcc{}
	var order []int
	stop := ""
	err := scanSSEData(src, func(payload string) bool {
		var ch OAIChunk
		if json.Unmarshal([]byte(payload), &ch) != nil {
			return true
		}
		if ch.Model != "" {
			mr.Model = ch.Model
		}
		if ch.Usage != nil {
			mr.Usage.InputTokens = ch.Usage.PromptTokens
			mr.Usage.OutputTokens = ch.Usage.CompletionTokens
		}
		if len(ch.Choices) == 0 {
			return true
		}
		c := ch.Choices[0]
		if d := c.Delta; d != nil {
			if d.Content != "" {
				text.WriteString(d.Content)
			}
			for _, tc := range d.ToolCalls {
				ta, ok := tools[tc.Index]
				if !ok {
					ta = &streamToolAcc{}
					tools[tc.Index] = ta
					order = append(order, tc.Index)
				}
				if tc.ID != "" {
					ta.id = tc.ID
				}
				if tc.Function.Name != "" {
					ta.name = tc.Function.Name
				}
				ta.args.WriteString(tc.Function.Arguments)
			}
		}
		if c.FinishReason != nil && *c.FinishReason != "" {
			stop = StopFromFinish(c.FinishReason)
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	if t := text.String(); t != "" {
		mr.Content = append(mr.Content, Block{Type: "text", Text: t})
	}
	sortInts(order)
	for _, i := range order {
		ta := tools[i]
		in := json.RawMessage(ta.args.String())
		if len(in) == 0 || !json.Valid(in) {
			in = json.RawMessage("{}")
		}
		mr.Content = append(mr.Content, Block{
			Type:  "tool_use",
			ID:    FirstNonEmpty(ta.id, "toolu_"+RandHex(10)),
			Name:  ta.name,
			Input: in,
		})
	}
	if len(mr.Content) == 0 {
		mr.Content = append(mr.Content, Block{Type: "text", Text: ""})
	}
	mr.StopReason = FirstNonEmpty(stop, "end_turn")
	if mr.Model == "" {
		mr.Model = "unknown"
	}
	if mr.Usage.InputTokens == 0 && mr.Usage.OutputTokens == 0 {
		mr.Usage.OutputTokens = EstimateTextTokens(text.String()) + int64(len(tools))*60
	}
	return mr, nil
}

type anthCollector struct {
	blocks map[int64]*Block
	inputs map[int64]*strings.Builder
	order  []int64
	mr     *MessageResponse
}

func CollectAnthropicStream(src io.Reader) (*MessageResponse, error) {
	ac := &anthCollector{
		blocks: map[int64]*Block{},
		inputs: map[int64]*strings.Builder{},
		mr: &MessageResponse{
			ID:      "msg_" + RandHex(12),
			Type:    "message",
			Role:    "assistant",
			Content: []Block{},
		},
	}
	err := scanSSEData(src, func(payload string) bool {
		var ev struct {
			Type         string           `json:"type"`
			Index        int64            `json:"index"`
			Message      *MessageResponse `json:"message"`
			ContentBlock *struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta *struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
			StopReason string `json:"stop_reason"`
			Usage      *Usage `json:"usage"`
		}
		if json.Unmarshal([]byte(payload), &ev) != nil {
			return true
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				ac.mr.ID = FirstNonEmpty(ev.Message.ID, ac.mr.ID)
				ac.mr.Model = ev.Message.Model
				ac.mr.Usage.InputTokens = ev.Message.Usage.InputTokens
				ac.mr.Usage.CacheReadInputTokens = ev.Message.Usage.CacheReadInputTokens
				ac.mr.Usage.CacheCreationInputTokens = ev.Message.Usage.CacheCreationInputTokens
			}
		case "content_block_start":
			if ev.ContentBlock != nil {
				b := &Block{Type: ev.ContentBlock.Type, ID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name}
				ac.blocks[ev.Index] = b
				ac.inputs[ev.Index] = &strings.Builder{}
				ac.order = append(ac.order, ev.Index)
			}
		case "content_block_delta":
			b := ac.blocks[ev.Index]
			buf := ac.inputs[ev.Index]
			if b == nil || buf == nil || ev.Delta == nil {
				return true
			}
			switch ev.Delta.Type {
			case "text_delta":
				b.Text += ev.Delta.Text
			case "input_json_delta":
				buf.WriteString(ev.Delta.PartialJSON)
			}
		case "message_delta":
			if ev.StopReason != "" {
				ac.mr.StopReason = ev.StopReason
			}
			if ev.Usage != nil && ev.Usage.OutputTokens > 0 {
				ac.mr.Usage.OutputTokens = ev.Usage.OutputTokens
			}
		case "error":
			return false
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	sortInts64(ac.order)
	for _, i := range ac.order {
		b := ac.blocks[i]
		raw := ac.inputs[i].String()
		if b.Type == "tool_use" {
			if raw == "" || !json.Valid([]byte(raw)) {
				raw = "{}"
			}
			b.Input = json.RawMessage(raw)
		}
		if b.Type != "thinking" {
			ac.mr.Content = append(ac.mr.Content, *b)
		}
	}
	if len(ac.mr.Content) == 0 {
		ac.mr.Content = append(ac.mr.Content, Block{Type: "text", Text: ""})
	}
	if ac.mr.StopReason == "" {
		ac.mr.StopReason = "end_turn"
	}
	return ac.mr, nil
}

func sortInts64(a []int64) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}

func WriteSyntheticStream(dst io.Writer, flush func(), mr *MessageResponse) {
	emit := func(event string, payload any) {
		b, _ := json.Marshal(payload)
		dst.Write([]byte("event: " + event + "\ndata: " + string(b) + "\n\n"))
		if flush != nil {
			flush()
		}
	}
	in := mr.Usage.InputTokens
	out := mr.Usage.OutputTokens
	if out == 0 {
		total := 0
		for _, b := range mr.Content {
			total += len(b.Text) + len(b.Input)
		}
		out = EstimateTextTokens(strings.Repeat("x", total))
	}
	emit("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": FirstNonEmpty(mr.ID, "msg_"+RandHex(12)), "type": "message", "role": "assistant", "model": mr.Model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]int64{"input_tokens": in, "output_tokens": 1},
		},
	})
	for i, b := range mr.Content {
		switch b.Type {
		case "tool_use":
			emit("content_block_start", map[string]any{
				"type": "content_block_start", "index": i,
				"content_block": map[string]any{"type": "tool_use", "id": b.ID, "name": b.Name},
			})
			args := string(b.Input)
			if args == "" {
				args = "{}"
			}
			emit("content_block_delta", map[string]any{
				"type": "content_block_delta", "index": i,
				"delta": map[string]string{"type": "input_json_delta", "partial_json": args},
			})
		default:
			emit("content_block_start", map[string]any{
				"type": "content_block_start", "index": i,
				"content_block": map[string]any{"type": "text", "text": ""},
			})
			if b.Text != "" {
				emit("content_block_delta", map[string]any{
					"type": "content_block_delta", "index": i,
					"delta": map[string]string{"type": "text_delta", "text": b.Text},
				})
			}
		}
		emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
	}
	sr := mr.StopReason
	if sr == "" {
		sr = "end_turn"
	}
	emit("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": sr, "stop_sequence": nil},
		"usage": map[string]int64{"output_tokens": out, "input_tokens": in},
	})
	emit("message_stop", map[string]any{"type": "message_stop"})
}
