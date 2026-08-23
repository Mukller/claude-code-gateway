package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const ProtocolVersion = "2024-11-05"

type Handler struct {
	ServerInfo ServerInfo
	Stats      func() any
	Logs       func(limit int) any
	Models     func() any
	Providers  func() any
	Tokens     func() any
	Reload     func() (any, error)
	Cost       func(model string, inTok, outTok int64) any
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

func objectSchema(props map[string]any, required []string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

func textResult(v any) map[string]any {
	var text string
	switch t := v.(type) {
	case string:
		text = t
	default:
		b, _ := json.MarshalIndent(v, "", "  ")
		text = string(b)
	}
	return map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
		"isError": false,
	}
}

func errResult(msg string) map[string]any {
	return map[string]any{
		"content": []map[string]string{{"type": "text", "text": msg}},
		"isError": true,
	}
}

func (h *Handler) tools() []toolDef {
	return []toolDef{
		{Name: "gateway_stats", Description: "Usage statistics for the gateway: today and all-time requests, tokens and cost.", InputSchema: objectSchema(nil, nil)},
		{Name: "gateway_logs", Description: "Recent request log entries.", InputSchema: objectSchema(map[string]any{"limit": map[string]string{"type": "integer", "description": "how many entries (default 20)"}}, nil)},
		{Name: "gateway_models", Description: "Model catalog exposed by the gateway.", InputSchema: objectSchema(nil, nil)},
		{Name: "gateway_providers", Description: "Configured providers with key counts.", InputSchema: objectSchema(nil, nil)},
		{Name: "gateway_tokens", Description: "Client tokens with budget spend and reset times.", InputSchema: objectSchema(nil, nil)},
		{Name: "gateway_reload", Description: "Hot-reload providers/routing/pricing from config file.", InputSchema: objectSchema(nil, nil)},
		{Name: "estimate_cost", Description: "Estimate USD cost for a model using gateway pricing table.", InputSchema: objectSchema(map[string]any{
			"model":         map[string]string{"type": "string"},
			"input_tokens":  map[string]string{"type": "integer"},
			"output_tokens": map[string]string{"type": "integer"},
		}, []string{"model"})},
	}
}

func (h *Handler) callTool(name string, args json.RawMessage) (any, error) {
	var a struct {
		Limit        int    `json:"limit"`
		Model        string `json:"model"`
		InputTokens  int64  `json:"input_tokens"`
		OutputTokens int64  `json:"output_tokens"`
	}
	if len(args) > 0 {
		json.Unmarshal(args, &a)
	}
	switch name {
	case "gateway_stats":
		return textResult(h.Stats()), nil
	case "gateway_logs":
		if a.Limit <= 0 {
			a.Limit = 20
		}
		return textResult(h.Logs(a.Limit)), nil
	case "gateway_models":
		return textResult(h.Models()), nil
	case "gateway_providers":
		return textResult(h.Providers()), nil
	case "gateway_tokens":
		return textResult(h.Tokens()), nil
	case "gateway_reload":
		res, err := h.Reload()
		if err != nil {
			return errResult(err.Error()), nil
		}
		return textResult(res), nil
	case "estimate_cost":
		if a.Model == "" {
			return errResult("model is required"), nil
		}
		return textResult(h.Cost(a.Model, a.InputTokens, a.OutputTokens)), nil
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}

func (h *Handler) Dispatch(raw []byte) (response any, isNotification bool) {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return errorResponse(json.RawMessage("null"), -32700, "parse error"), false
	}
	isNotification = len(req.ID) == 0 || string(req.ID) == "null"

	result, rpcErr := func() (any, *rpcError) {
		switch req.Method {
		case "initialize":
			return map[string]any{
				"protocolVersion": ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      h.ServerInfo,
				"instructions":    "Gateway management tools: stats, logs, models, providers, tokens, reload, cost estimation.",
			}, nil
		case "notifications/initialized", "notifications/cancelled":
			return nil, nil
		case "ping":
			return map[string]any{}, nil
		case "tools/list":
			return map[string]any{"tools": h.tools()}, nil
		case "tools/call":
			res, err := h.callTool(req.Params.Name, req.Params.Arguments)
			if err != nil {
				return nil, &rpcError{Code: -32602, Message: err.Error()}
			}
			return res, nil
		default:
			return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
		}
	}()

	if rpcErr != nil {
		return errorResponse(req.ID, rpcErr.Code, rpcErr.Message), false
	}
	if isNotification {
		return nil, true
	}
	return successResponse(req.ID, result), false
}

func envelope(id json.RawMessage, key string, val any) map[string]any {
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	if len(id) == 0 {
		delete(resp, "id")
	}
	resp[key] = val
	return resp
}

func successResponse(id json.RawMessage, result any) map[string]any {
	return envelope(id, "result", result)
}

func errorResponse(id json.RawMessage, code int, msg string) map[string]any {
	return envelope(id, "error", rpcError{Code: code, Message: msg})
}

func ServeStream(r io.Reader, w io.Writer, h *Handler) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	enc := json.NewEncoder(w)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		resp, notification := h.Dispatch(line)
		if notification {
			continue
		}
		if resp == nil {
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}
