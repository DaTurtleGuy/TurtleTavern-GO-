package handlers

import (
	"fmt"
	"strings"
)

func fileFieldString(body map[string]any, field string) (string, bool) {
	v, ok := body[field]
	if !ok || v == nil {
		return "", true
	}
	var s string
	switch t := v.(type) {
	case string:
		s = t
	default:
		s = fmt.Sprint(t)
	}
	if strings.ContainsAny(s, "/\\\x00") {
		return "", false
	}
	return s, true
}

func validFileField(body map[string]any, field string) bool {
	_, ok := fileFieldString(body, field)
	return ok
}

func orAny(v any, defaults ...any) any {
	if v == nil {
		if len(defaults) > 0 {
			return defaults[0]
		}
		return ""
	}
	if s, ok := v.(string); ok {
		if s == "" && len(defaults) > 0 {
			return defaults[0]
		}
		return s
	}
	return v
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func orAnyString(v any, defaults ...string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	if len(defaults) > 0 {
		return defaults[0]
	}
	return ""
}

// goUniqueName replicates Node's getUniqueName default behavior:
// startIndex=1 with nameBuilder (base,i) => i===0 ? base : "base (i)",
// so the first candidate is "base (1)".
func goUniqueName(base string, exists func(string) bool) string {
	for i := 1; i < 10000; i++ {
		candidate := base + " (" + itoa(i) + ")"
		if !exists(candidate) {
			return candidate
		}
	}
	return base + " (" + itoa(10000) + ")"
}

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}
