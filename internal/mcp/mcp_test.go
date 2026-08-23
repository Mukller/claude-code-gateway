package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDispatchInitializeAndTools(t *testing.T) {
	h := &Handler{
		ServerInfo: ServerInfo{Name: "test-gw", Version: "1.0"},
		Stats:      func() any { return map[string]any{"requests": 42} },
		Cost: func(model string, in, out int64) any {
			return map[string]any{"cost_usd": 0.5, "model": model}
		},
	}

	resp, notif := h.Dispatch([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if notif {
		t.Fatal("initialize is a request")
	}
	m := resp.(map[string]any)
	res := m["result"].(map[string]any)
	if res["protocolVersion"] != ProtocolVersion {
		t.Fatalf("protocol = %v", res["protocolVersion"])
	}

	resp, _ = h.Dispatch([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	tools := resp.(map[string]any)["result"].(map[string]any)["tools"].([]toolDef)
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"gateway_stats", "gateway_reload", "estimate_cost"} {
		if !names[want] {
			t.Fatalf("tool %s missing", want)
		}
	}

	resp, _ = h.Dispatch([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"gateway_stats"}}`))
	content := resp.(map[string]any)["result"].(map[string]any)["content"].([]map[string]string)
	if !strings.Contains(content[0]["text"], "42") {
		t.Fatalf("stats text = %s", content[0]["text"])
	}

	resp, _ = h.Dispatch([]byte(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"estimate_cost","arguments":{"model":"claude-sonnet","input_tokens":1000}}}`))
	res = resp.(map[string]any)["result"].(map[string]any)
	if res["isError"] != false || !strings.Contains(res["content"].([]map[string]string)[0]["text"], "claude-sonnet") {
		t.Fatalf("estimate_cost broken: %+v", res)
	}
}

func TestDispatchNotificationAndErrors(t *testing.T) {
	h := &Handler{ServerInfo: ServerInfo{Name: "g", Version: "1"}}
	_, notif := h.Dispatch([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if !notif {
		t.Fatal("initialized must be notification")
	}

	resp, _ := h.Dispatch([]byte(`{"jsonrpc":"2.0","id":9,"method":"nope/method"}`))
	e := resp.(map[string]any)["error"].(rpcError)
	if e.Code != -32601 {
		t.Fatalf("error code = %v", e.Code)
	}
}

func TestServeStream(t *testing.T) {
	h := &Handler{
		ServerInfo: ServerInfo{Name: "g", Version: "1"},
		Stats:      func() any { return "ok-stats" },
	}
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"gateway_stats"}}`,
	}, "\n")
	var out bytes.Buffer
	if err := ServeStream(strings.NewReader(in), &out, h); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 responses (notification skipped), got %d: %v", len(lines), lines)
	}
	var r1, r2 map[string]any
	json.Unmarshal([]byte(lines[0]), &r1)
	json.Unmarshal([]byte(lines[1]), &r2)
	if _, hasResult := r1["result"]; !hasResult {
		t.Fatalf("ping response missing result: %v", r1)
	}
	text := r2["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]
	if text != "ok-stats" {
		t.Fatalf("stats via stdio = %v", text)
	}
}
