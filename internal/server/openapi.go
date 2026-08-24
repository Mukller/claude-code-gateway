package server

import (
	"net/http"
)

func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	spec := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       "Claude Code Gateway",
			"version":     Version,
			"description": "Self-hosted AI gateway: multi-provider routing, key rotation, fallbacks, budgets, semantic cache, guardrails.",
		},
		"servers": []map[string]any{
			{"url": "http://localhost:8090"},
		},
		"paths": map[string]any{
			"/v1/messages": map[string]any{
				"post": map[string]any{
					"summary":  "Anthropic Messages API (streaming + non-streaming)",
					"security": []map[string][]string{{"ApiKeyAuth": {}}},
					"requestBody": map[string]any{
						"required": true,
						"content": map[string]any{"application/json": map[string]any{
							"schema": map[string]any{"$ref": "#/components/schemas/MessagesRequest"},
						}},
					},
					"responses": map[string]any{
						"200": map[string]any{"description": "Message response or SSE stream"},
						"400": map[string]any{"description": "Invalid request"},
						"401": map[string]any{"description": "Unauthorized"},
						"429": map[string]any{"description": "Rate limited or budget exceeded"},
						"502": map[string]any{"description": "All providers exhausted"},
					},
				},
			},
			"/v1/messages/count_tokens": map[string]any{
				"post": map[string]any{
					"summary":   "Estimate input tokens",
					"responses": map[string]any{"200": map[string]any{"description": "Token count"}},
				},
			},
			"/v1/models": map[string]any{
				"get": map[string]any{
					"summary": "Model catalog",
					"parameters": []map[string]any{{
						"name": "format", "in": "query",
						"schema": map[string]any{"type": "string", "enum": []string{"anthropic", "openai"}},
					}},
					"responses": map[string]any{"200": map[string]any{"description": "Model list"}},
				},
			},
			"/healthz": map[string]any{
				"get": map[string]any{"summary": "Health check", "responses": map[string]any{"200": map[string]any{"description": "OK"}}},
			},
			"/metrics": map[string]any{
				"get": map[string]any{"summary": "Prometheus metrics", "responses": map[string]any{"200": map[string]any{"description": "Metrics in text format"}}},
			},
			"/mcp": map[string]any{
				"post": map[string]any{"summary": "MCP JSON-RPC endpoint", "responses": map[string]any{"200": map[string]any{"description": "JSON-RPC response"}}},
			},
			"/admin/stats": map[string]any{
				"get": map[string]any{"summary": "Usage statistics", "responses": map[string]any{"200": map[string]any{"description": "Aggregated stats"}}},
			},
			"/admin/logs": map[string]any{
				"get": map[string]any{"summary": "Recent request log", "responses": map[string]any{"200": map[string]any{"description": "Log entries"}}},
			},
			"/admin/tokens": map[string]any{
				"get": map[string]any{"summary": "Client budgets", "responses": map[string]any{"200": map[string]any{"description": "Token reports"}}},
			},
			"/admin/reload": map[string]any{
				"post": map[string]any{"summary": "Hot reload config", "responses": map[string]any{"200": map[string]any{"description": "Reload result"}}},
			},
			"/admin/flush-cache": map[string]any{
				"post": map[string]any{"summary": "Flush response cache", "responses": map[string]any{"200": map[string]any{"description": "Flushed"}}},
			},
			"/admin/export.csv": map[string]any{
				"get": map[string]any{"summary": "Export usage as CSV", "responses": map[string]any{"200": map[string]any{"description": "CSV file"}}},
			},
		},
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"ApiKeyAuth": map[string]any{
					"type": "apiKey", "in": "header", "name": "x-api-key",
				},
			},
			"schemas": map[string]any{
				"MessagesRequest": map[string]any{
					"type":     "object",
					"required": []string{"model", "messages", "max_tokens"},
					"properties": map[string]any{
						"model":      map[string]any{"type": "string"},
						"max_tokens": map[string]any{"type": "integer"},
						"stream":     map[string]any{"type": "boolean"},
						"system":     map[string]any{"type": "string"},
						"messages": map[string]any{"type": "array", "items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"role":    map[string]any{"type": "string", "enum": []string{"user", "assistant"}},
								"content": map[string]any{},
							},
						}},
						"tools": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
					},
				},
			},
		},
	}
	writeJSON(w, http.StatusOK, spec)
}
