package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/api"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/llm/cache"
	"github.com/singhand-labs/AegisCrawler/internal/llm/dsl"
	"github.com/singhand-labs/AegisCrawler/internal/llm/intent"
	"github.com/singhand-labs/AegisCrawler/internal/llm/providers"
	llmrequirement "github.com/singhand-labs/AegisCrawler/internal/llm/requirement"
	"github.com/singhand-labs/AegisCrawler/internal/scheduler"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// startDBSizeReporter periodically updates the database size metric by stat'ing
// the SQLite file on disk. It stops when ctx is cancelled.
func startDBSizeReporter(ctx context.Context, dbPath string, interval time.Duration, metrics *api.Metrics, logger *zap.Logger) {
	if interval <= 0 {
		interval = time.Minute
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()

	update := func() {
		info, err := os.Stat(dbPath)
		if err != nil {
			logger.Error("failed to stat database file", zap.String("path", dbPath), zap.Error(err))
			return
		}
		metrics.SetDBSize(info.Size())
	}
	update()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			update()
		}
	}
}

// newLogger returns a zap logger configured for the supplied log level.
func newLogger(level string) (*zap.Logger, error) {
	level = strings.ToLower(strings.TrimSpace(level))
	var configuredLevel zapcore.Level
	if err := configuredLevel.Set(level); err != nil {
		return nil, fmt.Errorf("invalid LOG_LEVEL %q: %w", level, err)
	}
	loggerConfig := zap.NewProductionConfig()
	if configuredLevel == zapcore.DebugLevel {
		loggerConfig = zap.NewDevelopmentConfig()
	}
	loggerConfig.Level = zap.NewAtomicLevelAt(configuredLevel)
	return loggerConfig.Build()
}

// missingSecurityKeys returns the names of required security keys that are empty.
// It returns nil when no keys are missing or when security keys are not required.
func missingSecurityKeys(cfg *config.Config) []string {
	if !cfg.RequireSecurityKeys {
		return nil
	}
	missing := []string{}
	if cfg.WorkerAPIKey == "" {
		missing = append(missing, "WORKER_API_KEY")
	}
	if cfg.AdminAPIKey == "" {
		missing = append(missing, "ADMIN_API_KEY")
	}
	if cfg.VariableEncryptionKey == "" {
		missing = append(missing, "VARIABLE_ENCRYPTION_KEY")
	}
	return missing
}

// validateStartupConfig runs configuration checks before the server opens a
// database, starts workers, or constructs any provider client.
func validateStartupConfig(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("configuration is required")
	}
	if err := cfg.ValidateLLMPolicy(); err != nil {
		return err
	}
	if !cfg.LLMEnabled {
		return nil
	}
	if policy := cfg.EnforcedLLMPolicy(); policy != nil {
		if err := budget.ValidatePolicyAdmissibility(policy); err != nil {
			return fmt.Errorf("enforced LLM policy cannot admit its configured routes: %w", err)
		}
	}
	return nil
}

type providerBinding struct {
	route    string
	provider llm.Provider
}

type llmStartupPlan struct {
	constructProviders bool
	activateLedger     bool
	startWorkers       bool
}

// planLLMStartup keeps provider construction, persistent-ledger activation,
// and queue workers behind the same LLM_ENABLED gate. Policy mode remains
// observable while LLM is disabled, but it must not require credentials,
// provider clients, budget storage, or claim queued work until LLM traffic is
// enabled.
func planLLMStartup(cfg *config.Config) llmStartupPlan {
	if cfg == nil || !cfg.LLMEnabled {
		return llmStartupPlan{}
	}
	return llmStartupPlan{
		constructProviders: true,
		activateLedger:     cfg.EnforcedLLMPolicy() != nil,
		startWorkers:       true,
	}
}

// buildLLMProviders validates and constructs all provider clients before the
// server opens storage or starts a worker. Enforced policies bind immutable
// primary/fallback slots while legacy deployments retain named providers.
func buildLLMProviders(cfg *config.Config, logger *zap.Logger) ([]providerBinding, error) {
	enforced := cfg.EnforcedLLMPolicy() != nil
	bindings := make([]providerBinding, 0, len(cfg.ProviderRoutes()))
	for route, providerConfig := range cfg.ProviderRoutes() {
		providerName := route
		if providerConfig.ProviderLabel != "" {
			providerName = providerConfig.ProviderLabel
		}
		provider, err := providers.Build(providerName, providerConfig)
		if err != nil {
			if enforced {
				return nil, fmt.Errorf("build enforced %s provider: %w", route, err)
			}
			logger.Warn("failed to build llm provider", zap.String("name", route), zap.Error(err))
			continue
		}
		bindings = append(bindings, providerBinding{route: route, provider: provider})
	}
	return bindings, nil
}

// activateBudgetLedger constructs the persistent hard-budget ledger, reconciles
// every nonterminal entry left by a previous process, and binds it to the
// orchestrator. Reconciliation runs before workers can dispatch so a crashed
// call is charged conservatively rather than silently forgotten.
func activateBudgetLedger(ctx context.Context, cfg *config.Config, s *store.Store, orch *llm.Orchestrator, llmMetrics *llm.Metrics, logger *zap.Logger) (budget.Ledger, error) {
	policy := cfg.EnforcedLLMPolicy()
	if policy == nil {
		return nil, fmt.Errorf("an enforced LLM policy is required to activate the budget ledger")
	}
	limits := budget.LimitsFromPolicy(policy)
	if !limits.Configured() {
		return nil, fmt.Errorf("enforced LLM policy is missing usable global budget caps")
	}
	if err := budget.ValidatePolicyAdmissibility(policy); err != nil {
		return nil, fmt.Errorf("enforced LLM policy cannot admit its configured routes: %w", err)
	}
	ledger := store.NewBudgetLedger(s, limits)

	report, err := ledger.Recover(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconcile budget ledger: %w", err)
	}
	logger.Info("llm budget ledger reconciled",
		zap.Int("inspected", report.Inspected),
		zap.Int("released", report.Released),
		zap.Int("markedUncertain", report.MarkedUncertain),
		zap.String("policyFingerprint", policy.Fingerprint),
	)

	// Released reservations and full uncertain settlements are reflected by
	// the refreshed daily gauges below. The uncertain counter additionally
	// records each possibly-dispatched call reconciled at startup.
	llmMetrics.RecordUncertainUsageCount("startup_reconciliation", report.MarkedUncertain)
	orch.SetBudgetLedger(ledger)
	if err := orch.RefreshBudgetMetrics(ctx); err != nil {
		return nil, fmt.Errorf("publish initial budget metrics: %w", err)
	}
	return ledger, nil
}

func registerLLMProviders(cfg *config.Config, orch *llm.Orchestrator, logger *zap.Logger) error {
	bindings, err := buildLLMProviders(cfg, logger)
	if err != nil {
		return err
	}
	enforced := cfg.EnforcedLLMPolicy() != nil
	for _, binding := range bindings {
		if enforced {
			orch.RegisterProviderAs(binding.route, binding.provider)
			continue
		}
		orch.RegisterProvider(binding.provider)
	}
	return nil
}

// @title AegisCrawler Page Agent Service
// @version 0.1.0
// @description Production-grade task coordination service for AegisCrawler data collection.
// @license.name GPL-3.0-or-later
// @BasePath /
// @securityDefinitions.apikey AdminApiKey
// @in header
// @name Authorization
// @description Provide the admin API key as `Bearer <token>`. Admin endpoints require this header when ADMIN_API_KEY is configured.
func main() {
	os.Exit(runCLI(os.Args[1:]))
}

// runServe starts the HTTP server; it is the default command. configPath is
// the optional -c override for the config file location.
func runServe(configPath string) int {
	applyConfigPath(configPath)
	cfg := config.Load()
	if cfg.ConfigFileErr != nil {
		panic(fmt.Errorf("invalid config file %s: %w", cfg.ConfigFile, cfg.ConfigFileErr))
	}
	if err := validateStartupConfig(cfg); err != nil {
		panic(fmt.Errorf("invalid startup configuration: %w", err))
	}

	logger, err := newLogger(cfg.LogLevel)
	if err != nil {
		panic(err)
	}
	defer logger.Sync()

	if missing := missingSecurityKeys(cfg); len(missing) > 0 {
		logger.Fatal("required security keys are empty", zap.Strings("missing", missing))
	}
	config.WarnIfLegacyPolicyMode(config.SettingLookup(), logger)
	llmPlan := planLLMStartup(cfg)
	var providerBindings []providerBinding
	if llmPlan.constructProviders {
		providerBindings, err = buildLLMProviders(cfg, logger)
		if err != nil {
			logger.Fatal("failed to construct enforced llm provider routes", zap.Error(err))
		}
	}

	s, err := store.NewWithConfig(cfg, cfg.DatabasePath, cfg.VariableEncryptionKey)
	if err != nil {
		logger.Fatal("failed to open database", zap.Error(err))
	}
	defer s.Close()

	metrics := api.NewMetrics()
	sch := scheduler.New(s, cfg, logger, cfg.LeaseDuration, cfg.MaxRetries)
	sch.SetRetentionCallback(func(report store.PurgeReport) {
		for entity, n := range report {
			if n > 0 {
				metrics.IncRowsPurged(entity, n)
			}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sch.Start(ctx, cfg.SweeperInterval)
	sch.StartSchedules(ctx, cfg.ScheduleRunInterval)
	sch.StartRetention(ctx, cfg.RetentionSweepInterval)
	go startDBSizeReporter(ctx, cfg.DatabasePath, cfg.DBSizeMetricsInterval, metrics, logger)

	llmCache, err := cache.NewSQLiteCache(s)
	if err != nil {
		logger.Fatal("failed to create llm cache", zap.Error(err))
	}

	orch := llm.NewOrchestratorWithCache(cfg, llmCache, logger)
	orch.SetMetrics(metrics.LLM())
	// The per-workflow budget envelope is bounded by the same store that owns
	// dsl_workflows. The setter mirrors SetBudgetLedger; legacy mode simply
	// skips workflow-budget admission until the ledger is bound.
	orch.SetWorkflowBudgetLedger(s)
	var budgetLedger budget.Ledger
	if llmPlan.activateLedger {
		for _, binding := range providerBindings {
			orch.RegisterProviderAs(binding.route, binding.provider)
		}
		// The hard budget ledger must exist and be reconciled before any worker
		// can dispatch. Enforced mode fails closed without it.
		budgetLedger, err = activateBudgetLedger(ctx, cfg, s, orch, metrics.LLM(), logger)
		if err != nil {
			logger.Fatal("failed to activate the llm budget ledger", zap.Error(err))
		}
	} else {
		for _, binding := range providerBindings {
			orch.RegisterProvider(binding.provider)
		}
	}
	llmJobManager := llm.NewJobManager(s, cfg, orch, logger)
	llmJobManager.SetMetrics(metrics.LLM())

	intentPredictor := intent.NewPredictor(cfg, orch, logger)
	dslGenerator := dsl.NewGenerator(cfg, orch, logger)
	requirementWorkflow := llmrequirement.NewWorkflow(cfg, orch)
	requirementManager := llmrequirement.NewManager(s, cfg, requirementWorkflow, logger)
	dslWorkflow := dsl.NewDSLWorkflow(cfg, orch)
	dslWorkflowManager := dsl.NewDSLManager(s, cfg, dslWorkflow, logger)
	if llmPlan.startWorkers {
		go llmJobManager.StartWorker(ctx, cfg.LLMJobWorkerInterval)
		go requirementManager.StartWorker(ctx, cfg.LLMJobWorkerInterval)
		go dslWorkflowManager.StartWorker(ctx, cfg.LLMJobWorkerInterval)
	}

	router := api.NewWithDSLWorkflow(s, sch, llmJobManager, intentPredictor, dslGenerator, cfg, logger, metrics, requirementManager, dslWorkflowManager,
		api.WithLLMBudget(budgetLedger, budgetLedger != nil),
		api.WithLLMRuntime(orch))
	server := &http.Server{
		Addr:        cfg.ListenAddr,
		Handler:     router,
		ReadTimeout: 30 * time.Second,
		// WriteTimeout is intentionally disabled; RequestTimeoutMiddleware
		// enforces the configured per-request timeout and can return 504.
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		logger.Info("server starting", zap.String("addr", cfg.ListenAddr))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("server error", zap.Error(err))
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	logger.Info("server shutting down")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", zap.Error(err))
	}
	return 0
}
