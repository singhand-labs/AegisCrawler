package config

import (
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestLoadProductionLLMPolicy_EnforcedPrimaryRouteIsSecretFree(t *testing.T) {
	env := map[string]string{
		"LLM_POLICY_MODE":                    "enforced",
		"LLM_PRIMARY_PROVIDER":               "aliyun",
		"LLM_PRIMARY_ADAPTER":                "openai",
		"LLM_PRIMARY_MODEL":                  "qwen-test",
		"LLM_PRIMARY_BASE_URL":               "https://provider.example.test/v1",
		"LLM_PRIMARY_API_KEY":                "synthetic-primary-key",
		"LLM_PRIMARY_REQUEST_TIMEOUT":        "30s",
		"LLM_PRIMARY_TEMPERATURE":            "0",
		"LLM_PRIMARY_STRICT_TOOL_OUTPUT":     "true",
		"LLM_PRIMARY_ENABLE_THINKING":        "false",
		"LLM_PRIMARY_INPUT_USD_PER_MILLION":  "0.14",
		"LLM_PRIMARY_OUTPUT_USD_PER_MILLION": "0.28",
		"LLM_PRIMARY_MAX_INPUT_TOKENS":       "4096",
		"LLM_PRIMARY_MAX_OUTPUT_TOKENS":      "512",
		"LLM_PRIMARY_PRICE_REVISION":         "test-v1",
		"LLM_GLOBAL_MAX_REQUEST_USD":         "0.60",
		"LLM_GLOBAL_DAILY_BUDGET_USD":        "3.00",
		"LLM_WORKSPACE_BUDGETS_JSON":         `{"default":{"maxRequestUSD":"0.60","dailyBudgetUSD":"3.00"}}`,
		"LLM_REQUIREMENT_MAX_ATTEMPTS":       "2",
		"LLM_DSL_GENERATION_MAX_ATTEMPTS":    "3",
		"LLM_DSL_MAX_REPAIRS":                "1",
		"LLM_SELECTOR_MAX_REPAIRS":           "1",
	}

	policy, err := LoadProductionLLMPolicy(true, func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Primary.Provider != "aliyun" || policy.Primary.Adapter != "openai" {
		t.Fatalf("unexpected primary route: %+v", policy.Primary)
	}
	if policy.Primary.InputUSDPerMillion != USDNanos(140_000_000) {
		t.Fatalf("input price = %d, want 140000000", policy.Primary.InputUSDPerMillion)
	}
	if policy.RequirementMaxAttempts != 2 ||
		policy.DSLGenerationMaxAttempts != 3 ||
		policy.DSLMaxRepairs != 1 ||
		policy.SelectorMaxRepairs != 1 {
		t.Fatalf("unexpected automatic limits: %+v", policy)
	}
	if policy.Fingerprint == "" || strings.Contains(policy.Fingerprint, env["LLM_PRIMARY_API_KEY"]) {
		t.Fatalf("fingerprint leaked a secret or was empty: %q", policy.Fingerprint)
	}
}

func TestLoadProductionLLMPolicy_EnforcedRejectsConflictingOrUnknownConfiguration(t *testing.T) {
	for name, mutate := range map[string]func(map[string]string){
		"legacy retry": func(env map[string]string) {
			env["LLM_MAX_RETRIES"] = "1"
		},
		"unknown workspace field": func(env map[string]string) {
			env["LLM_WORKSPACE_BUDGETS_JSON"] = "{\"default\":{\"maxRequestUSD\":\"0.60\",\"dailyBudgetUSD\":\"3.00\",\"unexpected\":\"x\"}}"
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := validEnforcedPolicyEnv()
			mutate(env)
			if _, err := LoadProductionLLMPolicy(true, func(key string) (string, bool) {
				value, ok := env[key]
				return value, ok
			}); err == nil {
				t.Fatal("expected enforced policy rejection")
			}
		})
	}
}

func TestLoadProductionLLMPolicy_EnforcedRejectsPartialFallbackRoute(t *testing.T) {
	env := validEnforcedPolicyEnv()
	env["LLM_FALLBACK_MODEL"] = "qwen-fallback"

	if _, err := LoadProductionLLMPolicy(true, func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}); err == nil {
		t.Fatal("expected partial fallback route to be rejected")
	}
}

func TestLoadProductionLLMPolicy_EnforcedRejectsPartialFallbackOutputCapDialect(t *testing.T) {
	env := validEnforcedPolicyEnv()
	env["LLM_FALLBACK_OUTPUT_CAP_DIALECT"] = "max_completion_tokens"

	if _, err := LoadProductionLLMPolicy(true, mapLookup(env)); err == nil {
		t.Fatal("output-cap-only fallback route was not rejected as partial")
	}
}

func TestLoadProductionLLMPolicy_EnforcedThinkingRequiresAliyunCompletionCap(t *testing.T) {
	for name, mutate := range map[string]func(map[string]string){
		"thinking without explicit dialect": func(env map[string]string) {
			env["LLM_PRIMARY_ENABLE_THINKING"] = "true"
		},
		"thinking with answer-only cap": func(env map[string]string) {
			env["LLM_PRIMARY_ENABLE_THINKING"] = "true"
			env["LLM_PRIMARY_OUTPUT_CAP_DIALECT"] = "max_tokens"
		},
		"unknown dialect": func(env map[string]string) {
			env["LLM_PRIMARY_OUTPUT_CAP_DIALECT"] = "provider_magic"
		},
		"non Aliyun endpoint": func(env map[string]string) {
			env["LLM_PRIMARY_OUTPUT_CAP_DIALECT"] = "max_completion_tokens"
		},
		"cap cannot absorb tolerance": func(env map[string]string) {
			env["LLM_PRIMARY_ENABLE_THINKING"] = "true"
			env["LLM_PRIMARY_OUTPUT_CAP_DIALECT"] = "max_completion_tokens"
			env["LLM_PRIMARY_BASE_URL"] = "https://dashscope.aliyuncs.com/compatible-mode/v1"
			env["LLM_PRIMARY_MAX_OUTPUT_TOKENS"] = "10"
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := validEnforcedPolicyEnv()
			mutate(env)
			if _, err := LoadProductionLLMPolicy(true, mapLookup(env)); err == nil {
				t.Fatal("expected enforced output-cap contract rejection")
			}
		})
	}

	env := validEnforcedPolicyEnv()
	env["LLM_PRIMARY_ENABLE_THINKING"] = "true"
	env["LLM_PRIMARY_OUTPUT_CAP_DIALECT"] = "max_completion_tokens"
	env["LLM_PRIMARY_BASE_URL"] = "https://dashscope.aliyuncs.com/compatible-mode/v1"
	env["LLM_PRIMARY_MAX_OUTPUT_TOKENS"] = "11"
	env["LLM_PRIMARY_PROVIDER"] = "operator-defined-label"
	policy, err := LoadProductionLLMPolicy(true, mapLookup(env))
	if err != nil {
		t.Fatalf("valid Aliyun completion cap was rejected: %v", err)
	}
	if policy.Primary.OutputCapDialect != OutputCapDialectMaxCompletionTokens {
		t.Fatalf("output-cap dialect = %q", policy.Primary.OutputCapDialect)
	}
}

func TestIsAliyunOpenAICompatibleBaseURL(t *testing.T) {
	for _, test := range []struct {
		name string
		url  string
		want bool
	}{
		{name: "China shared endpoint", url: "https://dashscope.aliyuncs.com/compatible-mode/v1", want: true},
		{name: "international shared endpoint", url: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", want: true},
		{name: "US shared endpoint", url: "https://dashscope-us.aliyuncs.com/compatible-mode/v1", want: true},
		{name: "workspace endpoint", url: "https://workspace-id.maas.aliyuncs.com/compatible-mode/v1", want: true},
		{name: "explicit HTTPS port", url: "https://dashscope.aliyuncs.com:443/compatible-mode/v1", want: true},
		{name: "wrong provider", url: "https://provider.example.test/compatible-mode/v1"},
		{name: "deceptive shared hostname", url: "https://dashscope.aliyuncs.com.example.test/compatible-mode/v1"},
		{name: "bare workspace suffix", url: "https://maas.aliyuncs.com/compatible-mode/v1"},
		{name: "wrong path", url: "https://dashscope.aliyuncs.com/v1"},
		{name: "nonstandard port", url: "https://dashscope.aliyuncs.com:8443/compatible-mode/v1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isAliyunOpenAICompatibleBaseURL(test.url); got != test.want {
				t.Fatalf("isAliyunOpenAICompatibleBaseURL(%q) = %t, want %t", test.url, got, test.want)
			}
		})
	}
}

func TestLoadProductionLLMPolicy_OutputCapDialectChangesFingerprint(t *testing.T) {
	defaultEnv := validEnforcedPolicyEnv()
	defaultEnv["LLM_PRIMARY_BASE_URL"] = "https://dashscope.aliyuncs.com/compatible-mode/v1"
	defaultPolicy, err := LoadProductionLLMPolicy(true, mapLookup(defaultEnv))
	if err != nil {
		t.Fatal(err)
	}

	completionEnv := validEnforcedPolicyEnv()
	completionEnv["LLM_PRIMARY_BASE_URL"] = "https://dashscope.aliyuncs.com/compatible-mode/v1"
	completionEnv["LLM_PRIMARY_OUTPUT_CAP_DIALECT"] = "max_completion_tokens"
	completionPolicy, err := LoadProductionLLMPolicy(true, mapLookup(completionEnv))
	if err != nil {
		t.Fatal(err)
	}
	if defaultPolicy.Fingerprint == completionPolicy.Fingerprint {
		t.Fatal("output-cap dialect did not change the immutable policy fingerprint")
	}
}

func TestLoadProductionLLMPolicy_EnforcedRejectsAllLegacyRouteSources(t *testing.T) {
	for key, value := range map[string]string{
		"LLM_PROVIDER_CONFIGS":            `{"legacy":{"provider":"openai"}}`,
		"LLM_PROVIDER":                    "legacy",
		"LLM_API_KEY":                     "synthetic-legacy-key",
		"LLM_BASE_URL":                    "https://legacy.example.test/v1",
		"LLM_MODEL":                       "legacy-model",
		"LLM_TEMPERATURE":                 "0.2",
		"LLM_REQUEST_TIMEOUT":             "60s",
		"LLM_MAX_INPUT_TOKENS":            "8192",
		"LLM_MAX_OUTPUT_TOKENS":           "1024",
		"LLM_OPENAI_ENABLE_THINKING":      "true",
		"LLM_OPENAI_STRICT_TOOL_OUTPUT":   "true",
		"LLM_JOB_MAX_ATTEMPTS":            "3",
		"LLM_DSL_SELECTOR_REPAIR_ENABLED": "true",
		"LLM_DAILY_COST_BUDGET":           "10",
	} {
		t.Run(key, func(t *testing.T) {
			env := validEnforcedPolicyEnv()
			env[key] = value
			if _, err := LoadProductionLLMPolicy(true, mapLookup(env)); err == nil {
				t.Fatalf("%s was accepted in enforced mode", key)
			}
		})
	}
}

func TestLoadProductionLLMPolicy_EnforcedRejectsInvalidPolicyInputs(t *testing.T) {
	for name, mutate := range map[string]func(map[string]string){
		"missing input rate": func(env map[string]string) {
			delete(env, "LLM_PRIMARY_INPUT_USD_PER_MILLION")
		},
		"missing revision": func(env map[string]string) {
			delete(env, "LLM_PRIMARY_PRICE_REVISION")
		},
		"missing model": func(env map[string]string) {
			delete(env, "LLM_PRIMARY_MODEL")
		},
		"missing key": func(env map[string]string) {
			delete(env, "LLM_PRIMARY_API_KEY")
		},
		"malformed workspace budget": func(env map[string]string) {
			env["LLM_WORKSPACE_BUDGETS_JSON"] = "{"
		},
		"same provider labels": func(env map[string]string) {
			copyPrimaryAsFallback(env)
			env["LLM_FALLBACK_PROVIDER"] = env["LLM_PRIMARY_PROVIDER"]
		},
		"unsupported Anthropic strict output": func(env map[string]string) {
			env["LLM_PRIMARY_ADAPTER"] = "anthropic"
			env["LLM_PRIMARY_STRICT_TOOL_OUTPUT"] = "true"
		},
		"unsupported Anthropic thinking": func(env map[string]string) {
			env["LLM_PRIMARY_ADAPTER"] = "anthropic"
			env["LLM_PRIMARY_ENABLE_THINKING"] = "true"
		},
		"DeepSeek strict output requires beta endpoint": func(env map[string]string) {
			env["LLM_PRIMARY_BASE_URL"] = "https://api.deepseek.com/v1"
		},
		"non loopback HTTP endpoint": func(env map[string]string) {
			env["LLM_PRIMARY_BASE_URL"] = "http://provider.example.test/v1"
		},
		"endpoint query": func(env map[string]string) {
			env["LLM_PRIMARY_BASE_URL"] = "https://provider.example.test/v1?token=synthetic"
		},
		"endpoint fragment": func(env map[string]string) {
			env["LLM_PRIMARY_BASE_URL"] = "https://provider.example.test/v1#fragment"
		},
		"endpoint user info": func(env map[string]string) {
			env["LLM_PRIMARY_BASE_URL"] = "https://synthetic@provider.example.test/v1"
		},
		"too much USD precision": func(env map[string]string) {
			env["LLM_GLOBAL_MAX_REQUEST_USD"] = "0.0000000001"
		},
		"USD overflow": func(env map[string]string) {
			env["LLM_GLOBAL_MAX_REQUEST_USD"] = "9223372037"
		},
		"positive legacy retries": func(env map[string]string) {
			env["LLM_MAX_RETRIES"] = "1"
		},
		"legacy API key": func(env map[string]string) {
			env["LLM_API_KEY"] = "synthetic-legacy-key"
		},
		"requirement attempts too low": func(env map[string]string) {
			env["LLM_REQUIREMENT_MAX_ATTEMPTS"] = "0"
		},
		"requirement attempts too high": func(env map[string]string) {
			env["LLM_REQUIREMENT_MAX_ATTEMPTS"] = "4"
		},
		"generation attempts too low": func(env map[string]string) {
			env["LLM_DSL_GENERATION_MAX_ATTEMPTS"] = "0"
		},
		"generation attempts too high": func(env map[string]string) {
			env["LLM_DSL_GENERATION_MAX_ATTEMPTS"] = "4"
		},
		"repairs too low": func(env map[string]string) {
			env["LLM_DSL_MAX_REPAIRS"] = "-1"
		},
		"repairs too high": func(env map[string]string) {
			env["LLM_DSL_MAX_REPAIRS"] = "3"
		},
		"selector repairs too low": func(env map[string]string) {
			env["LLM_SELECTOR_MAX_REPAIRS"] = "-1"
		},
		"selector repairs too high": func(env map[string]string) {
			env["LLM_SELECTOR_MAX_REPAIRS"] = "2"
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := validEnforcedPolicyEnv()
			mutate(env)
			if _, err := LoadProductionLLMPolicy(true, mapLookup(env)); err == nil {
				t.Fatal("expected enforced policy rejection")
			}
		})
	}
}

func TestLoadProductionLLMPolicy_EnforcedCanonicalizesRouteAndPreservesFallback(t *testing.T) {
	env := validEnforcedPolicyEnv()
	env["LLM_PRIMARY_BASE_URL"] = "https://provider.example.test/v1/"
	copyPrimaryAsFallback(env)
	env["LLM_FALLBACK_PROVIDER"] = "anthropic-secondary"
	env["LLM_FALLBACK_ADAPTER"] = "anthropic"
	env["LLM_FALLBACK_MODEL"] = "claude-test"
	env["LLM_FALLBACK_STRICT_TOOL_OUTPUT"] = "false"

	policy, err := LoadProductionLLMPolicy(true, mapLookup(env))
	if err != nil {
		t.Fatal(err)
	}
	if policy.Primary.BaseURL != "https://provider.example.test/v1" {
		t.Fatalf("primary endpoint = %q", policy.Primary.BaseURL)
	}
	if policy.Fallback == nil || policy.Fallback.Provider != "anthropic-secondary" || policy.Fallback.Model != "claude-test" {
		t.Fatalf("fallback route = %+v", policy.Fallback)
	}
}

func TestParseUSDNanos(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want USDNanos
		ok   bool
	}{
		{raw: "0", want: 0, ok: true},
		{raw: "0.000000001", want: 1, ok: true},
		{raw: "0.14", want: 140_000_000, ok: true},
		{raw: "1", want: 1_000_000_000, ok: true},
		{raw: "1.000000000", want: 1_000_000_000, ok: true},
		{raw: "-0.1"}, {raw: "1e-1"}, {raw: ".1"}, {raw: "1."}, {raw: "0.0000000001"}, {raw: "9223372037"}, {raw: "9223372036.999999999"},
	} {
		t.Run(test.raw, func(t *testing.T) {
			got, err := parseUSDNanos(test.raw)
			if test.ok {
				if err != nil || got != test.want {
					t.Fatalf("parseUSDNanos(%q) = %d, %v; want %d, nil", test.raw, got, err, test.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("parseUSDNanos(%q) = %d, want error", test.raw, got)
			}
		})
	}
}

func mapLookup(env map[string]string) EnvLookup {
	return func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}
}

func copyPrimaryAsFallback(env map[string]string) {
	for _, name := range []string{
		"PROVIDER", "ADAPTER", "MODEL", "BASE_URL", "API_KEY", "REQUEST_TIMEOUT",
		"TEMPERATURE", "STRICT_TOOL_OUTPUT", "ENABLE_THINKING", "INPUT_USD_PER_MILLION",
		"OUTPUT_USD_PER_MILLION", "MAX_INPUT_TOKENS", "MAX_OUTPUT_TOKENS",
		"OUTPUT_CAP_DIALECT", "PRICE_REVISION",
	} {
		env["LLM_FALLBACK_"+name] = env["LLM_PRIMARY_"+name]
	}
}

func validEnforcedPolicyEnv() map[string]string {
	return map[string]string{
		"LLM_POLICY_MODE":                    "enforced",
		"LLM_PRIMARY_PROVIDER":               "aliyun",
		"LLM_PRIMARY_ADAPTER":                "openai",
		"LLM_PRIMARY_MODEL":                  "qwen-test",
		"LLM_PRIMARY_BASE_URL":               "https://provider.example.test/v1",
		"LLM_PRIMARY_API_KEY":                "synthetic-primary-key",
		"LLM_PRIMARY_REQUEST_TIMEOUT":        "30s",
		"LLM_PRIMARY_TEMPERATURE":            "0",
		"LLM_PRIMARY_STRICT_TOOL_OUTPUT":     "true",
		"LLM_PRIMARY_ENABLE_THINKING":        "false",
		"LLM_PRIMARY_INPUT_USD_PER_MILLION":  "0.14",
		"LLM_PRIMARY_OUTPUT_USD_PER_MILLION": "0.28",
		"LLM_PRIMARY_MAX_INPUT_TOKENS":       "4096",
		"LLM_PRIMARY_MAX_OUTPUT_TOKENS":      "512",
		"LLM_PRIMARY_PRICE_REVISION":         "test-v1",
		"LLM_GLOBAL_MAX_REQUEST_USD":         "0.60",
		"LLM_GLOBAL_DAILY_BUDGET_USD":        "3.00",
		"LLM_WORKSPACE_BUDGETS_JSON":         "{\"default\":{\"maxRequestUSD\":\"0.60\",\"dailyBudgetUSD\":\"3.00\"}}",
		"LLM_REQUIREMENT_MAX_ATTEMPTS":       "2",
		"LLM_DSL_GENERATION_MAX_ATTEMPTS":    "3",
		"LLM_DSL_MAX_REPAIRS":                "1",
		"LLM_SELECTOR_MAX_REPAIRS":           "1",
	}
}

func TestWarnIfLegacyPolicyMode(t *testing.T) {
	// legacy mode (explicit): warning must fire.
	t.Run("legacy", func(t *testing.T) {
		core, observed := observer.New(zap.WarnLevel)
		logger := zap.New(core)
		WarnIfLegacyPolicyMode(func(key string) (string, bool) {
			if key == "LLM_POLICY_MODE" {
				return "legacy", true
			}
			return "", false
		}, logger)
		entries := observed.All()
		if len(entries) != 1 {
			t.Fatalf("expected 1 warning entry, got %d: %+v", len(entries), entries)
		}
		msg := strings.ToLower(entries[0].Message)
		if !strings.Contains(msg, "legacy llm policy mode is deprecated") {
			t.Errorf("expected deprecation warning, got: %q", entries[0].Message)
		}
		// Should recommend enforced mode either in the message or in a field.
		hasEnforced := strings.Contains(msg, "enforced")
		for _, f := range entries[0].Context {
			if f.String == "enforced" || strings.Contains(strings.ToLower(f.String), "enforced") {
				hasEnforced = true
			}
		}
		if !hasEnforced {
			t.Errorf("expected warning to recommend enforced mode, message=%q fields=%+v", entries[0].Message, entries[0].Context)
		}
	})

	// Unset env var: treated as legacy; warning must fire.
	t.Run("unset", func(t *testing.T) {
		core, observed := observer.New(zap.WarnLevel)
		logger := zap.New(core)
		WarnIfLegacyPolicyMode(func(key string) (string, bool) {
			return "", false
		}, logger)
		if len(observed.All()) != 1 {
			t.Fatalf("expected 1 warning entry when env unset, got %d", len(observed.All()))
		}
	})

	// Blank value: treated as legacy; warning must fire.
	t.Run("blank", func(t *testing.T) {
		core, observed := observer.New(zap.WarnLevel)
		logger := zap.New(core)
		WarnIfLegacyPolicyMode(func(key string) (string, bool) {
			if key == "LLM_POLICY_MODE" {
				return "  ", true
			}
			return "", false
		}, logger)
		if len(observed.All()) != 1 {
			t.Fatalf("expected 1 warning entry when env blank, got %d", len(observed.All()))
		}
	})

	// Enforced: warning must NOT fire.
	t.Run("enforced_silent", func(t *testing.T) {
		core, observed := observer.New(zap.WarnLevel)
		logger := zap.New(core)
		WarnIfLegacyPolicyMode(func(key string) (string, bool) {
			if key == "LLM_POLICY_MODE" {
				return "enforced", true
			}
			return "", false
		}, logger)
		if len(observed.All()) != 0 {
			t.Errorf("expected no warning in enforced mode, got: %+v", observed.All())
		}
	})
}
