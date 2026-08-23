package provider

import (
	"fmt"
	"strconv"
	"strings"
)

type TransformFunc func(m map[string]any)

func ParseTransforms(specs []string) ([]TransformFunc, error) {
	out := make([]TransformFunc, 0, len(specs))
	for _, spec := range specs {
		name, arg, _ := strings.Cut(spec, ":")
		var fn TransformFunc
		switch name {
		case "set":
			k, v, ok := strings.Cut(arg, "=")
			if !ok || k == "" {
				return nil, fmt.Errorf("transform set: want key=value, got %q", arg)
			}
			fn = setField(k, parseScalar(v))
		case "max_tokens_cap":
			n, err := strconv.Atoi(arg)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("transform max_tokens_cap: want int, got %q", arg)
			}
			fn = func(m map[string]any) {
				if mt, ok := m["max_tokens"].(float64); ok && mt > float64(n) {
					m["max_tokens"] = float64(n)
				}
			}
		case "reasoning_effort":
			if arg == "" {
				return nil, fmt.Errorf("transform reasoning_effort: need low|medium|high")
			}
			fn = setField("reasoning_effort", arg)
		case "drop_keys":
			keys := strings.Split(arg, ",")
			fn = func(m map[string]any) {
				for _, k := range keys {
					delete(m, strings.TrimSpace(k))
				}
			}
		case "system_prefix":
			text := arg
			fn = func(m map[string]any) {
				sys, _ := m["messages"].([]any)
				if len(sys) == 0 {
					return
				}
				if first, ok := sys[0].(map[string]any); ok && first["role"] == "system" {
					if c, ok := first["content"].(string); ok {
						first["content"] = text + "\n\n" + c
						return
					}
				}
				newMsgs := make([]any, 0, len(sys)+1)
				newMsgs = append(newMsgs, map[string]any{"role": "system", "content": text})
				newMsgs = append(newMsgs, sys...)
				m["messages"] = newMsgs
			}
		default:
			return nil, fmt.Errorf("unknown transformer %q", name)
		}
		out = append(out, fn)
	}
	return out, nil
}

func ApplyTransforms(m map[string]any, fns []TransformFunc) {
	for _, fn := range fns {
		fn(m)
	}
}

func setField(key string, val any) TransformFunc {
	return func(m map[string]any) { m[key] = val }
}

func parseScalar(v string) any {
	switch v {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return f
	}
	return v
}
