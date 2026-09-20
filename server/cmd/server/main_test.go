package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/singhand-labs/AegisCrawler/internal/api"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

func TestStartDBSizeReporter(t *testing.T) {
	f, err := os.CreateTemp("", "db-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString("x"); err != nil {
		t.Fatalf("write temp db: %v", err)
	}
	f.Close()

	logger := zap.NewNop()
	metrics := api.NewMetrics()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		startDBSizeReporter(ctx, f.Name(), 10*time.Millisecond, metrics, logger)
		close(done)
	}()

	time.Sleep(25 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reporter did not stop")
	}
}

func TestNewLogger(t *testing.T) {
	logger, err := newLogger("debug")
	if err != nil {
		t.Fatal(err)
	}
	if logger == nil {
		t.Fatal("expected logger")
	}
	if !logger.Core().Enabled(zap.DebugLevel) {
		t.Fatal("debug logger did not enable debug messages")
	}
	_ = logger.Sync()

	logger, err = newLogger("info")
	if err != nil {
		t.Fatal(err)
	}
	if logger == nil {
		t.Fatal("expected logger")
	}
	if logger.Core().Enabled(zap.DebugLevel) || !logger.Core().Enabled(zap.InfoLevel) {
		t.Fatal("info logger has the wrong threshold")
	}
	_ = logger.Sync()

	logger, err = newLogger(" WARN ")
	if err != nil {
		t.Fatal(err)
	}
	if logger.Core().Enabled(zap.InfoLevel) || !logger.Core().Enabled(zap.WarnLevel) {
		t.Fatal("warn logger has the wrong threshold")
	}
	_ = logger.Sync()

	logger, err = newLogger("error")
	if err != nil {
		t.Fatal(err)
	}
	if logger.Core().Enabled(zap.WarnLevel) || !logger.Core().Enabled(zap.ErrorLevel) {
		t.Fatal("error logger has the wrong threshold")
	}
	_ = logger.Sync()

	if _, err := newLogger("verbose"); err == nil {
		t.Fatal("expected invalid log level to fail")
	}
}

func TestMissingSecurityKeys(t *testing.T) {
	cfg := &config.Config{RequireSecurityKeys: false}
	if missing := missingSecurityKeys(cfg); missing != nil {
		t.Fatalf("expected nil when not required, got %v", missing)
	}

	cfg = &config.Config{
		RequireSecurityKeys:   true,
		WorkerAPIKey:          "x",
		AdminAPIKey:           "x",
		VariableEncryptionKey: "x",
	}
	if missing := missingSecurityKeys(cfg); len(missing) != 0 {
		t.Fatalf("expected no missing keys, got %v", missing)
	}

	cfg = &config.Config{RequireSecurityKeys: true}
	missing := missingSecurityKeys(cfg)
	if len(missing) != 3 {
		t.Fatalf("expected 3 missing keys, got %v", missing)
	}
}

func TestStartDBSizeReporterDefaults(t *testing.T) {
	f, err := os.CreateTemp("", "db-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.WriteString("x")
	f.Close()

	logger := zap.NewNop()
	metrics := api.NewMetrics()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		startDBSizeReporter(ctx, f.Name(), 0, metrics, logger)
		close(done)
	}()

	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reporter did not stop")
	}
}

func TestStartDBSizeReporterStatError(t *testing.T) {
	logger := zap.NewNop()
	metrics := api.NewMetrics()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		startDBSizeReporter(ctx, "/nonexistent/path/db.db", 10*time.Millisecond, metrics, logger)
		close(done)
	}()

	time.Sleep(25 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reporter did not stop")
	}
}

func TestMissingSecurityKeysPartial(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		want []string
	}{
		{
			name: "worker missing",
			cfg: &config.Config{
				RequireSecurityKeys:   true,
				AdminAPIKey:           "x",
				VariableEncryptionKey: "x",
			},
			want: []string{"WORKER_API_KEY"},
		},
		{
			name: "admin missing",
			cfg: &config.Config{
				RequireSecurityKeys:   true,
				WorkerAPIKey:          "x",
				VariableEncryptionKey: "x",
			},
			want: []string{"ADMIN_API_KEY"},
		},
		{
			name: "encryption missing",
			cfg: &config.Config{
				RequireSecurityKeys: true,
				WorkerAPIKey:        "x",
				AdminAPIKey:         "x",
			},
			want: []string{"VARIABLE_ENCRYPTION_KEY"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := missingSecurityKeys(tc.cfg)
			if len(got) != len(tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("expected %v, got %v", tc.want, got)
				}
			}
		})
	}
}

func TestValidateStartupConfigRejectsInvalidEnforcedLLMPolicy(t *testing.T) {
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("LLM_POLICY_MODE", "enforced")

	if err := validateStartupConfig(config.Load()); err == nil {
		t.Fatal("validateStartupConfig accepted an incomplete enforced LLM policy")
	}
}

func TestValidateStartupConfigRejectsAlwaysDeniedEnforcedPolicy(t *testing.T) {
	setEnforcedLLMEnvironment(t)
	t.Setenv("LLM_GLOBAL_MAX_REQUEST_USD", "0.000001")

	if err := validateStartupConfig(config.Load()); err == nil {
		t.Fatal("validateStartupConfig accepted a policy whose primary route can never be admitted")
	}
}

func TestPlanLLMStartupSkipsProvidersLedgerAndWorkersWhenDisabled(t *testing.T) {
	for _, mode := range []string{"legacy", "enforced"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("LLM_ENABLED", "false")
			t.Setenv("LLM_POLICY_MODE", mode)
			cfg := config.Load()
			if err := validateStartupConfig(cfg); err != nil {
				t.Fatalf("disabled %s policy must not require provider or budget configuration: %v", mode, err)
			}
			if mode == "enforced" && cfg.EnforcedLLMPolicy() == nil {
				t.Fatal("test requires the configured policy mode to remain observable as enforced")
			}

			plan := planLLMStartup(cfg)
			if plan.constructProviders {
				t.Fatal("disabled LLM must not construct provider clients")
			}
			if plan.activateLedger {
				t.Fatal("disabled LLM must not activate the hard-budget ledger")
			}
			if plan.startWorkers {
				t.Fatal("disabled LLM must not start LLM workers")
			}
		})
	}
}

func TestPlanLLMStartupStartsWorkersWhenEnabled(t *testing.T) {
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("LLM_POLICY_MODE", "legacy")

	plan := planLLMStartup(config.Load())
	if !plan.constructProviders || !plan.startWorkers {
		t.Fatalf("enabled startup plan = %+v; want providers and workers", plan)
	}
	if plan.activateLedger {
		t.Fatal("legacy mode must not activate the enforced hard-budget ledger")
	}
}

func TestRegisterLLMProvidersBindsEnforcedRouteSlots(t *testing.T) {
	setEnforcedLLMEnvironment(t)
	cfg := config.Load()
	if err := validateStartupConfig(cfg); err != nil {
		t.Fatal(err)
	}
	orch := llm.NewOrchestrator(cfg, zap.NewNop())

	if err := registerLLMProviders(cfg, orch, zap.NewNop()); err != nil {
		t.Fatal(err)
	}
}

func TestBuildLLMProvidersValidatesEnforcedRoutesBeforeRegistration(t *testing.T) {
	setEnforcedLLMEnvironment(t)
	bindings, err := buildLLMProviders(config.Load(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 || bindings[0].route != "primary" || bindings[0].provider.Name() != "aliyun" {
		t.Fatalf("buildLLMProviders() = %+v", bindings)
	}
}

func TestActivateBudgetLedgerReconcilesBeforeDispatch(t *testing.T) {
	setEnforcedLLMEnvironment(t)
	cfg := config.Load()
	if err := validateStartupConfig(cfg); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s := newStartupTestStore(t)

	policy := cfg.EnforcedLLMPolicy()
	if policy == nil {
		t.Fatal("expected an enforced policy")
	}
	limits := budget.LimitsFromPolicy(policy)

	// Leave a possibly-dispatched entry behind, as a crash mid-call would.
	seed := store.NewBudgetLedger(s, limits)
	identity := budget.DispatchIdentity{
		WorkspaceID:     "default",
		OperationKind:   budget.OperationEnhance,
		OperationID:     "crashed-job",
		LogicalAttempt:  1,
		PhysicalOrdinal: budget.OrdinalPrimary,
	}
	rates := budget.Rates{
		InputUSDPerMillion:       policy.Primary.InputUSDPerMillion,
		CachedInputUSDPerMillion: policy.Primary.CachedInputUSDPerMillion,
		OutputUSDPerMillion:      policy.Primary.OutputUSDPerMillion,
	}
	caps := budget.Caps{
		MaxInputTokens:  policy.Primary.MaxInputTokens,
		MaxOutputTokens: policy.Primary.MaxOutputTokens,
	}
	reserved, err := budget.Reservation(rates, caps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Reserve(ctx, budget.ReserveRequest{
		Identity:          identity,
		BudgetDay:         budget.BudgetDay(time.Now().UTC()),
		RouteSlot:         "primary",
		Provider:          policy.Primary.Provider,
		Model:             policy.Primary.Model,
		Endpoint:          policy.Primary.BaseURL,
		PolicyFingerprint: policy.Fingerprint,
		PriceRevision:     policy.Primary.PriceRevision,
		RequestHash:       "hash",
		Rates:             rates,
		Caps:              caps,
		Reserved:          reserved,
	}); err != nil {
		t.Fatalf("seed reservation: %v", err)
	}
	if err := seed.MarkPossiblyDispatched(ctx, identity); err != nil {
		t.Fatalf("seed dispatch marker: %v", err)
	}

	// Leave a second definitely-undispatched reservation. Recovery must release
	// it, and the initial gauges must exclude it from both settled and active.
	releasedIdentity := identity
	releasedIdentity.OperationID = "reserved-before-crash"
	if _, err := seed.Reserve(ctx, budget.ReserveRequest{
		Identity:          releasedIdentity,
		BudgetDay:         budget.BudgetDay(time.Now().UTC()),
		RouteSlot:         "primary",
		Provider:          policy.Primary.Provider,
		Model:             policy.Primary.Model,
		Endpoint:          policy.Primary.BaseURL,
		PolicyFingerprint: policy.Fingerprint,
		PriceRevision:     policy.Primary.PriceRevision,
		RequestHash:       "released-hash",
		Rates:             rates,
		Caps:              caps,
		Reserved:          reserved,
	}); err != nil {
		t.Fatalf("seed undispatched reservation: %v", err)
	}

	orch := llm.NewOrchestrator(cfg, zap.NewNop())
	llmMetrics := llm.NewMetricsWithRegistry(prometheus.NewRegistry())
	orch.SetMetrics(llmMetrics)
	activated, err := activateBudgetLedger(ctx, cfg, s, orch, llmMetrics, zap.NewNop())
	if err != nil {
		t.Fatalf("activateBudgetLedger: %v", err)
	}
	if activated == nil {
		t.Fatal("expected activation to return the bound ledger")
	}

	// The crashed call must have been charged conservatively at startup.
	entry, err := seed.Lookup(ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || entry.State != budget.StateUsageUncertain {
		t.Fatalf("recovered entry = %+v, want usage_uncertain", entry)
	}
	if entry.Settled != entry.Reserved {
		t.Fatalf("recovered settled = %d, want the full reservation %d", entry.Settled, entry.Reserved)
	}
	releasedEntry, err := seed.Lookup(ctx, releasedIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if releasedEntry == nil || releasedEntry.State != budget.StateReleased || releasedEntry.Settled != 0 {
		t.Fatalf("recovered undispatched entry = %+v, want released with zero settlement", releasedEntry)
	}
	if got := testutil.ToFloat64(llmMetrics.UncertainUsage.WithLabelValues("startup_reconciliation")); got != 1 {
		t.Fatalf("startup reconciliation uncertain calls = %v, want 1", got)
	}
	if got := testutil.ToFloat64(llmMetrics.BudgetSettled); got != float64(reserved) {
		t.Fatalf("settled gauge = %v, want %d", got, reserved)
	}
	if got := testutil.ToFloat64(llmMetrics.BudgetActive); got != 0 {
		t.Fatalf("active gauge = %v, want 0 after release/uncertain recovery", got)
	}
	wantRemaining := policy.GlobalDailyBudgetUSD - reserved
	if got := testutil.ToFloat64(llmMetrics.BudgetRemaining); got != float64(wantRemaining) {
		t.Fatalf("remaining gauge = %v, want %d after released reservation is freed", got, wantRemaining)
	}
}

func TestActivateBudgetLedgerRequiresEnforcedPolicy(t *testing.T) {
	cfg := &config.Config{}
	s := newStartupTestStore(t)
	orch := llm.NewOrchestrator(cfg, zap.NewNop())

	if _, err := activateBudgetLedger(context.Background(), cfg, s, orch, nil, zap.NewNop()); err == nil {
		t.Fatal("expected activation to require an enforced policy")
	}
}

func newStartupTestStore(t *testing.T) *store.Store {
	t.Helper()
	f, err := os.CreateTemp("", "aegis-startup-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	s, err := store.New(f.Name(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func setEnforcedLLMEnvironment(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"LLM_ENABLED":                         "true",
		"LLM_POLICY_MODE":                     "enforced",
		"LLM_PRIMARY_PROVIDER":                "aliyun",
		"LLM_PRIMARY_ADAPTER":                 "openai",
		"LLM_PRIMARY_MODEL":                   "qwen-test",
		"LLM_PRIMARY_BASE_URL":                "https://provider.example.test/v1",
		"LLM_PRIMARY_API_KEY":                 "synthetic-primary-key",
		"LLM_PRIMARY_REQUEST_TIMEOUT":         "30s",
		"LLM_PRIMARY_TEMPERATURE":             "0",
		"LLM_PRIMARY_STRICT_TOOL_OUTPUT":      "true",
		"LLM_PRIMARY_ENABLE_THINKING":         "false",
		"LLM_PRIMARY_INPUT_USD_PER_MILLION":   "0.14",
		"LLM_PRIMARY_OUTPUT_USD_PER_MILLION":  "0.28",
		"LLM_PRIMARY_MAX_INPUT_TOKENS":        "4096",
		"LLM_PRIMARY_MAX_OUTPUT_TOKENS":       "512",
		"LLM_PRIMARY_PRICE_REVISION":          "test-v1",
		"LLM_GLOBAL_MAX_REQUEST_USD":          "0.60",
		"LLM_GLOBAL_DAILY_BUDGET_USD":         "3.00",
		"LLM_WORKSPACE_BUDGETS_JSON":          `{"default":{"maxRequestUSD":"0.60","dailyBudgetUSD":"3.00"}}`,
		"LLM_REQUIREMENT_MAX_ATTEMPTS":        "2",
		"LLM_DSL_GENERATION_MAX_ATTEMPTS":     "3",
		"LLM_DSL_MAX_REPAIRS":                 "1",
		"LLM_SELECTOR_MAX_REPAIRS":            "1",
		"LLM_PROVIDER_CONFIGS":                "",
		"LLM_PROVIDER":                        "",
		"LLM_FALLBACK_PROVIDER":               "",
		"LLM_API_KEY":                         "",
		"LLM_BASE_URL":                        "",
		"LLM_MODEL":                           "",
		"LLM_TEMPERATURE":                     "",
		"LLM_REQUEST_TIMEOUT":                 "",
		"LLM_MAX_INPUT_TOKENS":                "",
		"LLM_MAX_OUTPUT_TOKENS":               "",
		"LLM_OPENAI_ENABLE_THINKING":          "",
		"LLM_OPENAI_STRICT_TOOL_OUTPUT":       "",
		"LLM_JOB_MAX_ATTEMPTS":                "",
		"LLM_DSL_SELECTOR_REPAIR_ENABLED":     "",
		"LLM_DAILY_COST_BUDGET":               "",
		"LLM_MAX_RETRIES":                     "",
		"LLM_ALLOW_DEGRADED_FALLBACK":         "",
		"LLM_TEMPERATURE_COMPATIBILITY_RETRY": "",
	} {
		t.Setenv(key, value)
	}
}
