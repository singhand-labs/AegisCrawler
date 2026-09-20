package providers

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
)

// Build creates a provider from a logical name and per-provider config.
func Build(name string, pc config.ProviderConfig) (llm.Provider, error) {
	if pc.Timeout <= 0 {
		pc.Timeout = 60 * time.Second
	}
	switch pc.Provider {
	case "openai", "":
		if pc.OpenAIStrictToolOutput != nil && *pc.OpenAIStrictToolOutput {
			if err := validateStrictToolBaseURL(pc.BaseURL); err != nil {
				return nil, err
			}
		}
		provider := NewOpenAIProvider(name, pc.APIKey, pc.BaseURL, pc.Model, pc.Timeout, pc.Temperature, !pc.DisableTemperatureCompatibilityRetry)
		provider.enableThinking = pc.OpenAIEnableThinking
		provider.outputCapDialect = pc.OutputCapDialect
		provider.strictToolOutput = pc.OpenAIStrictToolOutput != nil && *pc.OpenAIStrictToolOutput
		return provider, nil
	case "anthropic":
		provider := NewAnthropicProvider(name, pc.APIKey, pc.BaseURL, pc.Model, pc.Timeout, pc.Temperature, !pc.DisableTemperatureCompatibilityRetry)
		// The route thinking flag is adapter-neutral: an explicit false makes
		// reasoning-first models disable thinking for deterministic output.
		if pc.OpenAIEnableThinking != nil && !*pc.OpenAIEnableThinking {
			provider.DisableThinking()
		}
		return provider, nil
	case "loopback":
		return NewLoopbackProvider(name, pc.LoopbackArchivePath, pc.Model)
	default:
		return nil, fmt.Errorf("unsupported provider %q", pc.Provider)
	}
}

func validateStrictToolBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return nil
	}
	if strings.EqualFold(parsed.Hostname(), "api.deepseek.com") &&
		strings.TrimRight(parsed.EscapedPath(), "/") != "/beta" {
		return fmt.Errorf(
			"openai strict tool output for api.deepseek.com requires baseURL https://api.deepseek.com/beta",
		)
	}
	return nil
}
