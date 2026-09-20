package config

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"time"
)

// Config holds server configuration.
type Config struct {
	// ConfigFile records the config.yml path that provided the file layer;
	// empty when no file was found. ConfigFileErr is set when a file exists
	// but could not be read or parsed; startup must treat it as fatal so a
	// broken file never silently degrades to environment-only defaults.
	ConfigFile    string
	ConfigFileErr error

	ListenAddr                     string
	DatabasePath                   string
	LeaseDuration                  time.Duration
	SweeperInterval                time.Duration
	MaxRetries                     int
	MaxWorkerTasks                 int
	RateLimitPerSecond             float64
	RateLimitBurst                 int
	WorkerRateLimitPerSecond       float64
	WorkerRateLimitBurst           int
	SiteRateLimitPerSecond         float64
	SiteRateLimitBurst             int
	SiteRateLimitCacheTTL          time.Duration
	CircuitBreakerFailureThreshold int
	CircuitBreakerFailureWindow    time.Duration
	CircuitBreakerOpenDuration     time.Duration
	LogLevel                       string
	WorkerAPIKey                   string
	AdminAPIKey                    string
	MetricsAPIKey                  string
	SwaggerAPIKey                  string
	VariableEncryptionKey          string
	RequireSecurityKeys            bool

	// Additive platform capabilities. They default off so independently
	// deployed extensions, workers, and servers can negotiate staged rollouts.
	RecordingV2Enabled      bool
	WorkflowV2Enabled       bool
	WorkerProtocolV2Enabled bool
	MCPEnabled              bool
	MCPRateLimitPerSecond   float64
	MCPRateLimitBurst       int

	// Recording persistence limits.
	RecordingMaxDuration        time.Duration
	RecordingMaxActions         int
	RecordingMaxCompressedBytes int64
	RecordingRetention          time.Duration

	// Request limits.
	MaxRequestBodyBytes int64
	RequestTimeout      time.Duration

	// Data retention settings.
	ResultRetention        time.Duration
	LogRetention           time.Duration
	SnapshotRetention      time.Duration
	HeartbeatRetention     time.Duration
	StatusUpdateRetention  time.Duration
	CheckpointRetention    time.Duration
	CompletedTaskRetention time.Duration
	RetentionSweepInterval time.Duration
	RetentionBatchSize     int

	// Observability settings.
	DBSizeMetricsInterval time.Duration

	// Audit settings.
	AuditActor string

	// Schedule settings.
	ScheduleRunInterval time.Duration
	SchedulerLockLease  time.Duration

	// SQLite settings.
	SQLiteJournalMode string

	// LLM configuration.
	LLMEnabled  bool
	LLMProvider string
	// LLMPromptVersion selects the system prompt template file
	// templates/enhance_rule_system_<version>.txt. An empty or unknown value
	// falls back to the default embedded template.
	LLMPromptVersion    string
	LLMFallbackProvider string
	LLMAPIKey           string
	LLMBaseURL          string
	LLMModel            string
	LLMTemperature      float64
	// LLMOpenAIEnableThinking controls the non-standard enable_thinking field
	// only when explicitly configured for an OpenAI-compatible provider.
	LLMOpenAIEnableThinking *bool
	// LLMOpenAIStrictToolOutput enables forced, schema-validated function
	// output for requests that provide a StructuredOutput contract.
	LLMOpenAIStrictToolOutput *bool
	LLMMaxRetries             int
	// LLMCallMaxRetries bounds provider-call retries with linear backoff in
	// both policy modes (0 disables). Load defaults it to 2 — or to an
	// explicitly set legacy LLM_MAX_RETRIES in legacy mode — so hand-built
	// zero-value Configs mean "no retries".
	LLMCallMaxRetries int
	// LLMJobMaxAttempts bounds durable requirement and DSL job attempts.
	// Production defaults remain three; strict qualifications set it to one.
	LLMJobMaxAttempts int
	// LLMDSLMaxRepairs bounds repair jobs after failed provisional replays.
	// Zero explicitly disables automatic repair in environment-loaded config.
	LLMDSLMaxRepairs           int
	llmDSLMaxRepairsConfigured bool
	// LLMDSLSelectorRepairEnabled controls automatic selector-only repair
	// scheduling after an eligible provider-generation failure. Nil preserves
	// the production default of enabled.
	LLMDSLSelectorRepairEnabled *bool
	// LLMAllowDegradedFallback controls the deterministic manual-requirement
	// baseline used when every configured provider is unavailable.
	LLMAllowDegradedFallback           bool
	llmAllowDegradedFallbackConfigured bool
	// LLMTemperatureCompatibilityRetry permits one adapter-level retry at
	// temperature=1 for providers that reject the configured temperature.
	LLMTemperatureCompatibilityRetry           bool
	llmTemperatureCompatibilityRetryConfigured bool
	LLMRequestTimeout                          time.Duration
	LLMEnableReflection                        bool
	LLMCacheTTL                                time.Duration
	// LLMMaxInputTokens rejects enhancement requests whose estimated input size
	// exceeds this limit. 0 disables the limit.
	LLMMaxInputTokens int
	// LLMMaxOutputTokens falls back to baseline when the provider returns more
	// output tokens than this limit. 0 disables the limit.
	LLMMaxOutputTokens int
	// LLMDailyCostBudget is the soft daily cost limit in USD. 0 disables it.
	LLMDailyCostBudget float64
	// LLMTokenAlertThreshold logs a warning when input+output tokens exceed this
	// value in a single enhancement job. 0 disables the alert.
	LLMTokenAlertThreshold int
	// LLMJobBatchSize is the maximum number of pending jobs to process per tick.
	LLMJobBatchSize int
	// LLMJobWorkerInterval is the polling interval for the background LLM job worker.
	LLMJobWorkerInterval time.Duration
	// LLMAuditPrompts enables logging of a SHA-256 prompt hash. Legacy mode also
	// logs a truncated prefix; enforced mode never records prompt content.
	LLMAuditPrompts bool
	// LLMRateLimitPerSecond is the per-admin-key rate limit for the rule
	// enhancement endpoint. 0 or negative disables the limit.
	LLMRateLimitPerSecond float64
	// LLMRateLimitBurst is the maximum burst allowed for the enhancement
	// endpoint rate limit.
	LLMRateLimitBurst int

	// ReplayAttemptTimeoutSeconds bounds how long a DSL replay attempt may stay
	// running before the server-side reaper fails it. Zero means use the
	// default of 35 minutes.
	ReplayAttemptTimeoutSeconds int

	// WorkflowBudgetUSDCents overrides the default per-DSL-workflow budget
	// envelope. Zero keeps the 5¢ default (5_000_000 nanodollars). Negative
	// values are ignored.
	WorkflowBudgetUSDCents int

	llmPolicy    *ProductionLLMPolicy
	llmPolicyErr error
}

// Load returns configuration layered from environment variables, config.yml,
// and built-in defaults, in that order of precedence. The config file path
// defaults to config.yml in the working directory and can be moved with
// CONFIG_PATH (environment-only, because it bootstraps the file layer).
func Load() *Config {
	configPath := resolveConfigPath()
	values, yamlErr := loadYAMLConfigFile(configPath)
	// Reset the file layer for this Load call: a missing or unreadable file
	// returns to environment-only behavior, and callers cannot leak state.
	yamlValues = values
	llmEnabled := getBool("LLM_ENABLED", false)
	llmPolicy, llmPolicyErr := LoadProductionLLMPolicy(llmEnabled, lookupSetting)
	configFile := ""
	if values != nil {
		configFile = configPath
	}
	// Call-level retries default to 2. A legacy deployment that explicitly
	// set LLM_MAX_RETRIES keeps that value unless it also sets the new knob.
	// Enforced mode never honors LLM_MAX_RETRIES (startup rejects non-zero).
	callRetriesDefault := 2
	if _, legacySet := lookupSetting("LLM_MAX_RETRIES"); legacySet &&
		(llmPolicy == nil || llmPolicy.Mode != LLMPolicyModeEnforced) {
		callRetriesDefault = getInt("LLM_MAX_RETRIES", 2)
	}
	return &Config{
		ConfigFile:    configFile,
		ConfigFileErr: yamlErr,
		ListenAddr:                     getEnv("LISTEN_ADDR", ":8080"),
		DatabasePath:                   getEnv("DATABASE_PATH", "opencrawler.db"),
		LeaseDuration:                  getDuration("LEASE_DURATION", 60*time.Second),
		SweeperInterval:                getDuration("SWEEPER_INTERVAL", 30*time.Second),
		MaxRetries:                     getInt("MAX_RETRIES", 3),
		MaxWorkerTasks:                 getInt("MAX_WORKER_TASKS", 5),
		RateLimitPerSecond:             getFloat("RATE_LIMIT_PER_SECOND", 100),
		RateLimitBurst:                 getInt("RATE_LIMIT_BURST", 150),
		// Reporting-burst budgets: one healthy extraction legitimately emits
		// dozens of immediate per-row result submissions plus logs, one
		// summary, and terminal statuses within seconds. Both burst budgets
		// must cover one full single-page extraction (~20-40 rows observed on
		// listing pages) so a compliant worker never exhausts its own bucket;
		// the per-second refill still bounds sustained aggregate throughput.
		WorkerRateLimitPerSecond:       getFloat("WORKER_RATE_LIMIT_PER_SECOND", 20),
		WorkerRateLimitBurst:           getInt("WORKER_RATE_LIMIT_BURST", 64),
		SiteRateLimitPerSecond:         getFloat("SITE_RATE_LIMIT_PER_SECOND", 10),
		SiteRateLimitBurst:            getInt("SITE_RATE_LIMIT_BURST", 64),
		SiteRateLimitCacheTTL:          getDuration("SITE_RATE_LIMIT_CACHE_TTL", 5*time.Minute),
		CircuitBreakerFailureThreshold: getInt("CIRCUIT_BREAKER_FAILURE_THRESHOLD", 5),
		CircuitBreakerFailureWindow:    getDuration("CIRCUIT_BREAKER_FAILURE_WINDOW", 60*time.Second),
		CircuitBreakerOpenDuration:     getDuration("CIRCUIT_BREAKER_OPEN_DURATION", 30*time.Second),
		LogLevel:                       getEnv("LOG_LEVEL", "info"),
		WorkerAPIKey:                   getEnv("WORKER_API_KEY", ""),
		AdminAPIKey:                    getEnv("ADMIN_API_KEY", ""),
		MetricsAPIKey:                  getEnv("METRICS_API_KEY", ""),
		SwaggerAPIKey:                  getEnv("SWAGGER_API_KEY", ""),
		VariableEncryptionKey:          getEnv("VARIABLE_ENCRYPTION_KEY", ""),
		RequireSecurityKeys:            getBool("REQUIRE_SECURITY_KEYS", true),
		RecordingV2Enabled:             getBool("FEATURE_RECORDING_V2", false),
		WorkflowV2Enabled:              getBool("FEATURE_WORKFLOW_V2", false),
		WorkerProtocolV2Enabled:        getBool("FEATURE_WORKER_PROTOCOL_V2", false),
		MCPEnabled:                     getBool("FEATURE_MCP", false),
		MCPRateLimitPerSecond:          getFloat("MCP_RATE_LIMIT_PER_SECOND", 10),
		MCPRateLimitBurst:              getInt("MCP_RATE_LIMIT_BURST", 20),
		RecordingMaxDuration:           getDuration("RECORDING_MAX_DURATION", 2*time.Hour),
		RecordingMaxActions:            getInt("RECORDING_MAX_ACTIONS", 500),
		RecordingMaxCompressedBytes:    getInt64("RECORDING_MAX_COMPRESSED_BYTES", 25*1024*1024),
		RecordingRetention:             getDuration("RECORDING_RETENTION", 7*24*time.Hour),

		MaxRequestBodyBytes: getInt64("MAX_REQUEST_BODY_BYTES", 8*1024*1024),
		RequestTimeout:      getDuration("REQUEST_TIMEOUT", 180*time.Second),

		ResultRetention:        getDuration("RESULT_RETENTION", 7*24*time.Hour),
		LogRetention:           getDuration("LOG_RETENTION", 7*24*time.Hour),
		SnapshotRetention:      getDuration("SNAPSHOT_RETENTION", 7*24*time.Hour),
		HeartbeatRetention:     getDuration("HEARTBEAT_RETENTION", 24*time.Hour),
		StatusUpdateRetention:  getDuration("STATUS_UPDATE_RETENTION", 7*24*time.Hour),
		CheckpointRetention:    getDuration("CHECKPOINT_RETENTION", 7*24*time.Hour),
		CompletedTaskRetention: getDuration("COMPLETED_TASK_RETENTION", 30*24*time.Hour),
		RetentionSweepInterval: getDuration("RETENTION_SWEEP_INTERVAL", time.Hour),
		RetentionBatchSize:     getInt("RETENTION_BATCH_SIZE", 1000),

		DBSizeMetricsInterval: getDuration("DB_SIZE_METRICS_INTERVAL", time.Minute),
		AuditActor:            getEnv("AUDIT_ACTOR", "admin"),
		ScheduleRunInterval:   getDuration("SCHEDULE_RUN_INTERVAL", time.Minute),
		SchedulerLockLease:    getDuration("SCHEDULER_LOCK_LEASE", 2*time.Minute),
		SQLiteJournalMode:     getEnv("SQLITE_JOURNAL_MODE", "WAL"),

		LLMEnabled:                                 llmEnabled,
		LLMProvider:                                getEnv("LLM_PROVIDER", "openai"),
		LLMPromptVersion:                           getEnv("LLM_PROMPT_VERSION", "v1"),
		LLMFallbackProvider:                        getEnv("LLM_FALLBACK_PROVIDER", ""),
		LLMAPIKey:                                  getEnv("LLM_API_KEY", ""),
		LLMBaseURL:                                 getEnv("LLM_BASE_URL", "https://api.openai.com/v1"),
		LLMModel:                                   getEnv("LLM_MODEL", "gpt-4o"),
		LLMTemperature:                             getFloat("LLM_TEMPERATURE", 0.2),
		LLMOpenAIEnableThinking:                    getOptionalBool("LLM_OPENAI_ENABLE_THINKING"),
		LLMOpenAIStrictToolOutput:                  getOptionalBool("LLM_OPENAI_STRICT_TOOL_OUTPUT"),
		LLMMaxRetries:                              getInt("LLM_MAX_RETRIES", 2),
		LLMCallMaxRetries:                          getBoundedInt("LLM_CALL_MAX_RETRIES", callRetriesDefault, 0, 10),
		LLMJobMaxAttempts:                          getBoundedInt("LLM_JOB_MAX_ATTEMPTS", 3, 1, 3),
		LLMDSLMaxRepairs:                           getBoundedInt("LLM_DSL_MAX_REPAIRS", 3, 0, 3),
		llmDSLMaxRepairsConfigured:                 true,
		LLMDSLSelectorRepairEnabled:                getOptionalBool("LLM_DSL_SELECTOR_REPAIR_ENABLED"),
		LLMAllowDegradedFallback:                   getBool("LLM_ALLOW_DEGRADED_FALLBACK", true),
		llmAllowDegradedFallbackConfigured:         true,
		LLMTemperatureCompatibilityRetry:           getBool("LLM_TEMPERATURE_COMPATIBILITY_RETRY", true),
		llmTemperatureCompatibilityRetryConfigured: true,
		LLMRequestTimeout:                          getDuration("LLM_REQUEST_TIMEOUT", 180*time.Second),
		LLMEnableReflection:                        getBool("LLM_ENABLE_REFLECTION", true),
		LLMCacheTTL:                                getDuration("LLM_CACHE_TTL", 24*time.Hour),
		LLMMaxInputTokens:                          getInt("LLM_MAX_INPUT_TOKENS", 0),
		LLMMaxOutputTokens:                         getInt("LLM_MAX_OUTPUT_TOKENS", 0),
		LLMDailyCostBudget:                         getFloat("LLM_DAILY_COST_BUDGET", 0),
		LLMTokenAlertThreshold:                     getInt("LLM_TOKEN_ALERT_THRESHOLD", 0),
		LLMJobBatchSize:                            getInt("LLM_JOB_BATCH_SIZE", 10),
		LLMJobWorkerInterval:                       getDuration("LLM_JOB_WORKER_INTERVAL", 5*time.Second),
		LLMAuditPrompts:                            getBool("LLM_AUDIT_PROMPTS", false),
		LLMRateLimitPerSecond:                      getFloat("LLM_RATE_LIMIT_PER_SECOND", 1),
		LLMRateLimitBurst:                          getInt("LLM_RATE_LIMIT_BURST", 2),
		ReplayAttemptTimeoutSeconds:                getInt("REPLAY_ATTEMPT_TIMEOUT_SECONDS", 0),
		WorkflowBudgetUSDCents:                     getInt("WORKFLOW_BUDGET_USD_CENTS", 0),
		llmPolicy:                                  llmPolicy,
		llmPolicyErr:                               llmPolicyErr,
	}
}

func getBool(key string, defaultValue bool) bool {
	v, _ := lookupSetting(key)
	if v == "" {
		return defaultValue
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return defaultValue
	}
	return b
}

func getOptionalBool(key string) *bool {
	v, _ := lookupSetting(key)
	if v == "" {
		return nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return nil
	}
	return &b
}

func getEnv(key, defaultValue string) string {
	if v, _ := lookupSetting(key); v != "" {
		return v
	}
	return defaultValue
}

func getInt(key string, defaultValue int) int {
	v, _ := lookupSetting(key)
	if v == "" {
		return defaultValue
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultValue
	}
	return n
}

func getBoundedInt(key string, defaultValue, minimum, maximum int) int {
	value := getInt(key, defaultValue)
	if value < minimum || value > maximum {
		return defaultValue
	}
	return value
}

// DurableJobMaxAttempts preserves the historical three-attempt behavior for
// hand-built Config values while honoring environment-loaded bounds.
func (c *Config) DurableJobMaxAttempts() int {
	if c == nil || c.LLMJobMaxAttempts < 1 || c.LLMJobMaxAttempts > 3 {
		return 3
	}
	return c.LLMJobMaxAttempts
}

// WorkflowBudgetUSD returns the default per-DSL-workflow budget envelope in
// nanodollars on the same scale the LLM budget ledger prices at ($1 =
// 1_000_000_000 nanos, matching parseUSDNanos). The 5¢ default is therefore
// 50_000_000 nanos. Override via WORKFLOW_BUDGET_USD_CENTS (one cent =
// 10_000_000 nanos); a non-positive value keeps the default. The previous
// 1e8-scale helper produced a 5_000_000-nanos envelope that a single
// worst-case reservation (≈24_000_000 nanos) already exceeded, exhausting
// every new workflow on its first dispatch.
func (c *Config) WorkflowBudgetUSD() USDNanos {
	if c != nil && c.WorkflowBudgetUSDCents > 0 {
		return USDNanos(int64(c.WorkflowBudgetUSDCents) * 10_000_000)
	}
	return USDNanos(50_000_000)
}

// ValidateLLMPolicy returns the deferred startup validation error for an
// enforced LLM policy. It is intentionally separate from Load so callers can
// validate before acquiring external resources.
func (c *Config) ValidateLLMPolicy() error {
	if c == nil {
		return fmt.Errorf("configuration is required")
	}
	return c.llmPolicyErr
}

// EnforcedLLMPolicy returns the parsed policy only after it has passed
// validation. A nil result preserves legacy configuration behavior.
func (c *Config) EnforcedLLMPolicy() *ProductionLLMPolicy {
	if c == nil || c.llmPolicyErr != nil || c.llmPolicy == nil || c.llmPolicy.Mode != LLMPolicyModeEnforced {
		return nil
	}
	return c.llmPolicy
}

func (c *Config) RequirementMaxAttempts() int {
	if policy := c.EnforcedLLMPolicy(); policy != nil {
		return policy.RequirementMaxAttempts
	}
	return c.DurableJobMaxAttempts()
}

func (c *Config) DSLGenerationMaxAttempts() int {
	if policy := c.EnforcedLLMPolicy(); policy != nil {
		return policy.DSLGenerationMaxAttempts
	}
	return c.DurableJobMaxAttempts()
}

// DSLMaxRepairs preserves the historical default for hand-built Config
// values. Load marks an explicit zero as configured so strict runs can disable
// repair without changing production defaults.
func (c *Config) DSLMaxRepairs() int {
	if c == nil {
		return 3
	}
	if policy := c.EnforcedLLMPolicy(); policy != nil {
		return policy.DSLMaxRepairs
	}
	if c.llmDSLMaxRepairsConfigured && c.LLMDSLMaxRepairs >= 0 && c.LLMDSLMaxRepairs <= 3 {
		return c.LLMDSLMaxRepairs
	}
	if c.LLMDSLMaxRepairs >= 1 && c.LLMDSLMaxRepairs <= 3 {
		return c.LLMDSLMaxRepairs
	}
	return 3
}

// DSLSelectorRepairEnabled preserves automatic selector repair unless a
// qualification or operator explicitly disables its scheduling.
func (c *Config) DSLSelectorRepairEnabled() bool {
	return c.SelectorRepairEnabled()
}

func (c *Config) SelectorRepairEnabled() bool {
	if policy := c.EnforcedLLMPolicy(); policy != nil {
		return policy.SelectorMaxRepairs > 0
	}
	return c == nil ||
		c.LLMDSLSelectorRepairEnabled == nil ||
		*c.LLMDSLSelectorRepairEnabled
}

func (c *Config) AllowDegradedFallback() bool {
	if c.EnforcedLLMPolicy() != nil {
		return false
	}
	if c == nil || !c.llmAllowDegradedFallbackConfigured {
		return true
	}
	return c.LLMAllowDegradedFallback
}

func (c *Config) AllowTemperatureCompatibilityRetry() bool {
	if c.EnforcedLLMPolicy() != nil {
		return false
	}
	if c == nil || !c.llmTemperatureCompatibilityRetryConfigured {
		return true
	}
	return c.LLMTemperatureCompatibilityRetry
}

// ReplayAttemptTimeout returns the configured replay attempt timeout duration,
// defaulting to 35 minutes when unset or non-positive.
func (c *Config) ReplayAttemptTimeout() time.Duration {
	if c == nil || c.ReplayAttemptTimeoutSeconds <= 0 {
		return 35 * time.Minute
	}
	return time.Duration(c.ReplayAttemptTimeoutSeconds) * time.Second
}

func getInt64(key string, defaultValue int64) int64 {
	v, _ := lookupSetting(key)
	if v == "" {
		return defaultValue
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return defaultValue
	}
	return n
}

func getFloat(key string, defaultValue float64) float64 {
	v, _ := lookupSetting(key)
	if v == "" {
		return defaultValue
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return defaultValue
	}
	return f
}

func getDuration(key string, defaultValue time.Duration) time.Duration {
	v, _ := lookupSetting(key)
	if v == "" {
		return defaultValue
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return defaultValue
	}
	return d
}

// ProviderConfig holds per-provider credentials and model settings.
type ProviderConfig struct {
	// ProviderLabel is the operator-approved billing/provenance label. The
	// adapter in Provider selects the protocol implementation.
	ProviderLabel                        string           `json:"-"`
	Provider                             string           `json:"provider"` // openai | anthropic
	APIKey                               string           `json:"apiKey"`
	BaseURL                              string           `json:"baseURL"`
	Model                                string           `json:"model"`
	Timeout                              time.Duration    `json:"timeout"`
	Temperature                          float64          `json:"temperature"`
	OutputCapDialect                     OutputCapDialect `json:"-"`
	OpenAIEnableThinking                 *bool            `json:"openAIEnableThinking,omitempty"`
	OpenAIStrictToolOutput               *bool            `json:"openAIStrictToolOutput,omitempty"`
	LoopbackArchivePath                  string           `json:"loopbackArchivePath,omitempty"`
	DisableTemperatureCompatibilityRetry bool             `json:"-"`
}

// ProviderRoutes returns the fixed route slots for an enforced policy. Legacy
// deployments retain their named-provider configuration map.
func (c *Config) ProviderRoutes() map[string]ProviderConfig {
	if policy := c.EnforcedLLMPolicy(); policy != nil {
		routes := map[string]ProviderConfig{
			"primary": providerConfigFromRoute(policy.Primary),
		}
		if policy.Fallback != nil {
			routes["fallback"] = providerConfigFromRoute(*policy.Fallback)
		}
		return routes
	}
	return c.LLMProviderConfigs()
}

func providerConfigFromRoute(route RoutePolicy) ProviderConfig {
	thinking := route.EnableThinking
	strict := route.StrictToolOutput
	return ProviderConfig{
		ProviderLabel:                        route.Provider,
		Provider:                             route.Adapter,
		APIKey:                               route.APIKey,
		BaseURL:                              route.BaseURL,
		Model:                                route.Model,
		Timeout:                              route.RequestTimeout,
		Temperature:                          route.Temperature,
		OutputCapDialect:                     route.OutputCapDialect,
		OpenAIEnableThinking:                 &thinking,
		OpenAIStrictToolOutput:               &strict,
		DisableTemperatureCompatibilityRetry: true,
	}
}

// UnmarshalJSON supports timeout expressed as a duration string (e.g. "30s")
// or as a numeric value in nanoseconds, and temperature as a number.
func (pc *ProviderConfig) UnmarshalJSON(data []byte) error {
	type alias struct {
		Provider               string  `json:"provider"`
		APIKey                 string  `json:"apiKey"`
		BaseURL                string  `json:"baseURL"`
		Model                  string  `json:"model"`
		Temperature            float64 `json:"temperature"`
		OpenAIEnableThinking   *bool   `json:"openAIEnableThinking"`
		OpenAIStrictToolOutput *bool   `json:"openAIStrictToolOutput"`
		LoopbackArchivePath    string  `json:"loopbackArchivePath"`
	}
	var aux struct {
		alias
		Timeout any `json:"timeout"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	pc.Provider = aux.Provider
	pc.APIKey = aux.APIKey
	pc.BaseURL = aux.BaseURL
	pc.Model = aux.Model
	pc.Temperature = aux.Temperature
	pc.OpenAIEnableThinking = aux.OpenAIEnableThinking
	pc.OpenAIStrictToolOutput = aux.OpenAIStrictToolOutput
	pc.LoopbackArchivePath = aux.LoopbackArchivePath
	if aux.Timeout != nil {
		switch v := aux.Timeout.(type) {
		case string:
			d, err := time.ParseDuration(v)
			if err != nil {
				return err
			}
			pc.Timeout = d
		case float64:
			pc.Timeout = time.Duration(v)
		default:
			return fmt.Errorf("unsupported timeout type %T", aux.Timeout)
		}
	}
	return nil
}

// LLMProviderConfigs is keyed by provider logical name.
func (c *Config) LLMProviderConfigs() map[string]ProviderConfig {
	m := map[string]ProviderConfig{}
	if raw := getEnv("LLM_PROVIDER_CONFIGS", ""); raw != "" {
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			log.Printf("warning: LLM_PROVIDER_CONFIGS is malformed (%v); falling back to legacy LLM_* config", err)
		}
	}
	// Legacy fallback: ensure primary/fallback providers have at least one config.
	if _, ok := m[c.LLMProvider]; !ok && c.LLMProvider != "" {
		m[c.LLMProvider] = ProviderConfig{
			Provider:                             c.LLMProvider,
			APIKey:                               c.LLMAPIKey,
			BaseURL:                              c.LLMBaseURL,
			Model:                                c.LLMModel,
			Timeout:                              c.LLMRequestTimeout,
			Temperature:                          c.LLMTemperature,
			OpenAIEnableThinking:                 c.LLMOpenAIEnableThinking,
			OpenAIStrictToolOutput:               c.LLMOpenAIStrictToolOutput,
			DisableTemperatureCompatibilityRetry: !c.AllowTemperatureCompatibilityRetry(),
		}
	}
	if c.LLMFallbackProvider != "" {
		if _, ok := m[c.LLMFallbackProvider]; !ok {
			m[c.LLMFallbackProvider] = ProviderConfig{
				Provider:                             c.LLMFallbackProvider,
				APIKey:                               c.LLMAPIKey,
				BaseURL:                              c.LLMBaseURL,
				Model:                                c.LLMModel,
				Timeout:                              c.LLMRequestTimeout,
				Temperature:                          c.LLMTemperature,
				OpenAIEnableThinking:                 c.LLMOpenAIEnableThinking,
				OpenAIStrictToolOutput:               c.LLMOpenAIStrictToolOutput,
				DisableTemperatureCompatibilityRetry: !c.AllowTemperatureCompatibilityRetry(),
			}
		}
	}
	for name, provider := range m {
		if provider.OpenAIEnableThinking == nil && c.LLMOpenAIEnableThinking != nil &&
			(provider.Provider == "" || provider.Provider == "openai") {
			provider.OpenAIEnableThinking = c.LLMOpenAIEnableThinking
		}
		if provider.OpenAIStrictToolOutput == nil && c.LLMOpenAIStrictToolOutput != nil &&
			(provider.Provider == "" || provider.Provider == "openai") {
			provider.OpenAIStrictToolOutput = c.LLMOpenAIStrictToolOutput
		}
		provider.DisableTemperatureCompatibilityRetry = !c.AllowTemperatureCompatibilityRetry()
		m[name] = provider
	}
	return m
}

// OpenAIStrictToolOutputEnabled reports the effective setting for the active
// provider, including an explicit named-provider override.
func (c *Config) OpenAIStrictToolOutputEnabled() bool {
	if c == nil {
		return false
	}
	if provider, ok := c.LLMProviderConfigs()[c.LLMProvider]; ok &&
		provider.OpenAIStrictToolOutput != nil {
		return *provider.OpenAIStrictToolOutput
	}
	return c.LLMOpenAIStrictToolOutput != nil && *c.LLMOpenAIStrictToolOutput
}
