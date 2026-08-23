package provider

import (
	"encoding/json"
	"testing"
)

func applySpecs(t *testing.T, specs []string, payload string) string {
	t.Helper()
	fns, err := ParseTransforms(specs)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatal(err)
	}
	ApplyTransforms(m, fns)
	out, _ := json.Marshal(m)
	return string(out)
}

func TestTransformSetAndCap(t *testing.T) {
	out := applySpecs(t,
		[]string{"set:temperature=0.2", "max_tokens_cap:1024"},
		`{"max_tokens":8192,"temperature":1.0}`)
	var m map[string]any
	json.Unmarshal([]byte(out), &m)
	if m["max_tokens"] != 1024.0 {
		t.Fatalf("cap failed: %s", out)
	}
	if m["temperature"] != 0.2 {
		t.Fatalf("set failed: %s", out)
	}
}

func TestTransformReasoningEffortAndDropKeys(t *testing.T) {
	out := applySpecs(t,
		[]string{"reasoning_effort:high", "drop_keys:metadata,user"},
		`{"metadata":{"x":1},"user":"u","messages":[]}`)
	if out != `{"messages":[],"reasoning_effort":"high"}` {
		t.Fatalf("got %s", out)
	}
}

func TestTransformSystemPrefixExistingSystem(t *testing.T) {
	out := applySpecs(t, []string{"system_prefix:BE BRIEF"},
		`{"messages":[{"role":"system","content":"be nice"},{"role":"user","content":"hi"}]}`)
	var m map[string]any
	json.Unmarshal([]byte(out), &m)
	msgs := m["messages"].([]any)
	first := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "BE BRIEF\n\nbe nice" {
		t.Fatalf("prefix failed: %s", out)
	}
	if len(msgs) != 2 {
		t.Fatalf("extra system injected: %s", out)
	}
}

func TestTransformSystemPrefixNoSystem(t *testing.T) {
	out := applySpecs(t, []string{"system_prefix:RULES"},
		`{"messages":[{"role":"user","content":"hi"}]}`)
	var m map[string]any
	json.Unmarshal([]byte(out), &m)
	msgs := m["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" {
		t.Fatalf("inject failed: %s", out)
	}
}

func TestParseTransformsErrors(t *testing.T) {
	for _, bad := range [][]string{
		{"unknown_thing"},
		{"set:noequalsign"},
		{"max_tokens_cap:notanumber"},
		{"reasoning_effort:"},
	} {
		if _, err := ParseTransforms(bad); err == nil {
			t.Fatalf("expected error for %v", bad)
		}
	}
}
