package rule

import (
	"fmt"
	"net/url"
	"strings"
)

// Validate performs a server-side sanity check on a rule map.
func Validate(rule map[string]any) error {
	name, _ := rule["name"].(string)
	if name == "" {
		return fmt.Errorf("rule name is required")
	}
	entry, _ := rule["entry"].(string)
	if entry == "" {
		return fmt.Errorf("rule entry is required")
	}
	if _, err := url.Parse(entry); err != nil {
		return fmt.Errorf("rule entry must be a valid URL: %w", err)
	}
	if !strings.HasPrefix(entry, "http://") && !strings.HasPrefix(entry, "https://") {
		return fmt.Errorf("rule entry must be http(s) URL")
	}
	steps, ok := rule["steps"].([]any)
	if !ok || len(steps) == 0 {
		return fmt.Errorf("rule steps must be a non-empty array")
	}
	return nil
}
