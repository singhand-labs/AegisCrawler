package recording

import (
	"encoding/json"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

const (
	Redacted   = "[REDACTED]"
	RemovedURL = "[REMOVED_URL]"

	maxSanitizationAttributeNames = 64
	maxSanitizationAttributeBytes = 128
)

type semanticSanitizationEvidence struct {
	markupAltered   bool
	contentOmitted  bool
	alteredAttrs    map[string]struct{}
	allAttrsAltered bool
}

var sensitiveTextPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|api[_-]?key|authorization|cookie)\s*[:=]\s*["']?[^"'\s<>;&]+["']?`),
	regexp.MustCompile(`(?i)bearer\s+[a-z0-9._~+/=-]+`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`),
	regexp.MustCompile(`\b\d{16,19}\b`),
	regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`),
}

var urlCredentialsPattern = regexp.MustCompile(`://[^/@\s]+:[^/@\s]+@`)

type SanitizationReport struct {
	RedactedValues int `json:"redactedValues"`
	RemovedFields  int `json:"removedFields"`
}

func semanticNode(value map[string]any) bool {
	nodeType, _ := value["type"].(string)
	return nodeType == "element" || nodeType == "text"
}

func failClosedSemanticSanitization() semanticSanitizationEvidence {
	return semanticSanitizationEvidence{
		markupAltered: true, contentOmitted: true, allAttrsAltered: true,
	}
}

func normalizeSemanticSanitization(value any) semanticSanitizationEvidence {
	if value == nil {
		return failClosedSemanticSanitization()
	}
	raw, ok := value.(map[string]any)
	if !ok || len(raw) == 0 {
		return failClosedSemanticSanitization()
	}
	evidence := semanticSanitizationEvidence{}
	for key, item := range raw {
		switch key {
		case "markupAltered":
			marker, valid := item.(bool)
			if !valid || !marker {
				return failClosedSemanticSanitization()
			}
			evidence.markupAltered = true
		case "contentOmitted":
			marker, valid := item.(bool)
			if !valid || !marker {
				return failClosedSemanticSanitization()
			}
			evidence.contentOmitted = true
			evidence.markupAltered = true
		case "alteredAttributes":
			names, valid := sanitizationAttributeNames(item)
			if !valid {
				return failClosedSemanticSanitization()
			}
			for _, name := range names {
				evidence.addAlteredAttribute(name)
			}
			evidence.markupAltered = true
		default:
			return failClosedSemanticSanitization()
		}
	}
	if !evidence.markupAltered && !evidence.contentOmitted &&
		len(evidence.alteredAttrs) == 0 && !evidence.allAttrsAltered {
		return failClosedSemanticSanitization()
	}
	return evidence
}

func sanitizationAttributeNames(value any) ([]string, bool) {
	var values []any
	switch typed := value.(type) {
	case []any:
		values = typed
	case []string:
		values = make([]any, len(typed))
		for index, item := range typed {
			values[index] = item
		}
	default:
		return nil, false
	}
	if len(values) == 0 || len(values) > maxSanitizationAttributeNames {
		return nil, false
	}
	result := make([]string, 0, len(values))
	for _, item := range values {
		name, ok := item.(string)
		if !ok {
			return nil, false
		}
		normalized, ok := normalizeSanitizationAttributeName(name)
		if !ok {
			return nil, false
		}
		result = append(result, normalized)
	}
	return result, true
}

func normalizeSanitizationAttributeName(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "*" {
		return value, true
	}
	if value == "" || len(value) > maxSanitizationAttributeBytes {
		return "", false
	}
	for _, char := range value {
		if char <= ' ' || char == '"' || char == '\'' || char == '<' ||
			char == '>' || char == '=' || char == '`' {
			return "", false
		}
	}
	return value, true
}

func (e *semanticSanitizationEvidence) addAlteredAttribute(value string) {
	if e.allAttrsAltered {
		return
	}
	name, ok := normalizeSanitizationAttributeName(value)
	if !ok || name == "*" {
		e.allAttrsAltered = true
		e.alteredAttrs = nil
		return
	}
	if e.alteredAttrs == nil {
		e.alteredAttrs = map[string]struct{}{}
	}
	e.alteredAttrs[name] = struct{}{}
	if len(e.alteredAttrs) > maxSanitizationAttributeNames {
		e.allAttrsAltered = true
		e.alteredAttrs = nil
	}
}

func (e *semanticSanitizationEvidence) merge(other semanticSanitizationEvidence) {
	e.markupAltered = e.markupAltered || other.markupAltered
	e.contentOmitted = e.contentOmitted || other.contentOmitted
	if e.contentOmitted {
		e.markupAltered = true
	}
	if other.allAttrsAltered {
		e.allAttrsAltered = true
		e.alteredAttrs = nil
	} else {
		for name := range other.alteredAttrs {
			e.addAlteredAttribute(name)
		}
	}
	if e.allAttrsAltered || len(e.alteredAttrs) > 0 {
		e.markupAltered = true
	}
}

func (e semanticSanitizationEvidence) jsonValue() map[string]any {
	result := map[string]any{}
	if e.markupAltered || e.contentOmitted || e.allAttrsAltered || len(e.alteredAttrs) > 0 {
		result["markupAltered"] = true
	}
	if e.contentOmitted {
		result["contentOmitted"] = true
	}
	if e.allAttrsAltered {
		result["alteredAttributes"] = []string{"*"}
	} else if len(e.alteredAttrs) > 0 {
		names := make([]string, 0, len(e.alteredAttrs))
		for name := range e.alteredAttrs {
			names = append(names, name)
		}
		sort.Strings(names)
		result["alteredAttributes"] = names
	}
	return result
}

// Sanitize performs the server's defensive redaction pass. It never mutates
// the caller's object and strips raw markup/style/handler fields that do not
// belong in a semantic DOM archive.
func Sanitize(input map[string]any) (map[string]any, SanitizationReport) {
	copy := deepCopyMap(input)
	report := SanitizationReport{}
	value := sanitizeValue(copy, false, &report)
	out, _ := value.(map[string]any)
	if out == nil {
		out = map[string]any{}
	}
	return out, report
}

func sanitizeValue(value any, forceRedact bool, report *SanitizationReport) any {
	switch typed := value.(type) {
	case map[string]any:
		isSemanticNode := semanticNode(typed)
		evidence := semanticSanitizationEvidence{}
		if isSemanticNode {
			if raw, present := typed["sanitization"]; present {
				evidence = normalizeSemanticSanitization(raw)
			}
			if typed["type"] == "text" {
				if _, present := typed["rendered"]; present {
					// rendered is negative element evidence. A text-node marker
					// is malformed and would otherwise be discarded when the
					// semantic tree is converted for selector validation.
					evidence.markupAltered = true
					evidence.contentOmitted = true
				}
			}
		}
		hiddenInput := isHiddenInput(typed)
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if isSemanticNode && normalizeKey(key) == "sanitization" {
				continue
			}
			if isUnsafeSemanticField(key) {
				report.RemovedFields++
				if isSemanticNode {
					evidence.markupAltered = true
					if unsafeSemanticFieldOmitsContent(key) {
						evidence.contentOmitted = true
					}
				}
				continue
			}
			if normalizeKey(key) == "attributes" {
				clean, changed, altered := sanitizeSemanticAttributes(item, hiddenInput, report)
				out[key] = clean
				if isSemanticNode && changed {
					evidence.markupAltered = true
					for _, name := range altered {
						evidence.addAlteredAttribute(name)
					}
				}
				continue
			}
			if isSemanticNode && normalizeKey(key) == "children" {
				clean, omitted := sanitizeSemanticChildren(item, report)
				out[key] = clean
				if omitted {
					evidence.markupAltered = true
					evidence.contentOmitted = true
				}
				continue
			}
			redact := forceRedact || isSensitiveKey(key) || (hiddenInput && isValueField(key))
			clean := sanitizeValue(item, redact, report)
			out[key] = clean
			if isSemanticNode && !reflect.DeepEqual(clean, item) {
				evidence.markupAltered = true
				if normalizeKey(key) == "text" {
					evidence.contentOmitted = true
				}
			}
		}
		if isSemanticNode {
			if provenance := evidence.jsonValue(); len(provenance) > 0 {
				out["sanitization"] = provenance
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			if node, ok := item.(map[string]any); ok && isUnsafeSemanticNode(node) {
				report.RemovedFields++
				continue
			}
			out = append(out, sanitizeValue(item, forceRedact, report))
		}
		return out
	case string:
		if forceRedact {
			report.RedactedValues++
			return Redacted
		}
		redacted := typed
		for _, pattern := range sensitiveTextPatterns {
			redacted = pattern.ReplaceAllString(redacted, Redacted)
		}
		if redacted != typed {
			report.RedactedValues++
		}
		return redacted
	default:
		if forceRedact && value != nil {
			report.RedactedValues++
			return Redacted
		}
		return value
	}
}

func isUnsafeSemanticNode(node map[string]any) bool {
	tagName, _ := node["tagName"].(string)
	switch strings.ToLower(strings.TrimSpace(tagName)) {
	case "script", "style", "noscript", "template", "link", "meta":
		return true
	default:
		return false
	}
}

func isSensitiveKey(key string) bool {
	normalized := normalizeKey(key)
	for _, candidate := range []string{
		"password", "passwd", "pwd", "secret", "token", "accesstoken", "refreshtoken",
		"apikey", "authorization", "cookie", "setcookie", "credential", "credentials",
		"clientsecret", "privatekey",
	} {
		if normalized == candidate || strings.HasSuffix(normalized, candidate) {
			return true
		}
	}
	return false
}

func isUnsafeSemanticField(key string) bool {
	normalized := normalizeKey(key)
	if normalized == "outerhtml" || normalized == "innerhtml" || normalized == "rawhtml" || normalized == "srcdoc" || normalized == "style" || normalized == "stylesheet" {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(key))
	if !strings.HasPrefix(lower, "on") {
		return false
	}
	event := strings.TrimPrefix(lower, "on")
	for _, prefix := range []string{
		"click", "load", "error", "submit", "change", "input", "focus", "blur", "key",
		"mouse", "pointer", "touch", "drag", "drop", "scroll", "wheel", "animation",
		"transition", "before", "after", "message", "storage", "popstate", "hashchange",
	} {
		if strings.HasPrefix(event, prefix) {
			return true
		}
	}
	return false
}

func unsafeSemanticFieldOmitsContent(key string) bool {
	switch normalizeKey(key) {
	case "outerhtml", "innerhtml", "rawhtml", "srcdoc":
		return true
	default:
		return false
	}
}

func isHiddenInput(node map[string]any) bool {
	for key, value := range node {
		normalized := normalizeKey(key)
		if text, ok := value.(string); ok {
			if normalized == "type" && (strings.EqualFold(text, "password") || strings.EqualFold(text, "hidden")) {
				return true
			}
			if (normalized == "name" || normalized == "id" || normalized == "arialabel" || normalized == "autocomplete") && isSensitiveIdentity(text) {
				return true
			}
		}
	}
	attributes, ok := node["attributes"].([]any)
	if !ok {
		return false
	}
	for _, item := range attributes {
		attribute, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := attribute["name"].(string)
		value, _ := attribute["value"].(string)
		normalized := normalizeKey(name)
		if normalized == "type" && (strings.EqualFold(value, "password") || strings.EqualFold(value, "hidden")) {
			return true
		}
		if (normalized == "name" || normalized == "id" || normalized == "arialabel" || normalized == "autocomplete") && isSensitiveIdentity(value) {
			return true
		}
	}
	return false
}

func sanitizeSemanticChildren(value any, report *SanitizationReport) (any, bool) {
	children, ok := value.([]any)
	if !ok {
		return sanitizeValue(value, false, report), true
	}
	out := make([]any, 0, len(children))
	omitted := false
	for _, item := range children {
		node, valid := item.(map[string]any)
		if !valid || !semanticChildConvertible(node) || isUnsafeSemanticNode(node) {
			report.RemovedFields++
			omitted = true
			continue
		}
		clean, cleanOK := sanitizeValue(node, false, report).(map[string]any)
		if !cleanOK || !semanticChildConvertible(clean) {
			report.RemovedFields++
			omitted = true
			continue
		}
		out = append(out, clean)
	}
	return out, omitted
}

func semanticChildConvertible(node map[string]any) bool {
	nodeType, _ := node["type"].(string)
	switch nodeType {
	case "text":
		return true
	case "element":
		tagName, ok := node["tagName"].(string)
		return ok && strings.TrimSpace(tagName) != ""
	default:
		return false
	}
}

func sanitizeSemanticAttributes(
	value any,
	sensitiveElement bool,
	report *SanitizationReport,
) (any, bool, []string) {
	attributes, ok := value.([]any)
	if !ok {
		return sanitizeValue(value, false, report), true, []string{"*"}
	}
	out := make([]any, 0, len(attributes))
	changed := false
	altered := map[string]struct{}{}
	for _, item := range attributes {
		attribute, ok := item.(map[string]any)
		if !ok {
			out = append(out, sanitizeValue(item, false, report))
			changed = true
			altered["*"] = struct{}{}
			continue
		}
		name, _ := attribute["name"].(string)
		if isUnsafeSemanticAttribute(name) {
			report.RemovedFields++
			changed = true
			continue
		}
		clean := make(map[string]any, len(attribute))
		for key, raw := range attribute {
			if normalizeKey(key) != "value" {
				sanitized := sanitizeValue(raw, false, report)
				clean[key] = sanitized
				if !reflect.DeepEqual(sanitized, raw) {
					changed = true
					if normalizeKey(key) == "name" {
						altered["*"] = struct{}{}
					}
				}
				continue
			}
			text, isText := raw.(string)
			if !isText {
				sanitized := sanitizeValue(raw, sensitiveElement || isSensitiveKey(name), report)
				clean[key] = sanitized
				if !reflect.DeepEqual(sanitized, raw) {
					changed = true
					altered[name] = struct{}{}
				}
				continue
			}
			normalizedName := normalizeKey(name)
			if isSensitiveKey(name) || (sensitiveElement && isSensitiveSemanticValueAttribute(normalizedName)) {
				if text != "" {
					report.RedactedValues++
					clean[key] = Redacted
					changed = true
					altered[name] = struct{}{}
				} else {
					clean[key] = text
				}
				continue
			}
			if isURLAttribute(normalizedName) {
				sanitized := sanitizeSemanticURL(text, report)
				clean[key] = sanitized
				if sanitized != text {
					changed = true
					altered[name] = struct{}{}
				}
				continue
			}
			sanitized := sanitizeValue(text, false, report)
			clean[key] = sanitized
			if !reflect.DeepEqual(sanitized, text) {
				changed = true
				altered[name] = struct{}{}
			}
		}
		out = append(out, clean)
	}
	names := make([]string, 0, len(altered))
	for name := range altered {
		names = append(names, name)
	}
	sort.Strings(names)
	return out, changed, names
}

func isSensitiveIdentity(value string) bool {
	normalized := normalizeKey(value)
	if normalized == "username" || normalized == "login" || normalized == "currentpassword" || normalized == "newpassword" || normalized == "onetimecode" || normalized == "ccnumber" || normalized == "cccsc" {
		return true
	}
	for _, candidate := range []string{
		"password", "passwd", "pwd", "secret", "token", "apikey", "authorization", "cookie",
		"credential", "cardnumber", "cvv", "cvc", "ssn",
	} {
		if strings.Contains(normalized, candidate) {
			return true
		}
	}
	return false
}

func isUnsafeSemanticAttribute(name string) bool {
	normalized := normalizeKey(name)
	return normalized == "style" || normalized == "srcdoc" || isUnsafeSemanticField(name)
}

func isSensitiveSemanticValueAttribute(normalizedName string) bool {
	switch normalizedName {
	case "value", "placeholder", "title", "arialabel":
		return true
	default:
		return false
	}
}

func isURLAttribute(normalizedName string) bool {
	switch normalizedName {
	case "href", "src", "action":
		return true
	default:
		return false
	}
}

func sanitizeSemanticURL(value string, report *SanitizationReport) string {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if strings.HasPrefix(trimmed, "data:") || strings.HasPrefix(trimmed, "blob:") || strings.HasPrefix(trimmed, "javascript:") {
		report.RedactedValues++
		return RemovedURL
	}
	withoutCredentials := urlCredentialsPattern.ReplaceAllString(value, "://"+Redacted+"@")
	if withoutCredentials != value {
		report.RedactedValues++
	}
	clean, _ := sanitizeValue(withoutCredentials, false, report).(string)
	return clean
}

func isValueField(key string) bool {
	switch normalizeKey(key) {
	case "value", "text", "innertext", "textcontent":
		return true
	default:
		return false
	}
}

func normalizeKey(key string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, strings.ToLower(key))
}

func marshalCanonical(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}
