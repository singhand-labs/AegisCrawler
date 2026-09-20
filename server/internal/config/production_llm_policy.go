package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

type EnvLookup func(string) (string, bool)

type LLMPolicyMode string

const (
	LLMPolicyModeLegacy   LLMPolicyMode = "legacy"
	LLMPolicyModeEnforced LLMPolicyMode = "enforced"

	productionPolicySchemaVersion = "v2"
	availabilityFallbackRule      = "one_hop:transport_or_timeout_or_http_408_429_5xx"

	// AliyunMaxCompletionTokensTolerance is the documented maximum difference
	// between max_completion_tokens and actual output usage. The wire cap is
	// reduced by this amount so the configured route cap remains hard.
	AliyunMaxCompletionTokensTolerance = 10
)

type USDNanos int64

type OutputCapDialect string

const (
	OutputCapDialectMaxTokens           OutputCapDialect = "max_tokens"
	OutputCapDialectMaxCompletionTokens OutputCapDialect = "max_completion_tokens"
)

type WorkspaceBudget struct {
	MaxRequestUSD  USDNanos
	DailyBudgetUSD USDNanos
}

type RoutePolicy struct {
	Slot                     string
	Provider                 string
	Adapter                  string
	Model                    string
	BaseURL                  string
	APIKey                   string
	PriceRevision            string
	RequestTimeout           time.Duration
	Temperature              float64
	StrictToolOutput         bool
	EnableThinking           bool
	OutputCapDialect         OutputCapDialect
	InputUSDPerMillion       USDNanos
	CachedInputUSDPerMillion USDNanos
	OutputUSDPerMillion      USDNanos
	MaxInputTokens           int
	MaxOutputTokens          int
}

type ProductionLLMPolicy struct {
	Mode                     LLMPolicyMode
	Primary                  RoutePolicy
	Fallback                 *RoutePolicy
	GlobalMaxRequestUSD      USDNanos
	GlobalDailyBudgetUSD     USDNanos
	WorkspaceBudgets         map[string]WorkspaceBudget
	RequirementMaxAttempts   int
	DSLGenerationMaxAttempts int
	DSLMaxRepairs            int
	SelectorMaxRepairs       int
	Fingerprint              string
}

func LoadProductionLLMPolicy(llmEnabled bool, lookup EnvLookup) (*ProductionLLMPolicy, error) {
	mode := LLMPolicyModeLegacy
	if raw, ok := lookup("LLM_POLICY_MODE"); ok && strings.TrimSpace(raw) != "" {
		mode = LLMPolicyMode(strings.ToLower(strings.TrimSpace(raw)))
	}
	if mode != LLMPolicyModeLegacy && mode != LLMPolicyModeEnforced {
		return nil, fmt.Errorf("LLM_POLICY_MODE must be legacy or enforced")
	}
	policy := &ProductionLLMPolicy{Mode: mode}
	if mode == LLMPolicyModeLegacy || !llmEnabled {
		return policy, nil
	}
	if err := rejectEnforcedLegacyConflict(lookup); err != nil {
		return nil, err
	}

	primary, err := loadRoutePolicy("PRIMARY", "primary", lookup)
	if err != nil {
		return nil, err
	}
	policy.Primary = primary
	fallbackConfigured := routePrefixConfigured("FALLBACK", lookup)
	if fallbackConfigured {
		if raw, ok := lookup("LLM_FALLBACK_PROVIDER"); !ok || strings.TrimSpace(raw) == "" {
			return nil, fmt.Errorf("LLM_FALLBACK_PROVIDER is required when any LLM_FALLBACK_* setting is configured")
		}
		fallback, err := loadRoutePolicy("FALLBACK", "fallback", lookup)
		if err != nil {
			return nil, err
		}
		if fallback.Provider == primary.Provider {
			return nil, fmt.Errorf("LLM_PRIMARY_PROVIDER and LLM_FALLBACK_PROVIDER must differ")
		}
		policy.Fallback = &fallback
	}

	if policy.GlobalMaxRequestUSD, err = requiredUSDNanos("LLM_GLOBAL_MAX_REQUEST_USD", lookup); err != nil {
		return nil, err
	}
	if policy.GlobalDailyBudgetUSD, err = requiredUSDNanos("LLM_GLOBAL_DAILY_BUDGET_USD", lookup); err != nil {
		return nil, err
	}
	if policy.WorkspaceBudgets, err = loadWorkspaceBudgets(lookup); err != nil {
		return nil, err
	}
	if policy.RequirementMaxAttempts, err = requiredBoundedInt("LLM_REQUIREMENT_MAX_ATTEMPTS", 1, 3, lookup); err != nil {
		return nil, err
	}
	if policy.DSLGenerationMaxAttempts, err = requiredBoundedInt("LLM_DSL_GENERATION_MAX_ATTEMPTS", 1, 3, lookup); err != nil {
		return nil, err
	}
	if policy.DSLMaxRepairs, err = requiredBoundedInt("LLM_DSL_MAX_REPAIRS", 0, 2, lookup); err != nil {
		return nil, err
	}
	if policy.SelectorMaxRepairs, err = requiredBoundedInt("LLM_SELECTOR_MAX_REPAIRS", 0, 1, lookup); err != nil {
		return nil, err
	}
	policy.Fingerprint, err = productionPolicyFingerprint(policy)
	if err != nil {
		return nil, err
	}
	return policy, nil
}

func routePrefixConfigured(prefix string, lookup EnvLookup) bool {
	for _, name := range []string{
		"PROVIDER", "ADAPTER", "MODEL", "BASE_URL", "API_KEY", "REQUEST_TIMEOUT",
		"TEMPERATURE", "STRICT_TOOL_OUTPUT", "ENABLE_THINKING", "INPUT_USD_PER_MILLION",
		"CACHED_INPUT_USD_PER_MILLION", "OUTPUT_USD_PER_MILLION", "MAX_INPUT_TOKENS",
		"MAX_OUTPUT_TOKENS", "OUTPUT_CAP_DIALECT", "PRICE_REVISION",
	} {
		if value, ok := lookup("LLM_" + prefix + "_" + name); ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func loadRoutePolicy(prefix, slot string, lookup EnvLookup) (RoutePolicy, error) {
	required := func(name string) (string, error) {
		return requiredEnv("LLM_"+prefix+"_"+name, lookup)
	}
	provider, err := required("PROVIDER")
	if err != nil {
		return RoutePolicy{}, err
	}
	adapter, err := required("ADAPTER")
	if err != nil {
		return RoutePolicy{}, err
	}
	if adapter != "openai" && adapter != "anthropic" {
		return RoutePolicy{}, fmt.Errorf("LLM_%s_ADAPTER must be openai or anthropic", prefix)
	}
	model, err := required("MODEL")
	if err != nil {
		return RoutePolicy{}, err
	}
	baseURL, err := required("BASE_URL")
	if err != nil {
		return RoutePolicy{}, err
	}
	baseURL, err = canonicalProviderBaseURL(baseURL)
	if err != nil {
		return RoutePolicy{}, fmt.Errorf("LLM_%s_BASE_URL: %w", prefix, err)
	}
	apiKey, err := required("API_KEY")
	if err != nil {
		return RoutePolicy{}, err
	}
	timeoutRaw, err := required("REQUEST_TIMEOUT")
	if err != nil {
		return RoutePolicy{}, err
	}
	timeout, err := time.ParseDuration(timeoutRaw)
	if err != nil || timeout <= 0 {
		return RoutePolicy{}, fmt.Errorf("LLM_%s_REQUEST_TIMEOUT must be positive", prefix)
	}
	temperatureRaw, err := required("TEMPERATURE")
	if err != nil {
		return RoutePolicy{}, err
	}
	temperature, err := strconv.ParseFloat(temperatureRaw, 64)
	if err != nil || math.IsNaN(temperature) || math.IsInf(temperature, 0) {
		return RoutePolicy{}, fmt.Errorf("LLM_%s_TEMPERATURE must be finite", prefix)
	}
	strict, err := requiredBool("LLM_"+prefix+"_STRICT_TOOL_OUTPUT", lookup)
	if err != nil {
		return RoutePolicy{}, err
	}
	thinking, err := requiredBool("LLM_"+prefix+"_ENABLE_THINKING", lookup)
	if err != nil {
		return RoutePolicy{}, err
	}
	if adapter == "anthropic" && strict {
		return RoutePolicy{}, fmt.Errorf("LLM_%s_STRICT_TOOL_OUTPUT is not supported by the anthropic adapter", prefix)
	}
	if adapter == "anthropic" && thinking {
		return RoutePolicy{}, fmt.Errorf("LLM_%s_ENABLE_THINKING is not supported by the anthropic adapter", prefix)
	}
	if adapter == "openai" && strict {
		parsedBaseURL, _ := url.Parse(baseURL)
		if strings.EqualFold(parsedBaseURL.Hostname(), "api.deepseek.com") && strings.TrimRight(parsedBaseURL.EscapedPath(), "/") != "/beta" {
			return RoutePolicy{}, fmt.Errorf("LLM_%s_BASE_URL must use /beta for strict tool output with api.deepseek.com", prefix)
		}
	}
	input, err := requiredUSDNanos("LLM_"+prefix+"_INPUT_USD_PER_MILLION", lookup)
	if err != nil {
		return RoutePolicy{}, err
	}
	cachedInput := input
	if value, ok := lookup("LLM_" + prefix + "_CACHED_INPUT_USD_PER_MILLION"); ok && strings.TrimSpace(value) != "" {
		cachedInput, err = parseUSDNanos(value)
		if err != nil || cachedInput <= 0 {
			return RoutePolicy{}, fmt.Errorf("LLM_%s_CACHED_INPUT_USD_PER_MILLION must be a positive USD amount", prefix)
		}
	}
	output, err := requiredUSDNanos("LLM_"+prefix+"_OUTPUT_USD_PER_MILLION", lookup)
	if err != nil {
		return RoutePolicy{}, err
	}
	maxInput, err := requiredBoundedInt("LLM_"+prefix+"_MAX_INPUT_TOKENS", 1, math.MaxInt, lookup)
	if err != nil {
		return RoutePolicy{}, err
	}
	maxOutput, err := requiredBoundedInt("LLM_"+prefix+"_MAX_OUTPUT_TOKENS", 1, math.MaxInt, lookup)
	if err != nil {
		return RoutePolicy{}, err
	}
	outputCapDialect := OutputCapDialectMaxTokens
	if raw, ok := lookup("LLM_" + prefix + "_OUTPUT_CAP_DIALECT"); ok && strings.TrimSpace(raw) != "" {
		outputCapDialect = OutputCapDialect(strings.ToLower(strings.TrimSpace(raw)))
	}
	switch outputCapDialect {
	case OutputCapDialectMaxTokens:
		if thinking {
			return RoutePolicy{}, fmt.Errorf(
				"LLM_%s_ENABLE_THINKING requires LLM_%s_OUTPUT_CAP_DIALECT=max_completion_tokens",
				prefix,
				prefix,
			)
		}
	case OutputCapDialectMaxCompletionTokens:
		if adapter != "openai" {
			return RoutePolicy{}, fmt.Errorf(
				"LLM_%s_OUTPUT_CAP_DIALECT=max_completion_tokens requires the openai adapter",
				prefix,
			)
		}
		if !isAliyunOpenAICompatibleBaseURL(baseURL) {
			return RoutePolicy{}, fmt.Errorf(
				"LLM_%s_OUTPUT_CAP_DIALECT=max_completion_tokens requires an Alibaba Model Studio OpenAI-compatible endpoint",
				prefix,
			)
		}
		if maxOutput <= AliyunMaxCompletionTokensTolerance {
			return RoutePolicy{}, fmt.Errorf(
				"LLM_%s_MAX_OUTPUT_TOKENS must be greater than %d for max_completion_tokens",
				prefix,
				AliyunMaxCompletionTokensTolerance,
			)
		}
	default:
		return RoutePolicy{}, fmt.Errorf(
			"LLM_%s_OUTPUT_CAP_DIALECT must be max_tokens or max_completion_tokens",
			prefix,
		)
	}
	revision, err := required("PRICE_REVISION")
	if err != nil {
		return RoutePolicy{}, err
	}
	return RoutePolicy{
		Slot: slot, Provider: provider, Adapter: adapter, Model: model,
		BaseURL: baseURL, APIKey: apiKey, PriceRevision: revision,
		RequestTimeout: timeout, Temperature: temperature,
		StrictToolOutput: strict, EnableThinking: thinking, OutputCapDialect: outputCapDialect,
		InputUSDPerMillion: input, CachedInputUSDPerMillion: cachedInput,
		OutputUSDPerMillion: output, MaxInputTokens: maxInput, MaxOutputTokens: maxOutput,
	}, nil
}

func isAliyunOpenAICompatibleBaseURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if strings.TrimRight(parsed.EscapedPath(), "/") != "/compatible-mode/v1" {
		return false
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "dashscope.aliyuncs.com", "dashscope-intl.aliyuncs.com", "dashscope-us.aliyuncs.com":
		return true
	default:
		return strings.HasSuffix(host, ".maas.aliyuncs.com")
	}
}

func requiredEnv(key string, lookup EnvLookup) (string, error) {
	value, ok := lookup(key)
	value = strings.TrimSpace(value)
	if !ok || value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

func requiredBool(key string, lookup EnvLookup) (bool, error) {
	raw, err := requiredEnv(key, lookup)
	if err != nil {
		return false, err
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be boolean", key)
	}
	return value, nil
}

func requiredBoundedInt(key string, minimum, maximum int, lookup EnvLookup) (int, error) {
	raw, err := requiredEnv(key, lookup)
	if err != nil {
		return 0, err
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", key, minimum, maximum)
	}
	return value, nil
}

func requiredUSDNanos(key string, lookup EnvLookup) (USDNanos, error) {
	raw, err := requiredEnv(key, lookup)
	if err != nil {
		return 0, err
	}
	value, err := parseUSDNanos(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive USD amount", key)
	}
	return value, nil
}

func parseUSDNanos(raw string) (USDNanos, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "-") || strings.ContainsAny(raw, "eE") {
		return 0, fmt.Errorf("invalid USD amount")
	}
	parts := strings.Split(raw, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, fmt.Errorf("invalid USD amount")
	}
	if len(parts) == 2 && (parts[1] == "" || len(parts[1]) > 9) {
		return 0, fmt.Errorf("invalid USD precision")
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 || whole > math.MaxInt64/1_000_000_000 {
		return 0, fmt.Errorf("USD amount overflows")
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	fraction += strings.Repeat("0", 9-len(fraction))
	fractionNanos, err := strconv.ParseInt(fraction, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid USD fraction")
	}
	wholeNanos := whole * 1_000_000_000
	if fractionNanos > math.MaxInt64-wholeNanos {
		return 0, fmt.Errorf("USD amount overflows")
	}
	return USDNanos(wholeNanos + fractionNanos), nil
}

func canonicalProviderBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		return "", fmt.Errorf("must be an absolute URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("must not include credentials, query, or fragment")
	}
	hostname := parsed.Hostname()
	loopback := hostname == "localhost" || (net.ParseIP(hostname) != nil && net.ParseIP(hostname).IsLoopback())
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback) {
		return "", fmt.Errorf("must use HTTPS except for loopback")
	}
	parsed.Path = strings.TrimRight(parsed.EscapedPath(), "/")
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func loadWorkspaceBudgets(lookup EnvLookup) (map[string]WorkspaceBudget, error) {
	raw, err := requiredEnv("LLM_WORKSPACE_BUDGETS_JSON", lookup)
	if err != nil {
		return nil, err
	}
	var source map[string]map[string]string
	if err := json.Unmarshal([]byte(raw), &source); err != nil || len(source) == 0 {
		return nil, fmt.Errorf("LLM_WORKSPACE_BUDGETS_JSON must be a non-empty budget object")
	}
	budgets := make(map[string]WorkspaceBudget, len(source))
	for workspace, sourceBudget := range source {
		if strings.TrimSpace(workspace) == "" {
			return nil, fmt.Errorf("LLM_WORKSPACE_BUDGETS_JSON contains an empty workspace")
		}
		if len(sourceBudget) != 2 || sourceBudget["maxRequestUSD"] == "" || sourceBudget["dailyBudgetUSD"] == "" {
			return nil, fmt.Errorf("workspace %q budget must contain only maxRequestUSD and dailyBudgetUSD", workspace)
		}
		maxRequest, err := parseUSDNanos(sourceBudget["maxRequestUSD"])
		if err != nil || maxRequest <= 0 {
			return nil, fmt.Errorf("workspace %q maxRequestUSD must be positive", workspace)
		}
		daily, err := parseUSDNanos(sourceBudget["dailyBudgetUSD"])
		if err != nil || daily <= 0 {
			return nil, fmt.Errorf("workspace %q dailyBudgetUSD must be positive", workspace)
		}
		budgets[workspace] = WorkspaceBudget{MaxRequestUSD: maxRequest, DailyBudgetUSD: daily}
	}
	return budgets, nil
}

func rejectEnforcedLegacyConflict(lookup EnvLookup) error {
	for _, key := range []string{
		"LLM_PROVIDER_CONFIGS",
		"LLM_PROVIDER",
		"LLM_API_KEY",
		"LLM_BASE_URL",
		"LLM_MODEL",
		"LLM_TEMPERATURE",
		"LLM_REQUEST_TIMEOUT",
		"LLM_MAX_INPUT_TOKENS",
		"LLM_MAX_OUTPUT_TOKENS",
		"LLM_OPENAI_ENABLE_THINKING",
		"LLM_OPENAI_STRICT_TOOL_OUTPUT",
		"LLM_JOB_MAX_ATTEMPTS",
		"LLM_DSL_SELECTOR_REPAIR_ENABLED",
		"LLM_DAILY_COST_BUDGET",
	} {
		if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
			return fmt.Errorf("%s is not valid with LLM_POLICY_MODE=enforced", key)
		}
	}
	if value, ok := lookup("LLM_MAX_RETRIES"); ok && strings.TrimSpace(value) != "" {
		retries, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || retries != 0 {
			return fmt.Errorf("LLM_MAX_RETRIES must be 0 when LLM_POLICY_MODE=enforced")
		}
	}
	if value, ok := lookup("LLM_ALLOW_DEGRADED_FALLBACK"); ok && strings.EqualFold(strings.TrimSpace(value), "true") {
		return fmt.Errorf("LLM_ALLOW_DEGRADED_FALLBACK must not be true when LLM_POLICY_MODE=enforced")
	}
	if value, ok := lookup("LLM_TEMPERATURE_COMPATIBILITY_RETRY"); ok && strings.EqualFold(strings.TrimSpace(value), "true") {
		return fmt.Errorf("LLM_TEMPERATURE_COMPATIBILITY_RETRY must not be true when LLM_POLICY_MODE=enforced")
	}
	return nil
}

func productionPolicyFingerprint(policy *ProductionLLMPolicy) (string, error) {
	type fingerprintRoute struct {
		Slot                     string
		Provider                 string
		Adapter                  string
		Model                    string
		BaseURL                  string
		PriceRevision            string
		RequestTimeout           string
		Temperature              float64
		StrictToolOutput         bool
		EnableThinking           bool
		OutputCapDialect         OutputCapDialect
		InputUSDPerMillion       USDNanos
		CachedInputUSDPerMillion USDNanos
		OutputUSDPerMillion      USDNanos
		MaxInputTokens           int
		MaxOutputTokens          int
	}
	route := func(value RoutePolicy) fingerprintRoute {
		return fingerprintRoute{
			Slot: value.Slot, Provider: value.Provider, Adapter: value.Adapter, Model: value.Model,
			BaseURL: value.BaseURL, PriceRevision: value.PriceRevision,
			RequestTimeout: value.RequestTimeout.String(), Temperature: value.Temperature,
			StrictToolOutput: value.StrictToolOutput, EnableThinking: value.EnableThinking,
			OutputCapDialect:   value.OutputCapDialect,
			InputUSDPerMillion: value.InputUSDPerMillion, CachedInputUSDPerMillion: value.CachedInputUSDPerMillion,
			OutputUSDPerMillion: value.OutputUSDPerMillion, MaxInputTokens: value.MaxInputTokens, MaxOutputTokens: value.MaxOutputTokens,
		}
	}
	payload := struct {
		SchemaVersion            string
		AvailabilityFallbackRule string
		Mode                     LLMPolicyMode
		Primary                  fingerprintRoute
		Fallback                 *fingerprintRoute
		GlobalMaxRequestUSD      USDNanos
		GlobalDailyBudgetUSD     USDNanos
		WorkspaceBudgets         map[string]WorkspaceBudget
		RequirementMaxAttempts   int
		DSLGenerationMaxAttempts int
		DSLMaxRepairs            int
		SelectorMaxRepairs       int
	}{
		SchemaVersion: productionPolicySchemaVersion, AvailabilityFallbackRule: availabilityFallbackRule,
		Mode: policy.Mode, Primary: route(policy.Primary),
		GlobalMaxRequestUSD: policy.GlobalMaxRequestUSD, GlobalDailyBudgetUSD: policy.GlobalDailyBudgetUSD,
		WorkspaceBudgets:         policy.WorkspaceBudgets,
		RequirementMaxAttempts:   policy.RequirementMaxAttempts,
		DSLGenerationMaxAttempts: policy.DSLGenerationMaxAttempts,
		DSLMaxRepairs:            policy.DSLMaxRepairs, SelectorMaxRepairs: policy.SelectorMaxRepairs,
	}
	if policy.Fallback != nil {
		value := route(*policy.Fallback)
		payload.Fallback = &value
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded)), nil
}

// WarnIfLegacyPolicyMode emits a startup deprecation warning whenever
// LLM_POLICY_MODE is unset or set to "legacy". The non-transactional
// tokenBudget checkBefore path used by legacy mode is scheduled for removal in
// a future release; the enforced transactional ledger is the path forward.
//
// The warning is purely informational: it does not alter configuration or fail
// startup. It fires once per process at the call site (typically from
// cmd/server main), giving operators a deprecation window before the legacy
// mode is removed.
func WarnIfLegacyPolicyMode(lookup EnvLookup, logger *zap.Logger) {
	if logger == nil {
		return
	}
	mode := LLMPolicyModeLegacy
	if raw, ok := lookup("LLM_POLICY_MODE"); ok && strings.TrimSpace(raw) != "" {
		mode = LLMPolicyMode(strings.ToLower(strings.TrimSpace(raw)))
	}
	if mode == LLMPolicyModeEnforced {
		return
	}
	currentMode := string(mode)
	if currentMode == "" {
		currentMode = "unset"
	}
	logger.Warn("legacy llm policy mode is deprecated; the non-transactional tokenBudget will be removed in a future release — set LLM_POLICY_MODE=enforced",
		zap.String("current_mode", currentMode),
		zap.String("recommended_mode", "enforced"),
	)
}
