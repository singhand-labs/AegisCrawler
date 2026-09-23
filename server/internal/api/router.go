package api

import (
	"net/http"

	_ "github.com/singhand-labs/AegisCrawler/docs"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/llm/dsl"
	"github.com/singhand-labs/AegisCrawler/internal/llm/intent"
	"github.com/singhand-labs/AegisCrawler/internal/mcpapi"
	"github.com/singhand-labs/AegisCrawler/internal/scheduler"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	httpSwagger "github.com/swaggo/http-swagger"
	"go.uber.org/zap"
)

// NewRouter builds the HTTP router.
func NewRouter(h *Handler, cfg *config.Config, logger *zap.Logger, metrics *Metrics) http.Handler {
	h.SetMetrics(metrics)
	mux := http.NewServeMux()

	// Worker-facing endpoints require bearer-token authentication when
	// WORKER_API_KEY is configured. Admin endpoints require bearer-token
	// authentication when ADMIN_API_KEY is configured.
	auth := AuthMiddleware(cfg)
	adminAuth := AdminAuthMiddleware(cfg, logger)
	metricsAuth := MetricsAuthMiddleware(cfg)
	swaggerAuth := SwaggerAuthMiddleware(cfg)
	enhanceRateLimit := AdminEnhanceRateLimitMiddleware(cfg.LLMRateLimitPerSecond, cfg.LLMRateLimitBurst)
	if cfg.AdminAPIKey == "" {
		logger.Warn("ADMIN_API_KEY is not set; admin endpoints are unauthenticated")
	}
	mux.HandleFunc("POST /tasks/claim", auth(http.HandlerFunc(h.ClaimTask)).ServeHTTP)
	mux.HandleFunc("GET /tasks/{id}", auth(http.HandlerFunc(h.GetTask)).ServeHTTP)
	mux.HandleFunc("GET /tasks/{id}/results", auth(http.HandlerFunc(h.GetResults)).ServeHTTP)
	mux.HandleFunc("POST /tasks/{id}/checkpoints", auth(http.HandlerFunc(h.SubmitCheckpoint)).ServeHTTP)
	mux.HandleFunc("GET /tasks/{id}/checkpoints/latest", auth(http.HandlerFunc(h.GetCheckpoint)).ServeHTTP)
	mux.HandleFunc("POST /tasks/{id}/human-interventions", auth(http.HandlerFunc(h.CreateHumanIntervention)).ServeHTTP)
	mux.HandleFunc("GET /tasks/{id}/human-interventions/{interventionId}", auth(http.HandlerFunc(h.GetHumanInterventionDecision)).ServeHTTP)
	mux.HandleFunc("GET /rules/{id}", auth(http.HandlerFunc(h.GetRule)).ServeHTTP)
	mux.HandleFunc("POST /results", auth(http.HandlerFunc(h.SubmitResult)).ServeHTTP)
	mux.HandleFunc("POST /logs", auth(http.HandlerFunc(h.SubmitLog)).ServeHTTP)
	mux.HandleFunc("POST /heartbeat", auth(http.HandlerFunc(h.SubmitHeartbeat)).ServeHTTP)
	mux.HandleFunc("POST /status", auth(http.HandlerFunc(h.SubmitStatus)).ServeHTTP)
	mux.HandleFunc("POST /snapshots", auth(http.HandlerFunc(h.SubmitSnapshot)).ServeHTTP)

	mux.HandleFunc("GET /metrics", metricsAuth(http.HandlerFunc(h.ServeMetrics)).ServeHTTP)

	// OpenAPI UI and spec
	mux.HandleFunc("GET /health", h.Health)
	mux.HandleFunc("GET /api/v1/capabilities", h.Capabilities)
	mux.HandleFunc("POST /api/v1/recordings", adminAuth(http.HandlerFunc(h.CreateRecording)).ServeHTTP)
	mux.HandleFunc("GET /api/v1/recordings/{id}", adminAuth(http.HandlerFunc(h.GetRecording)).ServeHTTP)
	mux.HandleFunc("DELETE /api/v1/recordings/{id}", adminAuth(http.HandlerFunc(h.DeleteRecording)).ServeHTTP)
	mux.HandleFunc("POST /api/v1/recordings/{id}/requirement-jobs", adminAuth(enhanceRateLimit(IdempotencyMiddleware(h.store, DefaultIdempotencyTTL, http.HandlerFunc(h.CreateCandidateRequirementJob)))).ServeHTTP)
	mux.HandleFunc("POST /api/v1/recordings/{id}/requirement-jobs/normalize", adminAuth(enhanceRateLimit(IdempotencyMiddleware(h.store, DefaultIdempotencyTTL, http.HandlerFunc(h.CreateNormalizationRequirementJob)))).ServeHTTP)
	mux.HandleFunc("GET /api/v1/requirement-jobs/{id}", adminAuth(http.HandlerFunc(h.GetRequirementJob)).ServeHTTP)
	mux.HandleFunc("GET /api/v1/requirement-jobs/{id}/provider-attempts", adminAuth(http.HandlerFunc(h.ListRequirementProviderAttempts)).ServeHTTP)
	mux.HandleFunc("GET /api/v1/requirement-jobs/{id}/provider-attempts/{attempt}", adminAuth(enhanceRateLimit(http.HandlerFunc(h.GetRequirementProviderAttempt))).ServeHTTP)
	mux.HandleFunc("GET /api/v1/requirement-jobs/{id}/provider-attempts/{attempt}/calls/{callId}/content", adminAuth(enhanceRateLimit(http.HandlerFunc(h.GetRequirementProviderCallContent))).ServeHTTP)
	mux.HandleFunc("POST /api/v1/requirement-jobs/{id}/retry", adminAuth(IdempotencyMiddleware(h.store, DefaultIdempotencyTTL, http.HandlerFunc(h.RetryRequirementJob))).ServeHTTP)
	mux.HandleFunc("GET /api/v1/requirements/{id}", adminAuth(http.HandlerFunc(h.GetCollectionRequirement)).ServeHTTP)
	mux.HandleFunc("POST /api/v1/requirements/{id}/confirm", adminAuth(http.HandlerFunc(h.ConfirmCollectionRequirement)).ServeHTTP)
	mux.HandleFunc("POST /api/v1/requirements/{id}/dsl-workflows", adminAuth(enhanceRateLimit(IdempotencyMiddleware(h.store, DefaultIdempotencyTTL, http.HandlerFunc(h.CreateDSLWorkflow)))).ServeHTTP)
	mux.HandleFunc("POST /api/v1/requirements/{id}/dsl-workflows/adopt-attempt-export", adminAuth(enhanceRateLimit(http.HandlerFunc(h.AdoptDSLAttemptExport))).ServeHTTP)
	mux.HandleFunc("GET /api/v1/dsl-workflows/{id}", adminAuth(http.HandlerFunc(h.GetDSLWorkflow)).ServeHTTP)
	mux.HandleFunc("PUT /api/v1/dsl-workflows/{id}/provisional", adminAuth(http.HandlerFunc(h.CorrectDSLWorkflow)).ServeHTTP)
	mux.HandleFunc("GET /api/v1/dsl-jobs/{id}", adminAuth(http.HandlerFunc(h.GetDSLJob)).ServeHTTP)
	mux.HandleFunc("GET /api/v1/dsl-jobs/{id}/provider-attempts", adminAuth(http.HandlerFunc(h.ListDSLProviderAttempts)).ServeHTTP)
	mux.HandleFunc("GET /api/v1/dsl-jobs/{id}/provider-attempts/{attempt}", adminAuth(enhanceRateLimit(http.HandlerFunc(h.GetDSLProviderAttempt))).ServeHTTP)
	mux.HandleFunc("GET /api/v1/dsl-jobs/{id}/provider-attempts/{attempt}/calls/{callId}/content", adminAuth(enhanceRateLimit(http.HandlerFunc(h.GetDSLProviderCallContent))).ServeHTTP)
	mux.HandleFunc("POST /api/v1/dsl-workflows/{id}/replays", adminAuth(IdempotencyMiddleware(h.store, DefaultIdempotencyTTL, http.HandlerFunc(h.StartDSLReplay))).ServeHTTP)
	mux.HandleFunc("GET /api/v1/dsl-replays/{id}", adminAuth(http.HandlerFunc(h.GetDSLReplay)).ServeHTTP)
	mux.HandleFunc("POST /api/v1/dsl-workflows/{id}/replays/{replayId}/complete", adminAuth(http.HandlerFunc(h.CompleteDSLReplay)).ServeHTTP)
	mux.HandleFunc("POST /api/v1/dsl-workflows/{id}/confirm", adminAuth(http.HandlerFunc(h.ConfirmDSLWorkflow)).ServeHTTP)
	mux.HandleFunc("GET /api/v1/tasks/{id}/results", adminAuth(http.HandlerFunc(h.GetVersionedTaskResults)).ServeHTTP)
	mux.HandleFunc("POST /admin/rules", adminAuth(http.HandlerFunc(h.CreateRule)).ServeHTTP)
	mux.HandleFunc("GET /admin/rules", adminAuth(http.HandlerFunc(h.ListRules)).ServeHTTP)
	mux.HandleFunc("GET /admin/rules/{id}", adminAuth(http.HandlerFunc(h.AdminGetRule)).ServeHTTP)
	mux.HandleFunc("PATCH /admin/rules/{id}", adminAuth(http.HandlerFunc(h.UpdateRule)).ServeHTTP)
	mux.HandleFunc("DELETE /admin/rules/{id}", adminAuth(http.HandlerFunc(h.DeleteRule)).ServeHTTP)
	mux.HandleFunc("POST /admin/rules/{id}/approve", adminAuth(http.HandlerFunc(h.ApproveRule)).ServeHTTP)
	mux.HandleFunc("POST /admin/rules/{id}/reject", adminAuth(http.HandlerFunc(h.RejectRule)).ServeHTTP)
	mux.HandleFunc("POST /admin/rules/{id}/versions", adminAuth(http.HandlerFunc(h.CreateRuleVersion)).ServeHTTP)
	mux.HandleFunc("GET /admin/rules/{id}/versions", adminAuth(http.HandlerFunc(h.ListRuleVersions)).ServeHTTP)
	mux.HandleFunc("GET /admin/rules/{id}/versions/{version}", adminAuth(http.HandlerFunc(h.GetRuleVersion)).ServeHTTP)
	mux.HandleFunc("POST /admin/rules/{id}/versions/{version}/approve", adminAuth(http.HandlerFunc(h.ApproveRuleVersion)).ServeHTTP)
	mux.HandleFunc("POST /admin/rules/{id}/versions/{version}/reject", adminAuth(http.HandlerFunc(h.RejectRuleVersion)).ServeHTTP)
	mux.HandleFunc("POST /admin/rules/predict-intent", adminAuth(enhanceRateLimit(http.HandlerFunc(h.PredictIntent))).ServeHTTP)
	mux.HandleFunc("POST /admin/rules/generate-from-intent", adminAuth(enhanceRateLimit(http.HandlerFunc(h.GenerateFromIntent))).ServeHTTP)
	mux.HandleFunc("POST /admin/rules/enhance", adminAuth(enhanceRateLimit(http.HandlerFunc(h.EnhanceRule))).ServeHTTP)
	mux.HandleFunc("GET /admin/rules/enhancements/jobs/{id}", adminAuth(http.HandlerFunc(h.GetLLMJob)).ServeHTTP)
	// Read-only LLM policy and hard-budget state. There is no v1 endpoint to
	// mutate policy, alter history, refund, release, or override a denial.
	mux.HandleFunc("GET /admin/llm/policy", adminAuth(http.HandlerFunc(h.GetLLMPolicy)).ServeHTTP)
	mux.HandleFunc("GET /admin/llm/budget", adminAuth(http.HandlerFunc(h.GetLLMBudget)).ServeHTTP)
	mux.HandleFunc("GET /admin/rules/{id}/enhancement", adminAuth(http.HandlerFunc(h.GetRuleEnhancement)).ServeHTTP)
	mux.HandleFunc("POST /admin/rules/{id}/enhancement/accept", adminAuth(http.HandlerFunc(h.AcceptEnhancement)).ServeHTTP)
	mux.HandleFunc("POST /admin/rules/{id}/enhancement/reject", adminAuth(http.HandlerFunc(h.RejectEnhancement)).ServeHTTP)
	mux.HandleFunc("POST /admin/tasks", adminAuth(http.HandlerFunc(h.CreateTask)).ServeHTTP)
	mux.HandleFunc("GET /admin/tasks", adminAuth(http.HandlerFunc(h.ListTasks)).ServeHTTP)
	mux.HandleFunc("GET /admin/tasks/{id}", adminAuth(http.HandlerFunc(h.GetTask)).ServeHTTP)
	mux.HandleFunc("GET /admin/tasks/{id}/results", adminAuth(http.HandlerFunc(h.GetAdminResults)).ServeHTTP)
	mux.HandleFunc("GET /admin/tasks/{id}/logs", adminAuth(http.HandlerFunc(h.GetTaskLogs)).ServeHTTP)
	mux.HandleFunc("POST /admin/tasks/{id}/cancel", adminAuth(http.HandlerFunc(h.CancelTask)).ServeHTTP)
	mux.HandleFunc("POST /admin/tasks/{id}/retry", adminAuth(http.HandlerFunc(h.RetryTask)).ServeHTTP)
	mux.HandleFunc("GET /admin/tasks/{id}/human-interventions", adminAuth(http.HandlerFunc(h.ListTaskHumanInterventions)).ServeHTTP)
	mux.HandleFunc("POST /admin/tasks/{id}/human-interventions/{interventionId}/decision", adminAuth(http.HandlerFunc(h.DecideHumanIntervention)).ServeHTTP)
	mux.HandleFunc("GET /admin/audit_logs", adminAuth(http.HandlerFunc(h.ListAuditLogs)).ServeHTTP)
	mux.HandleFunc("POST /admin/schedules", adminAuth(http.HandlerFunc(h.CreateSchedule)).ServeHTTP)
	mux.HandleFunc("GET /admin/schedules", adminAuth(http.HandlerFunc(h.ListSchedules)).ServeHTTP)
	mux.HandleFunc("GET /admin/schedules/preview", adminAuth(http.HandlerFunc(h.PreviewSchedule)).ServeHTTP)
	mux.HandleFunc("GET /admin/schedules/{id}", adminAuth(http.HandlerFunc(h.GetSchedule)).ServeHTTP)
	mux.HandleFunc("PATCH /admin/schedules/{id}", adminAuth(http.HandlerFunc(h.UpdateSchedule)).ServeHTTP)
	mux.HandleFunc("DELETE /admin/schedules/{id}", adminAuth(http.HandlerFunc(h.DeleteSchedule)).ServeHTTP)
	mux.HandleFunc("POST /admin/schedules/{id}/trigger", adminAuth(http.HandlerFunc(h.TriggerSchedule)).ServeHTTP)
	mux.HandleFunc("POST /admin/mcp/tokens", adminAuth(http.HandlerFunc(h.CreateMCPToken)).ServeHTTP)
	mux.HandleFunc("GET /admin/mcp/tokens", adminAuth(http.HandlerFunc(h.ListMCPTokens)).ServeHTTP)
	mux.HandleFunc("DELETE /admin/mcp/tokens/{id}", adminAuth(http.HandlerFunc(h.RevokeMCPToken)).ServeHTTP)
	if cfg.MCPEnabled {
		mux.Handle("/mcp", mcpapi.New(h.store, cfg, logger))
	}

	// Admin UI static files (unauthenticated; login is handled client-side).
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
	})
	mux.Handle("/admin/", AdminUIHandler())

	// OpenAPI UI and spec
	mux.Handle("GET /swagger/", swaggerAuth(httpSwagger.WrapHandler))
	mux.Handle("GET /swagger.json", swaggerAuth(http.FileServer(http.Dir("./docs"))))
	mux.Handle("GET /swagger.yaml", swaggerAuth(http.FileServer(http.Dir("./docs"))))

	// Middleware order (outermost first):
	// admin UI SPA fallback -> global rate limit -> metrics -> circuit breaker -> per-worker/site rate limits -> logging -> recovery -> timeout -> body size -> gzip request decompression -> mux
	var handler http.Handler = mux
	handler = GzipRequestMiddleware(cfg.MaxRequestBodyBytes)(handler)
	handler = MaxBodySizeMiddleware(cfg.MaxRequestBodyBytes)(handler)
	handler = RequestTimeoutMiddleware(cfg.RequestTimeout, logger)(handler)
	handler = RecoveryMiddleware(logger)(handler)
	handler = LoggingMiddleware(logger)(handler)
	handler = SiteRateLimitMiddleware(cfg.SiteRateLimitPerSecond, cfg.SiteRateLimitBurst, h.store, cfg.SiteRateLimitCacheTTL)(handler)
	handler = WorkerRateLimitMiddleware(cfg.WorkerRateLimitPerSecond, cfg.WorkerRateLimitBurst)(handler)
	handler = CircuitBreakerMiddleware(cfg)(handler)
	handler = MetricsCollectorMiddleware(metrics)(handler)
	handler = RateLimitMiddleware(cfg.RateLimitPerSecond, cfg.RateLimitBurst)(handler)
	handler = AdminUISPAFallbackMiddleware(handler)
	return handler
}

// New creates handler and router.
func New(s *store.Store, sch *scheduler.Scheduler, llmJobManager *llm.JobManager, intentPredictor *intent.Predictor, dslGenerator *dsl.Generator, cfg *config.Config, logger *zap.Logger, metrics *Metrics, requirementManagers ...requirementManager) http.Handler {
	h := NewHandler(s, sch, llmJobManager, intentPredictor, cfg, logger)
	if len(requirementManagers) > 0 {
		h.SetRequirementManager(requirementManagers[0])
	}
	if dslGenerator != nil {
		h.SetDSLGenerator(dslGenerator)
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	h.SetMetrics(metrics)
	return NewRouter(h, cfg, logger, metrics)
}

// RouterOption customizes the production router before it starts serving.
type RouterOption func(*Handler)

// WithLLMBudget binds the active hard-budget ledger and records that startup
// reconciliation completed, so the read-only Admin endpoints and enforced
// readiness can report real state.
func WithLLMBudget(ledger budget.Ledger, reconciled bool) RouterOption {
	return func(h *Handler) { h.SetLLMBudget(ledger, reconciled) }
}

// WithLLMRuntime binds constructed/open route state for enforced readiness and
// policy production eligibility.
func WithLLMRuntime(runtime interface {
	RouteReady(route string) (bool, string)
}) RouterOption {
	return func(h *Handler) { h.SetLLMRuntime(runtime) }
}

// NewWithDSLWorkflow builds the production router with both durable workflow managers.
func NewWithDSLWorkflow(s *store.Store, sch *scheduler.Scheduler, llmJobManager *llm.JobManager, intentPredictor *intent.Predictor, dslGenerator *dsl.Generator, cfg *config.Config, logger *zap.Logger, metrics *Metrics, requirements requirementManager, workflows dslWorkflowManager, opts ...RouterOption) http.Handler {
	h := NewHandler(s, sch, llmJobManager, intentPredictor, cfg, logger)
	h.SetRequirementManager(requirements)
	h.SetDSLWorkflowManager(workflows)
	if dslGenerator != nil {
		h.SetDSLGenerator(dslGenerator)
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	h.SetMetrics(metrics)
	for _, opt := range opts {
		if opt != nil {
			opt(h)
		}
	}
	return NewRouter(h, cfg, logger, metrics)
}
