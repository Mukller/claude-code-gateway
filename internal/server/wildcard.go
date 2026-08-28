package server

import "strings"

// matchWildcard checks whether a model identifier matches a pattern.
// Pattern syntax: only the suffix "*" is supported (e.g. "openai/*", "deepseek/*").
// Other wildcard characters in the pattern are treated as literals.
func matchWildcard(pattern, model string) (bool, bool) {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	model = strings.ToLower(strings.TrimSpace(model))
	if pattern == "" {
		return false, false
	}
	if !strings.ContainsAny(pattern, "*?") {
		if pattern == model {
			return true, true
		}
		return false, false
	}
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		if strings.HasPrefix(model, prefix) {
			return true, true
		}
	}
	return false, false
}
