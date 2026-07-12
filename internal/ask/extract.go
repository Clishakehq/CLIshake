package ask

import (
	"encoding/json"
	"errors"
)

// ExtractPlan finds and parses the first JSON object in raw model output.
// It tolerates surrounding prose and ```json fences by scanning for a
// balanced top-level '{' ... '}' span (string-aware, so braces inside
// quoted values don't confuse it) and attempting to unmarshal each
// candidate it finds until one parses into a non-empty Plan.
func ExtractPlan(raw string) (Plan, error) {
	sawEmptyPlan := false

	for i := 0; i < len(raw); i++ {
		if raw[i] != '{' {
			continue
		}
		end, ok := matchBrace(raw, i)
		if !ok {
			continue
		}
		candidate := raw[i : end+1]

		var p Plan
		if err := json.Unmarshal([]byte(candidate), &p); err != nil {
			continue
		}
		if len(p.Commands) == 0 {
			sawEmptyPlan = true
			continue
		}
		return p, nil
	}

	if sawEmptyPlan {
		return Plan{}, errors.New("model output contained plan JSON with no commands")
	}
	return Plan{}, errors.New("no valid plan JSON found in model output")
}

// matchBrace returns the index of the '}' that matches the '{' at start,
// scanning string literals (with backslash-escape awareness) so braces
// inside quoted strings are not counted.
func matchBrace(s string, start int) (int, bool) {
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}
