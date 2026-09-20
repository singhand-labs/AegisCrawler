package redact

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

var secretPatterns = []*regexp.Regexp{
	// HTTP credential headers may contain schemes, whitespace, cookie
	// attributes, and multiple values. Redact the complete header line so a
	// token is never left behind after replacing only "Bearer".
	regexp.MustCompile(`(?im)\b(authorization|proxy-authorization|cookie|set-cookie)\s*[:=]\s*[^\r\n]+`),
	// Key-value style secrets (password, token, api key, authorization header).
	regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|api[_-]?key|authorization)\s*[:=]\s*["']?[^"'\s<>]+["']?`),
	// Bank card numbers (16-19 digits).
	regexp.MustCompile(`\b\d{16,19}\b`),
	// Email addresses.
	regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`),
}

var jsonLikeKeyPattern = regexp.MustCompile(
	`(?i)(?:\\?["'])?((?:[A-Za-z0-9_-]|\\u[0-9a-f]{4})+)(?:\\?["'])?\s*:`,
)

const redactedPlaceholder = "[REDACTED]"

// Recording recursively redacts likely secrets from DOM snapshots and input values.
// Attribute names, tag names, and structural keys are preserved so selector
// inference is not affected.
func Recording(recording map[string]any) map[string]any {
	out := deepCopy(recording)
	walkRedact(out)
	return out
}

// Any defensively redacts a generic JSON-compatible diagnostic or artifact.
// Unlike Recording, it also treats credential-like field names as sensitive.
func Any(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var copied any
	if err := json.Unmarshal(encoded, &copied); err != nil {
		return nil
	}
	if text, ok := copied.(string); ok {
		return redactString(text)
	}
	walkRedactAny(copied)
	return copied
}

// String defensively redacts a provider-controlled string. JSON objects and
// arrays embedded inside strings are decoded and walked recursively before the
// regular expression fallback runs, preventing quoted credential keys from
// bypassing field-name redaction.
func String(value string) string {
	return redactString(value)
}

func walkRedactAny(value any) {
	switch current := value.(type) {
	case map[string]any:
		for key, item := range current {
			if isCredentialField(key) {
				current[key] = redactedPlaceholder
				continue
			}
			if text, ok := item.(string); ok {
				current[key] = redactString(text)
				continue
			}
			walkRedactAny(item)
		}
	case []any:
		for index, item := range current {
			if text, ok := item.(string); ok {
				current[index] = redactString(text)
				continue
			}
			walkRedactAny(item)
		}
	}
}

func isCredentialField(key string) bool {
	normalized := strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(key))
	// Token accounting fields describe quantities, not credentials. Keeping
	// these explicit exceptions avoids destroying provider audit metadata while
	// still redacting singular token values and credential-bearing fields.
	switch normalized {
	case "inputtokens", "outputtokens", "totaltokens", "maxtokens":
		return false
	}
	for _, marker := range []string{"password", "passwd", "pwd", "secret", "token", "apikey", "authorization", "cookie", "setcookie"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func walkRedact(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if isSensitiveField(k) {
				x[k] = redactValue(val)
				continue
			}
			walkRedact(val)
		}
	case []any:
		for _, item := range x {
			walkRedact(item)
		}
	}
}

func isSensitiveField(k string) bool {
	lower := strings.ToLower(k)
	switch lower {
	case "value", "text", "outerhtml", "innerhtml", "innertext", "textcontent":
		return true
	}
	return false
}

// redactValue recursively redacts strings inside a sensitive value while
// preserving structure (maps and slices).
func redactValue(v any) any {
	switch x := v.(type) {
	case string:
		return redactString(x)
	case map[string]any:
		for k, val := range x {
			x[k] = redactValue(val)
		}
		return x
	case []any:
		for i, item := range x {
			x[i] = redactValue(item)
		}
		return x
	default:
		return v
	}
}

func redactString(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var decoded any
		if json.Unmarshal([]byte(trimmed), &decoded) == nil {
			before, _ := json.Marshal(decoded)
			walkRedactAny(decoded)
			if encoded, err := json.Marshal(decoded); err == nil {
				if bytes.Equal(before, encoded) {
					return s
				}
				return string(encoded)
			}
		}
	}
	s = redactBalancedJSONFragments(s)
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, redactedPlaceholder)
	}
	return redactCredentialAssignments(s)
}

func redactBalancedJSONFragments(value string) string {
	type replacement struct {
		start int
		end   int
		value []byte
	}
	replacements := []replacement{}
	stack := []byte{}
	start := -1
	inString := false
	escaped := false
	for index := 0; index < len(value); index++ {
		current := value[index]
		if start < 0 {
			if current == '{' || current == '[' {
				start = index
				stack = append(stack[:0], current)
			}
			continue
		}
		if inString {
			switch {
			case escaped:
				escaped = false
			case current == '\\':
				escaped = true
			case current == '"':
				inString = false
			}
			continue
		}
		switch current {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, current)
		case '}', ']':
			open := stack[len(stack)-1]
			if (open == '{' && current != '}') || (open == '[' && current != ']') {
				start = -1
				stack = stack[:0]
				continue
			}
			stack = stack[:len(stack)-1]
			if len(stack) != 0 {
				continue
			}
			end := index + 1
			var decoded any
			if json.Unmarshal([]byte(value[start:end]), &decoded) == nil {
				before, _ := json.Marshal(decoded)
				walkRedactAny(decoded)
				if after, err := json.Marshal(decoded); err == nil && !bytes.Equal(before, after) {
					replacements = append(replacements, replacement{start: start, end: end, value: after})
				}
			}
			start = -1
		}
	}
	if len(replacements) == 0 {
		return value
	}
	var output strings.Builder
	output.Grow(len(value))
	cursor := 0
	for _, item := range replacements {
		output.WriteString(value[cursor:item.start])
		output.Write(item.value)
		cursor = item.end
	}
	output.WriteString(value[cursor:])
	return output.String()
}

func redactCredentialAssignments(value string) string {
	matches := jsonLikeKeyPattern.FindAllStringSubmatchIndex(value, -1)
	if len(matches) == 0 {
		return value
	}
	var output strings.Builder
	cursor := 0
	changed := false
	for _, match := range matches {
		if len(match) < 4 || match[2] < 0 || match[3] < 0 ||
			match[0] < cursor || !isCredentialField(decodeJSONKey(value[match[2]:match[3]])) {
			continue
		}
		valueStart := match[1]
		for valueStart < len(value) && (value[valueStart] == ' ' || value[valueStart] == '\t') {
			valueStart++
		}
		valueEnd := credentialValueEnd(value, valueStart)
		if valueEnd <= valueStart {
			continue
		}
		if !changed {
			output.Grow(len(value))
		}
		output.WriteString(value[cursor:valueStart])
		output.WriteString(`"[REDACTED]"`)
		cursor = valueEnd
		changed = true
	}
	if !changed {
		return value
	}
	output.WriteString(value[cursor:])
	return output.String()
}

func decodeJSONKey(key string) string {
	if !strings.Contains(key, `\u`) {
		return key
	}
	var decoded string
	if err := json.Unmarshal([]byte(`"`+key+`"`), &decoded); err != nil {
		return key
	}
	return decoded
}

func credentialValueEnd(value string, start int) int {
	if start >= len(value) {
		return start
	}
	switch value[start] {
	case '"', '\'':
		quote := value[start]
		escaped := false
		for index := start + 1; index < len(value); index++ {
			current := value[index]
			switch {
			case escaped:
				escaped = false
			case current == '\\':
				escaped = true
			case current == quote:
				return index + 1
			case current == '\r' || current == '\n':
				return index
			}
		}
		return len(value)
	case '{', '[':
		stack := []byte{value[start]}
		inString := false
		escaped := false
		for index := start + 1; index < len(value); index++ {
			current := value[index]
			if inString {
				switch {
				case escaped:
					escaped = false
				case current == '\\':
					escaped = true
				case current == '"':
					inString = false
				}
				continue
			}
			switch current {
			case '"':
				inString = true
			case '{', '[':
				stack = append(stack, current)
			case '}', ']':
				open := stack[len(stack)-1]
				if (open == '{' && current != '}') || (open == '[' && current != ']') {
					return index
				}
				stack = stack[:len(stack)-1]
				if len(stack) == 0 {
					return index + 1
				}
			}
		}
		return len(value)
	default:
		for index := start; index < len(value); index++ {
			switch value[index] {
			case ',', '}', ']', '\r', '\n':
				return index
			}
		}
		return len(value)
	}
}

func deepCopy(v map[string]any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		// Fallback to a shallow copy; this should not happen for valid recordings.
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = val
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		out = map[string]any{}
	}
	return out
}
