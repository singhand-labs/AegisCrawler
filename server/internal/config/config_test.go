package config

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

func TestProviderConfig_UnmarshalJSON_StringTimeout(t *testing.T) {
	var pc ProviderConfig
	if err := json.Unmarshal([]byte(`{"provider":"openai","apiKey":"k","baseURL":"http://x","model":"gpt-4o","timeout":"30s","openAIEnableThinking":false,"openAIStrictToolOutput":true}`), &pc); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if pc.Timeout != 30*time.Second {
		t.Fatalf("expected timeout 30s, got %s", pc.Timeout)
	}
	if pc.OpenAIEnableThinking == nil || *pc.OpenAIEnableThinking {
		t.Fatalf("expected explicit non-thinking mode, got %v", pc.OpenAIEnableThinking)
	}
	if pc.OpenAIStrictToolOutput == nil || !*pc.OpenAIStrictToolOutput {
		t.Fatalf("expected strict tool output, got %v", pc.OpenAIStrictToolOutput)
	}
}

func TestProviderConfig_UnmarshalJSON_LoopbackArchivePath(t *testing.T) {
	var pc ProviderConfig
	if err := json.Unmarshal([]byte(`{"provider":"loopback","loopbackArchivePath":"/tmp/loopback.json"}`), &pc); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if pc.Provider != "loopback" || pc.LoopbackArchivePath != "/tmp/loopback.json" {
		t.Fatalf("unexpected loopback config: %+v", pc)
	}
}

func TestProviderConfig_UnmarshalJSON_NumericTimeout(t *testing.T) {
	var pc ProviderConfig
	if err := json.Unmarshal([]byte(`{"provider":"openai","timeout":5000000000}`), &pc); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if pc.Timeout != 5*time.Second {
		t.Fatalf("expected timeout 5s, got %s", pc.Timeout)
	}
}

func TestProviderConfig_UnmarshalJSON_InvalidTimeout(t *testing.T) {
	var pc ProviderConfig
	err := json.Unmarshal([]byte(`{"timeout":"not-a-duration"}`), &pc)
	if err == nil {
		t.Fatal("expected error for invalid timeout")
	}
}

func TestConfig_LLMProviderConfigs_LogsMalformedJSON(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	t.Setenv("LLM_PROVIDER_CONFIGS", "not-json")
	t.Setenv("LLM_API_KEY", "legacy-key")

	cfg := Load()
	m := cfg.LLMProviderConfigs()

	if m["openai"].APIKey != "legacy-key" {
		t.Fatalf("expected legacy fallback config, got %+v", m)
	}
	if !strings.Contains(buf.String(), "LLM_PROVIDER_CONFIGS is malformed") {
		t.Fatalf("expected malformed JSON warning, got %q", buf.String())
	}
}

func TestConfig_LLMProviderConfigs_ParsesValidJSON(t *testing.T) {
	t.Setenv("LLM_PROVIDER_CONFIGS", `{"primary":{"provider":"openai","apiKey":"k1","model":"gpt-4o","timeout":"30s"}}`)
	t.Setenv("LLM_OPENAI_ENABLE_THINKING", "false")

	cfg := Load()
	m := cfg.LLMProviderConfigs()

	pc, ok := m["primary"]
	if !ok {
		t.Fatalf("expected primary config, got %+v", m)
	}
	if pc.APIKey != "k1" || pc.Model != "gpt-4o" || pc.Timeout != 30*time.Second {
		t.Fatalf("unexpected config: %+v", pc)
	}
	if pc.OpenAIEnableThinking == nil || *pc.OpenAIEnableThinking {
		t.Fatalf("expected global OpenAI thinking setting on named provider, got %v", pc.OpenAIEnableThinking)
	}
}

func TestConfig_LLMProviderConfigs_OptionalOpenAIThinkingMode(t *testing.T) {
	t.Setenv("LLM_OPENAI_ENABLE_THINKING", "false")
	cfg := Load()
	provider := cfg.LLMProviderConfigs()["openai"]
	if provider.OpenAIEnableThinking == nil || *provider.OpenAIEnableThinking {
		t.Fatalf("expected explicit non-thinking mode, got %v", provider.OpenAIEnableThinking)
	}

	t.Setenv("LLM_OPENAI_ENABLE_THINKING", "")
	cfg = Load()
	if provider := cfg.LLMProviderConfigs()["openai"]; provider.OpenAIEnableThinking != nil {
		t.Fatalf("thinking mode must remain unspecified by default, got %v", *provider.OpenAIEnableThinking)
	}
}

func TestConfig_LLMProviderConfigs_OptionalOpenAIStrictToolOutput(t *testing.T) {
	t.Setenv("LLM_OPENAI_STRICT_TOOL_OUTPUT", "true")
	cfg := Load()
	provider := cfg.LLMProviderConfigs()["openai"]
	if provider.OpenAIStrictToolOutput == nil || !*provider.OpenAIStrictToolOutput {
		t.Fatalf("expected strict tool output inheritance, got %v", provider.OpenAIStrictToolOutput)
	}

	t.Setenv("LLM_OPENAI_STRICT_TOOL_OUTPUT", "")
	cfg = Load()
	if provider := cfg.LLMProviderConfigs()["openai"]; provider.OpenAIStrictToolOutput != nil {
		t.Fatalf("strict tool output must remain unspecified by default, got %v", *provider.OpenAIStrictToolOutput)
	}
}

func TestConfig_OpenAIStrictToolOutputEnabledUsesNamedProvider(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "primary")
	t.Setenv("LLM_PROVIDER_CONFIGS", `{
		"primary":{
			"provider":"openai",
			"baseURL":"https://api.deepseek.com/beta",
			"openAIStrictToolOutput":true
		}
	}`)
	t.Setenv("LLM_OPENAI_STRICT_TOOL_OUTPUT", "")
	cfg := Load()
	if !cfg.OpenAIStrictToolOutputEnabled() {
		t.Fatal("named provider strict tool output was ignored")
	}
}

func TestConfig_LLM_AUDIT_PROMPTS_DefaultsToFalse(t *testing.T) {
	t.Setenv("LLM_AUDIT_PROMPTS", "")
	cfg := Load()
	if cfg.LLMAuditPrompts {
		t.Fatal("expected LLMAuditPrompts to default to false")
	}
}

func TestConfig_LLM_AUDIT_PROMPTS_ReadsEnv(t *testing.T) {
	t.Setenv("LLM_AUDIT_PROMPTS", "true")
	cfg := Load()
	if !cfg.LLMAuditPrompts {
		t.Fatal("expected LLMAuditPrompts to be true")
	}
}

func TestConfig_StrictQualificationLimitsAndDefaults(t *testing.T) {
	for _, key := range []string{
		"LLM_JOB_MAX_ATTEMPTS", "LLM_DSL_MAX_REPAIRS", "LLM_ALLOW_DEGRADED_FALLBACK",
		"LLM_TEMPERATURE_COMPATIBILITY_RETRY", "LLM_DSL_SELECTOR_REPAIR_ENABLED",
	} {
		t.Setenv(key, "")
	}
	defaults := Load()
	if defaults.DurableJobMaxAttempts() != 3 || defaults.DSLMaxRepairs() != 3 {
		t.Fatalf("unexpected durable workflow defaults: attempts=%d repairs=%d", defaults.DurableJobMaxAttempts(), defaults.DSLMaxRepairs())
	}
	if !defaults.AllowDegradedFallback() || !defaults.AllowTemperatureCompatibilityRetry() ||
		!defaults.DSLSelectorRepairEnabled() {
		t.Fatal("production compatibility defaults must remain enabled")
	}

	t.Setenv("LLM_JOB_MAX_ATTEMPTS", "1")
	t.Setenv("LLM_DSL_MAX_REPAIRS", "0")
	t.Setenv("LLM_ALLOW_DEGRADED_FALLBACK", "false")
	t.Setenv("LLM_TEMPERATURE_COMPATIBILITY_RETRY", "false")
	t.Setenv("LLM_DSL_SELECTOR_REPAIR_ENABLED", "false")
	strict := Load()
	if strict.DurableJobMaxAttempts() != 1 || strict.DSLMaxRepairs() != 0 {
		t.Fatalf("strict bounds not preserved: attempts=%d repairs=%d", strict.DurableJobMaxAttempts(), strict.DSLMaxRepairs())
	}
	if strict.AllowDegradedFallback() || strict.AllowTemperatureCompatibilityRetry() ||
		strict.DSLSelectorRepairEnabled() {
		t.Fatal("strict compatibility paths were not disabled")
	}
}

func TestConfig_DSLSelectorRepairSchedulingDefaultsAndExplicitValues(t *testing.T) {
	if !(&Config{}).DSLSelectorRepairEnabled() || !(*Config)(nil).DSLSelectorRepairEnabled() {
		t.Fatal("hand-built and nil configs must preserve automatic selector repair")
	}
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: "", want: true},
		{value: "true", want: true},
		{value: "false", want: false},
		{value: "invalid", want: true},
	} {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv("LLM_DSL_SELECTOR_REPAIR_ENABLED", test.value)
			if got := Load().DSLSelectorRepairEnabled(); got != test.want {
				t.Fatalf("selector repair enabled = %t, want %t", got, test.want)
			}
		})
	}
}

func TestConfig_StrictQualificationLimitsRejectOutOfRangeValues(t *testing.T) {
	for _, test := range []struct {
		attempts string
		repairs  string
	}{
		{attempts: "0", repairs: "-1"},
		{attempts: "4", repairs: "4"},
		{attempts: "invalid", repairs: "invalid"},
	} {
		t.Run(test.attempts+"/"+test.repairs, func(t *testing.T) {
			t.Setenv("LLM_JOB_MAX_ATTEMPTS", test.attempts)
			t.Setenv("LLM_DSL_MAX_REPAIRS", test.repairs)
			cfg := Load()
			if cfg.DurableJobMaxAttempts() != 3 || cfg.DSLMaxRepairs() != 3 {
				t.Fatalf("out-of-range values did not fall back: attempts=%d repairs=%d", cfg.DurableJobMaxAttempts(), cfg.DSLMaxRepairs())
			}
		})
	}
}

func TestConfig_ParsingHelpers(t *testing.T) {
	t.Setenv("MAX_RETRIES", "10")
	t.Setenv("MAX_REQUEST_BODY_BYTES", "1048576")
	t.Setenv("RATE_LIMIT_PER_SECOND", "50.5")
	t.Setenv("LEASE_DURATION", "2m")

	cfg := Load()
	if cfg.MaxRetries != 10 {
		t.Fatalf("expected MaxRetries 10, got %d", cfg.MaxRetries)
	}
	if cfg.MaxRequestBodyBytes != 1048576 {
		t.Fatalf("expected MaxRequestBodyBytes 1048576, got %d", cfg.MaxRequestBodyBytes)
	}
	if cfg.RateLimitPerSecond != 50.5 {
		t.Fatalf("expected RateLimitPerSecond 50.5, got %f", cfg.RateLimitPerSecond)
	}
	if cfg.LeaseDuration != 2*time.Minute {
		t.Fatalf("expected LeaseDuration 2m, got %s", cfg.LeaseDuration)
	}
}

func TestConfig_CapabilityFlagsDefaultOffAndReadEnvironment(t *testing.T) {
	for _, key := range []string{"FEATURE_RECORDING_V2", "FEATURE_WORKFLOW_V2", "FEATURE_WORKER_PROTOCOL_V2", "FEATURE_MCP", "MCP_RATE_LIMIT_PER_SECOND", "MCP_RATE_LIMIT_BURST"} {
		t.Setenv(key, "")
	}
	defaults := Load()
	if defaults.RecordingV2Enabled || defaults.WorkflowV2Enabled || defaults.WorkerProtocolV2Enabled || defaults.MCPEnabled {
		t.Fatalf("expected additive capabilities to default off: %+v", defaults)
	}
	if defaults.MCPRateLimitPerSecond != 10 || defaults.MCPRateLimitBurst != 20 {
		t.Fatalf("unexpected MCP rate-limit defaults: %+v", defaults)
	}

	t.Setenv("FEATURE_RECORDING_V2", "true")
	t.Setenv("FEATURE_WORKFLOW_V2", "true")
	t.Setenv("FEATURE_WORKER_PROTOCOL_V2", "true")
	t.Setenv("FEATURE_MCP", "true")
	t.Setenv("MCP_RATE_LIMIT_PER_SECOND", "2.5")
	t.Setenv("MCP_RATE_LIMIT_BURST", "7")
	enabled := Load()
	if !enabled.RecordingV2Enabled || !enabled.WorkflowV2Enabled || !enabled.WorkerProtocolV2Enabled || !enabled.MCPEnabled {
		t.Fatalf("expected capability environment flags to be enabled: %+v", enabled)
	}
	if enabled.MCPRateLimitPerSecond != 2.5 || enabled.MCPRateLimitBurst != 7 {
		t.Fatalf("unexpected configured MCP rate limits: %+v", enabled)
	}
}

func TestConfig_RecordingLimits(t *testing.T) {
	for _, key := range []string{"RECORDING_MAX_DURATION", "RECORDING_MAX_ACTIONS", "RECORDING_MAX_COMPRESSED_BYTES", "RECORDING_RETENTION"} {
		t.Setenv(key, "")
	}
	defaults := Load()
	if defaults.RecordingMaxDuration != 2*time.Hour || defaults.RecordingMaxActions != 500 || defaults.RecordingMaxCompressedBytes != 25*1024*1024 || defaults.RecordingRetention != 7*24*time.Hour {
		t.Fatalf("unexpected recording defaults: %+v", defaults)
	}

	t.Setenv("RECORDING_MAX_DURATION", "30m")
	t.Setenv("RECORDING_MAX_ACTIONS", "42")
	t.Setenv("RECORDING_MAX_COMPRESSED_BYTES", "2048")
	t.Setenv("RECORDING_RETENTION", "48h")
	configured := Load()
	if configured.RecordingMaxDuration != 30*time.Minute || configured.RecordingMaxActions != 42 || configured.RecordingMaxCompressedBytes != 2048 || configured.RecordingRetention != 48*time.Hour {
		t.Fatalf("unexpected configured recording limits: %+v", configured)
	}
}

func TestConfig_ParsingHelpers_FallBackOnInvalidValues(t *testing.T) {
	t.Setenv("MAX_RETRIES", "not-an-int")
	t.Setenv("MAX_REQUEST_BODY_BYTES", "not-an-int64")
	t.Setenv("RATE_LIMIT_PER_SECOND", "not-a-float")
	t.Setenv("LEASE_DURATION", "not-a-duration")
	t.Setenv("REQUIRE_SECURITY_KEYS", "not-a-bool")

	cfg := Load()
	if cfg.MaxRetries != 3 {
		t.Fatalf("expected default MaxRetries 3, got %d", cfg.MaxRetries)
	}
	if cfg.MaxRequestBodyBytes != 8*1024*1024 {
		t.Fatalf("expected default MaxRequestBodyBytes, got %d", cfg.MaxRequestBodyBytes)
	}
	if cfg.RateLimitPerSecond != 100 {
		t.Fatalf("expected default RateLimitPerSecond 100, got %f", cfg.RateLimitPerSecond)
	}
	if cfg.LeaseDuration != 60*time.Second {
		t.Fatalf("expected default LeaseDuration 60s, got %s", cfg.LeaseDuration)
	}
	if !cfg.RequireSecurityKeys {
		t.Fatal("expected default RequireSecurityKeys true")
	}
}

func TestProviderConfig_UnmarshalJSON_UnsupportedTimeoutType(t *testing.T) {
	var pc ProviderConfig
	err := json.Unmarshal([]byte(`{"timeout":["30s"]}`), &pc)
	if err == nil {
		t.Fatal("expected error for unsupported timeout type")
	}
}

func TestConfig_LLMProviderConfigs_FallbackProvider(t *testing.T) {
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("LLM_PROVIDER", "primary")
	t.Setenv("LLM_FALLBACK_PROVIDER", "fallback")
	t.Setenv("LLM_API_KEY", "shared-key")
	t.Setenv("LLM_BASE_URL", "http://localhost")
	t.Setenv("LLM_MODEL", "model-x")

	cfg := Load()
	m := cfg.LLMProviderConfigs()
	if m["primary"].APIKey != "shared-key" {
		t.Fatalf("expected primary config, got %+v", m["primary"])
	}
	if m["fallback"].APIKey != "shared-key" {
		t.Fatalf("expected fallback config with shared key, got %+v", m["fallback"])
	}
}

func TestConfig_EnforcedLLMPolicyUsesIndependentWorkflowLimits(t *testing.T) {
	setEnforcedPolicyEnvironment(t)

	cfg := Load()
	if err := cfg.ValidateLLMPolicy(); err != nil {
		t.Fatalf("ValidateLLMPolicy() error = %v", err)
	}
	policy := cfg.EnforcedLLMPolicy()
	if policy == nil || policy.Primary.Provider != "aliyun" {
		t.Fatalf("EnforcedLLMPolicy() = %+v, want primary aliyun", policy)
	}
	if cfg.RequirementMaxAttempts() != 2 || cfg.DSLGenerationMaxAttempts() != 3 ||
		cfg.DSLMaxRepairs() != 1 || !cfg.SelectorRepairEnabled() {
		t.Fatalf("unexpected enforced limits: requirement=%d generation=%d repairs=%d selector=%t",
			cfg.RequirementMaxAttempts(), cfg.DSLGenerationMaxAttempts(), cfg.DSLMaxRepairs(), cfg.SelectorRepairEnabled())
	}
	if cfg.AllowDegradedFallback() || cfg.AllowTemperatureCompatibilityRetry() {
		t.Fatal("enforced mode must disable degraded output and compatibility retries")
	}
}

func TestConfig_EnforcedLLMPolicyDefersInvalidStartupErrorToValidation(t *testing.T) {
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("LLM_POLICY_MODE", "enforced")

	if err := Load().ValidateLLMPolicy(); err == nil {
		t.Fatal("ValidateLLMPolicy() accepted missing enforced route")
	}
}

func TestConfig_ProviderRoutesUsesPolicySlotsAndProviderProvenance(t *testing.T) {
	setEnforcedPolicyEnvironment(t)
	for key, value := range map[string]string{
		"LLM_FALLBACK_PROVIDER":               "anthropic-secondary",
		"LLM_FALLBACK_ADAPTER":                "anthropic",
		"LLM_FALLBACK_MODEL":                  "claude-test",
		"LLM_FALLBACK_BASE_URL":               "https://fallback.example.test/v1",
		"LLM_FALLBACK_API_KEY":                "synthetic-fallback-key",
		"LLM_FALLBACK_REQUEST_TIMEOUT":        "45s",
		"LLM_FALLBACK_TEMPERATURE":            "0.1",
		"LLM_FALLBACK_STRICT_TOOL_OUTPUT":     "false",
		"LLM_FALLBACK_ENABLE_THINKING":        "false",
		"LLM_FALLBACK_INPUT_USD_PER_MILLION":  "0.20",
		"LLM_FALLBACK_OUTPUT_USD_PER_MILLION": "0.40",
		"LLM_FALLBACK_MAX_INPUT_TOKENS":       "8192",
		"LLM_FALLBACK_MAX_OUTPUT_TOKENS":      "1024",
		"LLM_FALLBACK_PRICE_REVISION":         "fallback-v1",
	} {
		t.Setenv(key, value)
	}

	routes := Load().ProviderRoutes()
	if len(routes) != 2 {
		t.Fatalf("ProviderRoutes() = %+v, want primary and fallback", routes)
	}
	primary := routes["primary"]
	if primary.Provider != "openai" || primary.ProviderLabel != "aliyun" ||
		primary.Model != "qwen-test" || primary.Timeout != 30*time.Second ||
		primary.OutputCapDialect != OutputCapDialectMaxTokens ||
		primary.OpenAIStrictToolOutput == nil || !*primary.OpenAIStrictToolOutput {
		t.Fatalf("primary route = %+v", primary)
	}
	fallback := routes["fallback"]
	if fallback.Provider != "anthropic" || fallback.ProviderLabel != "anthropic-secondary" ||
		fallback.Model != "claude-test" || fallback.Timeout != 45*time.Second ||
		fallback.DisableTemperatureCompatibilityRetry != true {
		t.Fatalf("fallback route = %+v", fallback)
	}
}

func setEnforcedPolicyEnvironment(t *testing.T) {
	t.Helper()
	for key, value := range validEnforcedPolicyEnv() {
		t.Setenv(key, value)
	}
	t.Setenv("LLM_ENABLED", "true")
	for _, key := range []string{
		"LLM_PROVIDER_CONFIGS", "LLM_PROVIDER", "LLM_FALLBACK_PROVIDER", "LLM_DAILY_COST_BUDGET",
		"LLM_API_KEY", "LLM_BASE_URL", "LLM_MODEL", "LLM_TEMPERATURE", "LLM_REQUEST_TIMEOUT",
		"LLM_MAX_INPUT_TOKENS", "LLM_MAX_OUTPUT_TOKENS", "LLM_OPENAI_ENABLE_THINKING",
		"LLM_OPENAI_STRICT_TOOL_OUTPUT", "LLM_JOB_MAX_ATTEMPTS", "LLM_DSL_SELECTOR_REPAIR_ENABLED",
		"LLM_MAX_RETRIES", "LLM_ALLOW_DEGRADED_FALLBACK", "LLM_TEMPERATURE_COMPATIBILITY_RETRY",
	} {
		t.Setenv(key, "")
	}
}
