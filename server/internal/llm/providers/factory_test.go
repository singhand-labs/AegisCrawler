package providers

import (
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
)

func TestBuild_OpenAI(t *testing.T) {
	p, err := Build("primary", config.ProviderConfig{
		Provider: "openai",
		APIKey:   "key",
		BaseURL:  "https://api.openai.com/v1",
		Timeout:  30 * time.Second,
	})
	if err != nil {
		t.Fatalf("build openai failed: %v", err)
	}
	if p.Name() != "primary" {
		t.Fatalf("unexpected name %q", p.Name())
	}
}

func TestBuild_Anthropic(t *testing.T) {
	p, err := Build("fallback", config.ProviderConfig{
		Provider: "anthropic",
		APIKey:   "key",
		BaseURL:  "https://api.anthropic.com/v1",
		Timeout:  30 * time.Second,
	})
	if err != nil {
		t.Fatalf("build anthropic failed: %v", err)
	}
	if p.Name() != "fallback" {
		t.Fatalf("unexpected name %q", p.Name())
	}
}

func TestBuild_DefaultTimeout(t *testing.T) {
	p, err := Build("primary", config.ProviderConfig{
		Provider: "openai",
		APIKey:   "key",
	})
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	op := p.(*OpenAIProvider)
	if op.client.Timeout != 60*time.Second {
		t.Fatalf("expected default timeout 60s, got %s", op.client.Timeout)
	}
}

func TestBuild_DefaultProviderOpenAI(t *testing.T) {
	p, err := Build("primary", config.ProviderConfig{
		APIKey:  "key",
		BaseURL: "https://api.openai.com/v1",
	})
	if err != nil {
		t.Fatalf("build default failed: %v", err)
	}
	if p.Name() != "primary" {
		t.Fatalf("unexpected name %q", p.Name())
	}
}

func TestBuild_Unsupported(t *testing.T) {
	_, err := Build("primary", config.ProviderConfig{
		Provider: "unknown",
	})
	if err == nil {
		t.Fatal("expected error for unsupported provider")
	}
}

func TestBuild_ModelPassedToOpenAI(t *testing.T) {
	disableThinking := false
	enableStrictToolOutput := true
	p, err := Build("primary", config.ProviderConfig{
		Provider:               "openai",
		APIKey:                 "key",
		BaseURL:                "https://api.deepseek.com/beta",
		Model:                  "custom-model",
		OpenAIEnableThinking:   &disableThinking,
		OpenAIStrictToolOutput: &enableStrictToolOutput,
	})
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	op := p.(*OpenAIProvider)
	if op.model != "custom-model" {
		t.Fatalf("expected model custom-model, got %q", op.model)
	}
	if op.enableThinking == nil || *op.enableThinking {
		t.Fatalf("expected explicit non-thinking mode, got %v", op.enableThinking)
	}
	if !op.strictToolOutput {
		t.Fatal("expected strict tool output")
	}
}

func TestBuild_PassesOutputCapDialectToOpenAI(t *testing.T) {
	p, err := Build("primary", config.ProviderConfig{
		Provider:         "openai",
		APIKey:           "key",
		BaseURL:          "https://dashscope.aliyuncs.com/compatible-mode/v1",
		OutputCapDialect: config.OutputCapDialectMaxCompletionTokens,
	})
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	op := p.(*OpenAIProvider)
	if op.outputCapDialect != config.OutputCapDialectMaxCompletionTokens {
		t.Fatalf("output-cap dialect = %q", op.outputCapDialect)
	}
}

func TestBuild_DeepSeekStrictToolOutputRequiresBetaBaseURL(t *testing.T) {
	enable := true
	_, err := Build("primary", config.ProviderConfig{
		Provider:               "openai",
		BaseURL:                "https://api.deepseek.com/v1",
		OpenAIStrictToolOutput: &enable,
	})
	if err == nil || !strings.Contains(err.Error(), "https://api.deepseek.com/beta") {
		t.Fatalf("expected DeepSeek beta URL failure, got %v", err)
	}
}

func TestBuild_ModelPassedToAnthropic(t *testing.T) {
	p, err := Build("fallback", config.ProviderConfig{
		Provider: "anthropic",
		APIKey:   "key",
		Model:    "claude-3-opus",
	})
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	ap := p.(*AnthropicProvider)
	if ap.model != "claude-3-opus" {
		t.Fatalf("expected model claude-3-opus, got %q", ap.model)
	}
}
