package schema

import (
	"encoding/json"
	"strings"
)

// ParseLenient extracts the first balanced JSON object from raw (stripping
// markdown code fences and ignoring braces inside string literals),
// unmarshals it into T, and validates required fields/ranges for the known
// types (View, Judge, AbsoluteGrade). It returns (zero, false) on failure.
func ParseLenient[T any](raw string) (T, bool) {
	var zero T
	obj, ok := firstBalancedObject(stripFences(raw))
	if !ok {
		return zero, false
	}
	var v T
	if err := json.Unmarshal([]byte(obj), &v); err != nil {
		return zero, false
	}
	if !validate(v) {
		return zero, false
	}
	return v, true
}

func stripFences(s string) string {
	s = strings.TrimSpace(s)
	// Remove ```json ... ``` or ``` ... ``` wrappers wherever they appear.
	for {
		start := strings.Index(s, "```")
		if start < 0 {
			return s
		}
		rest := s[start+3:]
		end := strings.Index(rest, "```")
		if end < 0 {
			return s
		}
		inner := rest[:end]
		inner = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(inner), "json"))
		s = strings.TrimSpace(s[:start] + "\n" + inner + "\n" + rest[end+3:])
	}
}

// firstBalancedObject returns the first balanced {...} substring,
// ignoring braces inside JSON string literals (with escape handling).
func firstBalancedObject(s string) (string, bool) {
	start := -1
	depth := 0
	inStr := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			if start < 0 {
				start = i
			}
			depth++
		case '}':
			if start < 0 {
				continue
			}
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}

func validate[T any](v T) bool {
	switch t := any(v).(type) {
	case View:
		if t.Qualification == "" || t.SuggestedReply == "" || t.DecisiveInsight == "" ||
			t.TradeoffOrObjection == "" || t.DependsOn == "" || t.Fallback == "" {
			return false
		}
		return true
	case Judge:
		if t.Qualification == "" || t.RecommendedReply == "" || t.Why == "" ||
			t.AcceptedCost == "" || t.ChangeCourseIf == "" {
			return false
		}
		return true
	case AbsoluteGrade:
		for _, score := range t.Scores {
			if score < 0 || score > 4 {
				return false
			}
		}
		return true
	default:
		return true
	}
}
