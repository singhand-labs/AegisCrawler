package intent

import (
	"strings"
)

var unsafePatterns = []string{
	"密码", "password", "窃取", "盗取", "hack", "crack", "暴力破解",
	"登录凭证", "cookie", "token", "信用卡", "身份证号", "手机号",
}

// FilterCandidates removes candidates that describe unsafe or malicious intents.
func FilterCandidates(candidates []Candidate) []Candidate {
	out := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		text := strings.ToLower(c.Label + " " + c.Description)
		if containsAny(text, unsafePatterns) {
			continue
		}
		out = append(out, c)
	}
	return out
}

func containsAny(text string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(text, p) {
			return true
		}
	}
	return false
}
