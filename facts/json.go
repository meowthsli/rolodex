package facts

import (
	"encoding/json"
	"strings"
)

// --- JSON helpers ---

func marshalTypes(t []string) string {
	if len(t) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(t)
	return string(b)
}

func UnmarshalTypes(s string) []string {
	if s == "" {
		return nil
	}
	var t []string
	_ = json.Unmarshal([]byte(s), &t)
	return t
}

func unionStrings(a, b []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(a)+len(b))
	for _, x := range append(append([]string{}, a...), b...) {
		if x == "" {
			continue
		}
		k := strings.ToLower(x)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, x)
	}
	return out
}

// unionProperties concatenates two JSON arrays of property objects, dropping
// duplicate entries so all distinct values are preserved.
func unionProperties(existing string, add json.RawMessage) string {
	var a, b []json.RawMessage
	_ = json.Unmarshal([]byte(existing), &a)
	_ = json.Unmarshal(add, &b)
	seen := make(map[string]struct{})
	for _, x := range a {
		seen[string(x)] = struct{}{}
	}
	for _, x := range b {
		k := string(x)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		a = append(a, x)
	}
	out, _ := json.Marshal(a)
	return string(out)
}

func mustJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func aliasSetsIntersect(a, b []string) bool {
	set := make(map[string]struct{}, len(a))
	for _, x := range a {
		set[x] = struct{}{}
	}
	for _, x := range b {
		if _, ok := set[x]; ok {
			return true
		}
	}
	return false
}
