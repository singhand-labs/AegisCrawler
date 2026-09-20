package llm

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/llm/cache"
	"github.com/singhand-labs/AegisCrawler/internal/llm/redact"
	"go.uber.org/zap"
)

// Orchestrator routes completion requests to registered providers with retry,
// optional fallback provider support, and a pluggable response cache keyed by
// the full CompletionRequest.
type Orchestrator struct {
	cfg               *config.Config
	providers         map[string]Provider
	providersMu       sync.RWMutex
	routeMu           sync.RWMutex
	closedRoute       map[string]string
	cache             cache.Cache
	logger            *zap.Logger
	metrics           *Metrics
	ledger            budget.Ledger
	ledgerMu          sync.RWMutex
	workflowLedger    budget.WorkflowBudgetLedger
	workflowLedgerMu  sync.RWMutex
	budgetMetricsGate chan struct{}
	now               func() time.Time
}

// NewOrchestrator creates an Orchestrator instance with an in-memory cache.
// This is convenient for unit tests; production code should use
// NewOrchestratorWithCache with a persistent cache.
func NewOrchestrator(cfg *config.Config, logger *zap.Logger) *Orchestrator {
	return NewOrchestratorWithCache(cfg, newMemoryCache(), logger)
}

// NewOrchestratorWithCache creates an Orchestrator backed by the supplied cache.
// A nil cache falls back to a small in-memory cache so Complete always has a
// cache layer available.
func NewOrchestratorWithCache(cfg *config.Config, c cache.Cache, logger *zap.Logger) *Orchestrator {
	if c == nil {
		c = newMemoryCache()
	}
	return &Orchestrator{
		cfg:               cfg,
		providers:         map[string]Provider{},
		closedRoute:       map[string]string{},
		cache:             c,
		logger:            logger,
		budgetMetricsGate: make(chan struct{}, 1),
		now:               func() time.Time { return time.Now().UTC() },
	}
}

// SetBudgetLedger binds the persistent hard-budget ledger. Enforced mode fails
// closed until a ledger is bound, so no physical call can be dispatched without
// a committed worst-case reservation.
func (o *Orchestrator) SetBudgetLedger(ledger budget.Ledger) {
	o.ledgerMu.Lock()
	o.ledger = ledger
	o.ledgerMu.Unlock()
}

// SetWorkflowBudgetLedger binds the per-workflow envelope ledger. When unset,
// workflow-budget admission is skipped (legacy behavior). When set, each DSL
// physical call is gated against the workflow's persisted envelope before any
// provider network call begins.
func (o *Orchestrator) SetWorkflowBudgetLedger(ledger budget.WorkflowBudgetLedger) {
	o.workflowLedgerMu.Lock()
	o.workflowLedger = ledger
	o.workflowLedgerMu.Unlock()
}

func (o *Orchestrator) workflowBudgetLedger() budget.WorkflowBudgetLedger {
	o.workflowLedgerMu.RLock()
	defer o.workflowLedgerMu.RUnlock()
	return o.workflowLedger
}

func (o *Orchestrator) budgetLedger() budget.Ledger {
	o.ledgerMu.RLock()
	defer o.ledgerMu.RUnlock()
	return o.ledger
}

// SetClock overrides the orchestrator clock used to resolve the UTC budget day.
// It exists for deterministic rollover tests.
func (o *Orchestrator) SetClock(now func() time.Time) {
	if now != nil {
		o.now = now
	}
}

func (o *Orchestrator) budgetDay() string {
	if o.now == nil {
		return budget.BudgetDay(time.Now().UTC())
	}
	return budget.BudgetDay(o.now())
}

// RegisterProvider registers a provider for routing. It is safe for concurrent
// use with Complete.
func (o *Orchestrator) RegisterProvider(p Provider) {
	o.RegisterProviderAs(p.Name(), p)
}

// RegisterProviderAs binds a provider to a routing slot. The slot can differ
// from Provider.Name when an enforced policy keeps stable primary/fallback
// routing while preserving the provider's billing provenance.
func (o *Orchestrator) RegisterProviderAs(route string, p Provider) {
	o.providersMu.Lock()
	o.providers[route] = p
	o.providersMu.Unlock()
}

// RouteReady reports whether an enforced route is constructed and remains
// open. Routes are closed only in process memory and reset on restart after an
// operator has corrected the production price/usage contract.
func (o *Orchestrator) RouteReady(route string) (bool, string) {
	o.providersMu.RLock()
	provider, constructed := o.providers[route]
	o.providersMu.RUnlock()
	if !constructed || provider == nil {
		return false, "not_constructed"
	}
	if o.cfg != nil {
		if policy := o.cfg.EnforcedLLMPolicy(); policy != nil {
			expectedProvider := ""
			switch route {
			case "primary":
				expectedProvider = policy.Primary.Provider
			case "fallback":
				if policy.Fallback != nil {
					expectedProvider = policy.Fallback.Provider
				}
			}
			if expectedProvider == "" || SanitizeCompletionMetadata(provider.Name()) != expectedProvider {
				return false, "provider_contract_mismatch"
			}
			if _, ok := provider.(InputTokenUpperBounder); !ok {
				return false, "input_bound_unavailable"
			}
		}
	}
	o.routeMu.RLock()
	reason := o.closedRoute[route]
	o.routeMu.RUnlock()
	if reason != "" {
		return false, reason
	}
	return true, ""
}

func (o *Orchestrator) closeRoute(route, reason string) {
	o.routeMu.Lock()
	if o.closedRoute[route] == "" {
		o.closedRoute[route] = reason
	}
	o.routeMu.Unlock()
}

// preparedProvider returns the exact provider instance whose request was
// bounded before admission. Enforced dispatch keeps this instance through the
// network call so a concurrent registry replacement cannot change the wire
// adapter after reservation.
func (o *Orchestrator) preparedProvider(prepared PreparedCall) (Provider, error) {
	o.providersMu.RLock()
	provider, ok := o.providers[prepared.Route]
	o.providersMu.RUnlock()
	if !ok || provider == nil {
		return nil, &RouteUnavailableError{Route: prepared.Route, Reason: "not_constructed"}
	}
	if providerName := SanitizeCompletionMetadata(provider.Name()); providerName != prepared.Provider {
		return nil, &RouteUnavailableError{Route: prepared.Route, Reason: "provider_contract_mismatch"}
	}
	return provider, nil
}

// SetMetrics assigns the LLM metrics collector to the orchestrator.
func (o *Orchestrator) SetMetrics(m *Metrics) {
	o.metrics = m
}

func cacheKey(req CompletionRequest) string {
	return CanonicalCompletionRequestHash(req)
}

// CompletionResult wraps the raw provider response with cache metadata.
type CompletionResult struct {
	*CompletionResponse
	CacheHit bool
	Provider string
	Model    string
}

// PeekCache looks up a cached completion for req without invoking a provider.
// It returns the cached result and true when a fresh entry exists. Cache errors
// are logged and treated as misses.
func (o *Orchestrator) PeekCache(ctx context.Context, req CompletionRequest) (*CompletionResult, bool) {
	start := time.Now()
	key := cacheKey(req)
	provider := o.cfg.LLMProvider
	model := modelFromRequest(req, o.cfg)
	if o.cfg.EnforcedLLMPolicy() != nil {
		prepared, err := o.PrepareCall("primary", req)
		if err != nil {
			return nil, false
		}
		if _, err := dispatchIdentity(ctx, budget.OrdinalPrimary); err != nil {
			return nil, false
		}
		if ready, _ := o.RouteReady(prepared.Route); !ready {
			return nil, false
		}
		key = preparedCacheKey(prepared)
		provider = prepared.Provider
		model = prepared.Model
		req = prepared.Request
	}
	cached, hit, err := o.cache.Get(ctx, key)
	if err != nil {
		o.logger.Warn("llm cache get failed", zap.Error(err))
		return nil, false
	}
	if !hit {
		return nil, false
	}
	cachedProvider := cached.Provider
	if cachedProvider == "" {
		cachedProvider = provider
	}
	cachedModel := cached.Model
	if cachedModel == "" {
		cachedModel = model
	}
	result := &CompletionResult{
		CompletionResponse: &CompletionResponse{
			Content:      cached.Content,
			InputTokens:  cached.InputTokens,
			OutputTokens: cached.OutputTokens,
			ResponseID:   cached.ResponseID,
			FinishReason: cached.FinishReason,
		},
		CacheHit: true,
		Provider: cachedProvider,
		Model:    cachedModel,
	}
	o.recordMetrics(cachedProvider, cachedModel, time.Since(start), cached.InputTokens, cached.OutputTokens, true, nil)
	return result, true
}

// Complete executes a completion request against the configured primary
// provider, falling back to LLMFallbackProvider if configured and the primary
// fails. Responses are cached by a SHA256 key of the marshaled request.
func (o *Orchestrator) Complete(ctx context.Context, req CompletionRequest) (*CompletionResult, error) {
	if o.cfg.EnforcedLLMPolicy() != nil {
		return o.completeEnforced(ctx, req, true)
	}
	return o.complete(ctx, req, true)
}

// CompleteOnce checks the normal exact-request cache and, on a miss, makes at
// most one physical call to the configured primary provider. It deliberately
// disables both provider retries and fallback and is reserved for workflows
// whose durable dispatch marker makes ambiguous redispatch unsafe.
func (o *Orchestrator) CompleteOnce(ctx context.Context, req CompletionRequest) (*CompletionResult, error) {
	if o.cfg.EnforcedLLMPolicy() != nil {
		return o.completeEnforced(ctx, req, false)
	}
	req.ExecutionPolicy = CompletionExecutionAtMostOnce
	return o.complete(ctx, req, false)
}

// PrepareCall applies the immutable primary or fallback route from an
// enforced policy. Caller-selected model, temperature, output cap, and
// execution policy are intentionally replaced.
func (o *Orchestrator) PrepareCall(route string, req CompletionRequest) (PreparedCall, error) {
	policy := o.cfg.EnforcedLLMPolicy()
	if policy == nil {
		return PreparedCall{}, fmt.Errorf("an enforced LLM policy is required")
	}
	routePolicy := policy.Primary
	switch route {
	case "primary":
	case "fallback":
		if policy.Fallback == nil {
			return PreparedCall{}, fmt.Errorf("fallback route is not configured")
		}
		routePolicy = *policy.Fallback
	default:
		return PreparedCall{}, fmt.Errorf("unknown enforced route %q", route)
	}
	preparedRequest := cloneCompletionRequest(req)
	preparedRequest.Model = routePolicy.Model
	preparedRequest.Temperature = routePolicy.Temperature
	preparedRequest.MaxOutputTokens = routePolicy.MaxOutputTokens
	preparedRequest.ExecutionPolicy = CompletionExecutionAtMostOnce
	return PreparedCall{
		Route:                    route,
		Provider:                 routePolicy.Provider,
		Model:                    routePolicy.Model,
		Endpoint:                 routePolicy.BaseURL,
		PolicyFingerprint:        policy.Fingerprint,
		PriceRevision:            routePolicy.PriceRevision,
		RequestHash:              CanonicalCompletionRequestHash(preparedRequest),
		Request:                  preparedRequest,
		MaxInputTokens:           routePolicy.MaxInputTokens,
		MaxOutputTokens:          routePolicy.MaxOutputTokens,
		InputUSDPerMillion:       routePolicy.InputUSDPerMillion,
		CachedInputUSDPerMillion: routePolicy.CachedInputUSDPerMillion,
		OutputUSDPerMillion:      routePolicy.OutputUSDPerMillion,
	}, nil
}

func preparedCacheKey(call PreparedCall) string {
	payload := call.PolicyFingerprint + "\x00" + call.Route + "\x00" + call.Provider + "\x00" +
		call.Model + "\x00" + call.Endpoint + "\x00" + call.PriceRevision + "\x00" + call.RequestHash
	return fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
}

func (o *Orchestrator) completeEnforced(ctx context.Context, req CompletionRequest, allowFallback bool) (*CompletionResult, error) {
	start := time.Now()
	primary, err := o.PrepareCall("primary", req)
	if err != nil {
		return nil, markProviderDispatchNotStarted(err)
	}
	// Resolve ownership before cache access. A zero-cost cache hit is still an
	// authenticated logical operation and must never be served to an
	// unattributable background call.
	primaryIdentity, err := dispatchIdentity(ctx, budget.OrdinalPrimary)
	if err != nil {
		return nil, markProviderDispatchNotStarted(err)
	}
	if ready, reason := o.RouteReady(primary.Route); !ready {
		return nil, markProviderDispatchNotStarted(
			&RouteUnavailableError{Route: primary.Route, Reason: reason},
		)
	}
	key := preparedCacheKey(primary)
	cached, hit, cacheErr := o.cache.Get(ctx, key)
	if cacheErr != nil {
		o.logger.Warn("llm cache get failed", zap.String("error", SanitizeCompletionError(cacheErr)))
	} else if hit {
		provider := cached.Provider
		if provider == "" {
			provider = primary.Provider
		}
		model := cached.Model
		if model == "" {
			model = primary.Model
		}
		result := &CompletionResult{CompletionResponse: &CompletionResponse{
			Content: cached.Content, InputTokens: cached.InputTokens, OutputTokens: cached.OutputTokens,
			ResponseID: cached.ResponseID, FinishReason: cached.FinishReason,
		}, CacheHit: true, Provider: provider, Model: model}
		traceCtx := withCompletionDispatchLineage(ctx, primary, primaryIdentity)
		if err := TraceCacheHit(traceCtx, primary.Request, result); err != nil {
			return nil, err
		}
		o.logger.Info("llm enforced cache hit",
			append(o.dispatchLogFields(primary, primaryIdentity),
				zap.String("transition", "cache_hit"),
				zap.Bool("physicalCall", false))...,
		)
		o.recordMetrics(provider, model, time.Since(start), cached.InputTokens, cached.OutputTokens, true, nil)
		return result, nil
	}

	// Every cache-miss physical call is admitted independently against the
	// hard budget before it reaches the provider network boundary.
	used := primary
	resp, err := o.dispatchAdmitted(ctx, primary, budget.OrdinalPrimary)
	if err != nil && allowFallback && IsAvailabilityError(err) {
		fallback, prepareErr := o.PrepareCall("fallback", req)
		if prepareErr != nil {
			return nil, err
		}
		o.logger.Warn("primary llm unavailable, trying enforced fallback",
			zap.String("error", SanitizeCompletionError(err)), zap.String("fallback", fallback.Provider))
		used = fallback
		fallbackResp, fallbackErr := o.dispatchAdmitted(ctx, fallback, budget.OrdinalFallback)
		if fallbackErr != nil && (budget.IsDenied(fallbackErr) || budget.IsUnavailable(fallbackErr)) {
			// A fallback denied by the remaining budget is terminal. The primary
			// availability failure remains the reported cause.
			o.logger.Warn("enforced fallback admission refused",
				zap.String("error", SanitizeCompletionError(fallbackErr)))
			return nil, fallbackErr
		}
		resp, err = fallbackResp, fallbackErr
	}
	if err != nil {
		return nil, err
	}
	if cachedContent, cacheSafe := cacheSafeCompletionContent(resp.Content); o.cfg.LLMCacheTTL > 0 &&
		used.Route == primary.Route && cacheSafe {
		if err := o.cache.Set(ctx, key, &cache.Entry{
			Content: cachedContent, InputTokens: resp.InputTokens, OutputTokens: resp.OutputTokens,
			ResponseID: SanitizeCompletionMetadata(resp.ResponseID), FinishReason: SanitizeCompletionMetadata(resp.FinishReason),
			Provider: SanitizeCompletionMetadata(used.Provider), Model: SanitizeCompletionMetadata(used.Model),
		}, o.cfg.LLMCacheTTL); err != nil {
			o.logger.Warn("llm cache set failed", zap.String("error", SanitizeCompletionError(err)))
		}
	} else if o.cfg.LLMCacheTTL > 0 && used.Route == primary.Route && !cacheSafe {
		o.logger.Warn("llm response not cached because sensitive content was detected")
	} else if o.cfg.LLMCacheTTL > 0 && used.Route != primary.Route {
		// The primary cache key cannot reconstruct a fallback route's distinct
		// model, request hash, price revision, and physical ordinal. Keep
		// fallback completions uncached rather than emit false cache lineage.
		o.logger.Info("llm fallback response not cached to preserve dispatch lineage",
			zap.String("route", used.Route),
			zap.String("policyFingerprint", used.PolicyFingerprint),
			zap.String("priceRevision", used.PriceRevision),
			zap.String("requestHash", used.RequestHash),
		)
	}
	return &CompletionResult{CompletionResponse: resp, Provider: used.Provider, Model: used.Model}, nil
}

// dispatchAdmitted performs one admitted physical call:
//
//  1. resolve the authenticated dispatch identity,
//  2. reserve the worst-case cost and commit before any network call,
//  3. persist the possibly-dispatched marker,
//  4. send exactly once, and
//  5. settle trusted usage, or the full reservation when usage is uncertain.
//
// It never retries the provider and never sends a request for an identity that
// already exists.
func (o *Orchestrator) dispatchAdmitted(ctx context.Context, prepared PreparedCall, ordinal int) (*CompletionResponse, error) {
	if ready, reason := o.RouteReady(prepared.Route); !ready {
		return nil, markProviderDispatchNotStarted(
			&RouteUnavailableError{Route: prepared.Route, Reason: reason},
		)
	}
	ledger := o.budgetLedger()
	if ledger == nil {
		// Enforced mode must not dispatch without a hard budget boundary.
		return nil, markProviderDispatchNotStarted(
			fmt.Errorf("%w: enforced LLM policy requires a budget ledger", budget.ErrLedgerUnavailable),
		)
	}
	identity, err := dispatchIdentity(ctx, ordinal)
	if err != nil {
		return nil, markProviderDispatchNotStarted(err)
	}
	provider, err := o.preparedProvider(prepared)
	if err != nil {
		return nil, markProviderDispatchNotStarted(err)
	}
	bounder, ok := provider.(InputTokenUpperBounder)
	if !ok {
		return nil, markProviderDispatchNotStarted(fmt.Errorf(
			"%w: %s route adapter cannot prove an input-token upper bound",
			ErrRouteUnavailable,
			prepared.Route,
		))
	}
	inputUpperBound, err := bounder.InputTokenUpperBound(prepared.Request)
	if err != nil {
		return nil, markProviderDispatchNotStarted(fmt.Errorf(
			"%w: prepare %s route input-token bound: %v",
			ErrRouteUnavailable,
			prepared.Route,
			err,
		))
	}
	if inputUpperBound < 0 || inputUpperBound > prepared.MaxInputTokens {
		return nil, markProviderDispatchNotStarted(&InputTokenLimitError{
			Route:      prepared.Route,
			UpperBound: inputUpperBound,
			Maximum:    prepared.MaxInputTokens,
		})
	}
	rates := preparedRates(prepared)
	caps := preparedCaps(prepared)
	reserved, err := budget.Reservation(rates, caps)
	if err != nil {
		return nil, markProviderDispatchNotStarted(err)
	}
	budgetDay := o.budgetDay()

	entry, err := ledger.Reserve(ctx, budget.ReserveRequest{
		Identity:          identity,
		BudgetDay:         budgetDay,
		RouteSlot:         prepared.Route,
		Provider:          prepared.Provider,
		Model:             prepared.Model,
		Endpoint:          prepared.Endpoint,
		PolicyFingerprint: prepared.PolicyFingerprint,
		PriceRevision:     prepared.PriceRevision,
		RequestHash:       prepared.RequestHash,
		Rates:             rates,
		Caps:              caps,
		Reserved:          reserved,
	})
	if err != nil {
		// A duplicate identity must never be redispatched, and a denial or
		// ledger failure never permits a provider call.
		o.recordAdmissionFailure(prepared, identity, err)
		return nil, markProviderDispatchNotStarted(err)
	}
	if entry != nil && entry.BudgetDay != "" {
		// The ledger chooses the authoritative UTC day only after acquiring its
		// write lock. Use that persisted day for all later metric snapshots even
		// when admission waited across midnight.
		budgetDay = entry.BudgetDay
	}
	o.logger.Info("llm budget transition",
		append(o.dispatchLogFields(prepared, identity),
			zap.String("transition", "reserve"),
			zap.String("state", budget.StateReserved),
			zap.Int64("amountUsdNanos", int64(reserved)))...,
	)
	o.observeBudgetSnapshot(ctx, ledger)

	// Per-workflow envelope: DSL calls only. The reserved amount is the
	// worst-case estimate. Admission denial releases the reservation (the
	// dispatch marker has not yet been persisted) and surfaces the
	// *DeniedError so classifyDSLJobError can mark the job terminal.
	workflowLedger := o.workflowBudgetLedger()
	if operation, ok := DispatchOperationFrom(ctx); ok &&
		operation.Kind == budget.OperationDSL && operation.WorkflowID != "" && workflowLedger != nil {
		if err := workflowLedger.CheckAndReserveWorkflowBudget(ctx, operation.WorkflowID, reserved); err != nil {
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			if releaseErr := ledger.Release(releaseCtx, identity, "workflow_budget_denied"); releaseErr != nil {
				o.metrics.RecordLedgerFailure("release")
				o.logger.Error("llm budget reservation could not be released after workflow budget denial",
					append(o.dispatchLogFields(prepared, identity),
						zap.String("transition", "release"),
						zap.String("error", SanitizeCompletionError(releaseErr)))...,
				)
			} else {
				o.logger.Info("llm budget transition",
					append(o.dispatchLogFields(prepared, identity),
						zap.String("transition", "release"),
						zap.String("state", budget.StateReleased),
						zap.String("errorClass", "workflow_budget_denied"))...,
				)
				o.observeBudgetSnapshot(releaseCtx, ledger)
			}
			cancel()
			return nil, markProviderDispatchNotStarted(err)
		}
	}

	// The dispatch marker must be durable before the network boundary so a crash
	// mid-call is reconciled conservatively.
	if err := ledger.MarkPossiblyDispatched(ctx, identity); err != nil {
		o.metrics.RecordLedgerFailure("mark_dispatched")
		// No provider call has begun. A detached release succeeds when the
		// marker definitely did not commit; if commit outcome was ambiguous the
		// ledger state check rejects release and keeps the reservation counted.
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		releaseErr := ledger.Release(releaseCtx, identity, "dispatch_marker_failed")
		if releaseErr != nil {
			o.metrics.RecordLedgerFailure("release")
			o.logger.Error("llm budget reservation could not be released after dispatch marker failure",
				append(o.dispatchLogFields(prepared, identity),
					zap.String("transition", "release"),
					zap.String("error", SanitizeCompletionError(releaseErr)))...,
			)
		} else {
			o.logger.Info("llm budget transition",
				append(o.dispatchLogFields(prepared, identity),
					zap.String("transition", "release"),
					zap.String("state", budget.StateReleased),
					zap.String("errorClass", "dispatch_marker_failed"))...,
			)
			o.observeBudgetSnapshot(releaseCtx, ledger)
		}
		cancel()
		return nil, markProviderDispatchNotStarted(err)
	}
	o.logger.Info("llm budget transition",
		append(o.dispatchLogFields(prepared, identity),
			zap.String("transition", "dispatch"),
			zap.String("state", budget.StatePossiblyDispatched))...,
	)

	traceCtx := withCompletionDispatchLineage(ctx, prepared, identity)
	// Retries stay inside one budget dispatch: the ledger settles the single
	// admitted dispatch with the winning attempt's actual usage, so retrying
	// transient availability errors (408/429/5xx/timeout) never double-bills
	// or duplicates dispatch lineage.
	resp, callErr := o.completeProviderWithRetry(
		traceCtx,
		prepared.Request,
		prepared.Route,
		provider,
		o.cfg.LLMCallMaxRetries,
	)
	outcome := "success"
	if callErr != nil {
		outcome = "failed"
		if IsAvailabilityError(callErr) {
			outcome = "unavailable"
		}
	}
	o.metrics.RecordPhysicalCall(prepared.Route, outcome)
	o.settleAdmitted(ctx, ledger, identity, prepared, budgetDay, reserved, resp, callErr)
	if callErr != nil {
		return nil, callErr
	}
	return resp, nil
}

// recordAdmissionFailure classifies a refused admission for metrics. Workspace
// IDs are never used as labels.
func (o *Orchestrator) recordAdmissionFailure(
	prepared PreparedCall,
	identity budget.DispatchIdentity,
	err error,
) {
	var denied *budget.DeniedError
	switch {
	case errors.As(err, &denied):
		o.metrics.RecordBudgetDenial(denied.Scope, denied.Limit)
		if identity.PhysicalOrdinal == budget.OrdinalFallback {
			o.metrics.RecordFallbackDenial()
		}
	case budget.IsUnavailable(err):
		o.metrics.RecordLedgerFailure("reserve")
	}
	o.logger.Warn("llm budget admission refused",
		append(o.dispatchLogFields(prepared, identity),
			zap.String("transition", "reserve"),
			zap.String("error", SanitizeCompletionError(err)))...,
	)
}

// settleAdmitted brings an admitted call to a terminal ledger state. Response
// usability and cost certainty are independent: a response with untrustworthy
// usage still charges its full reservation.
func (o *Orchestrator) settleAdmitted(
	ctx context.Context,
	ledger budget.Ledger,
	identity budget.DispatchIdentity,
	prepared PreparedCall,
	budgetDay string,
	reserved config.USDNanos,
	resp *CompletionResponse,
	callErr error,
) {
	// Settlement must still happen when the caller context is already done,
	// otherwise a cancelled call would leave its reservation nonterminal until
	// the next startup reconciliation.
	settleCtx := context.WithoutCancel(ctx)
	rates := preparedRates(prepared)
	caps := preparedCaps(prepared)

	assessment := usageFromResponse(resp)
	if assessment.ContractViolation {
		o.settleUncertain(
			settleCtx,
			ledger,
			identity,
			prepared,
			budgetDay,
			assessment.Reason,
		)
		o.closeRoute(prepared.Route, "usage_contract_violation")
		o.logger.Error("provider usage contract violation; charging the full reservation",
			append(o.dispatchLogFields(prepared, identity),
				zap.String("errorClass", assessment.Reason),
				zap.Int("unknownCategoryCount", len(assessment.UnknownCategories)),
				zap.String("routeState", "closed"))...,
		)
		o.addWorkflowSpend(settleCtx, reserved)
		return
	}
	if assessment.Trustworthy && callErr == nil {
		if err := budget.ValidateUsage(assessment.Usage, caps, assessment.UnknownCategories); err != nil {
			o.settleUncertain(settleCtx, ledger, identity, prepared, budgetDay, "usage_out_of_bounds")
			o.closeRoute(prepared.Route, "usage_contract_violation")
			o.logger.Error("provider reported usage outside the prepared bounds; charging the full reservation",
				append(o.dispatchLogFields(prepared, identity),
					zap.String("error", SanitizeCompletionError(err)),
					zap.String("routeState", "closed"))...,
			)
			o.addWorkflowSpend(settleCtx, reserved)
			return
		}
		cost, err := budget.SettledCost(rates, assessment.Usage)
		if err != nil {
			o.settleUncertain(settleCtx, ledger, identity, prepared, budgetDay, "unpriceable_usage")
			o.closeRoute(prepared.Route, "usage_contract_violation")
			o.addWorkflowSpend(settleCtx, reserved)
			return
		}
		if err := ledger.SettleTrusted(settleCtx, identity, assessment.Usage, cost, ""); err != nil {
			// A failed settlement leaves the reservation counted until startup
			// reconciliation reaches a terminal conservative state.
			o.metrics.RecordLedgerFailure("settle_trusted")
			o.logger.Error("llm budget settlement failed; the reservation stays counted",
				append(o.dispatchLogFields(prepared, identity),
					zap.String("error", SanitizeCompletionError(err)))...,
			)
		} else {
			o.logger.Info("llm budget transition",
				append(o.dispatchLogFields(prepared, identity),
					zap.String("transition", "settle"),
					zap.String("state", budget.StateSettled),
					zap.Int64("amountUsdNanos", int64(cost)))...,
			)
		}
		o.addWorkflowSpend(settleCtx, cost)
		o.observeBudgetSnapshot(settleCtx, ledger)
		return
	}

	reason := assessment.Reason
	if reason == "" {
		reason = "missing_usage"
	}
	if callErr != nil {
		reason = "failed_call"
		if IsAvailabilityError(callErr) {
			reason = "availability_failure"
		}
	}
	o.settleUncertain(settleCtx, ledger, identity, prepared, budgetDay, reason)
	o.addWorkflowSpend(settleCtx, reserved)
}

// addWorkflowSpend increments the parent DSL workflow's spent_usd_nanos by
// amount. It is best-effort: failures are logged and never fail the response
// path, mirroring the conservative ledger-settlement semantics. Calls are
// skipped for non-DSL operations and when no workflow ledger is bound.
func (o *Orchestrator) addWorkflowSpend(ctx context.Context, amount config.USDNanos) {
	if amount <= 0 {
		return
	}
	workflowLedger := o.workflowBudgetLedger()
	if workflowLedger == nil {
		return
	}
	operation, ok := DispatchOperationFrom(ctx)
	if !ok || operation.Kind != budget.OperationDSL || operation.WorkflowID == "" {
		return
	}
	if err := workflowLedger.AddWorkflowSpend(ctx, operation.WorkflowID, amount); err != nil {
		o.logger.Error("workflow budget spend update failed; the envelope counter may lag",
			zap.String("workflowId", operation.WorkflowID),
			zap.Int64("amountUsdNanos", int64(amount)),
			zap.String("error", SanitizeCompletionError(err)),
		)
	}
}

func (o *Orchestrator) settleUncertain(
	ctx context.Context,
	ledger budget.Ledger,
	identity budget.DispatchIdentity,
	prepared PreparedCall,
	budgetDay string,
	reason string,
) {
	o.metrics.RecordUncertainUsage(reason)
	if err := ledger.SettleUncertain(ctx, identity, reason); err != nil {
		o.metrics.RecordLedgerFailure("settle_uncertain")
		o.logger.Error("llm budget uncertain settlement failed; the reservation stays counted",
			append(o.dispatchLogFields(prepared, identity),
				zap.String("error", SanitizeCompletionError(err)))...,
		)
	} else {
		o.logger.Info("llm budget transition",
			append(o.dispatchLogFields(prepared, identity),
				zap.String("transition", "settle"),
				zap.String("state", budget.StateUsageUncertain),
				zap.String("errorClass", reason))...,
		)
	}
	o.observeBudgetSnapshot(ctx, ledger)
}

func (o *Orchestrator) dispatchLogFields(prepared PreparedCall, identity budget.DispatchIdentity) []zap.Field {
	workspaceHash := sha256.Sum256([]byte(identity.WorkspaceID))
	endpointHash := sha256.Sum256([]byte(prepared.Endpoint))
	return []zap.Field{
		zap.String("workspaceHash", fmt.Sprintf("%x", workspaceHash)),
		zap.String("operationKind", string(identity.OperationKind)),
		zap.String("operationId", identity.OperationID),
		zap.Int("logicalAttempt", identity.LogicalAttempt),
		zap.Int("physicalOrdinal", identity.PhysicalOrdinal),
		zap.String("route", prepared.Route),
		zap.String("provider", prepared.Provider),
		zap.String("model", prepared.Model),
		zap.String("endpointHash", fmt.Sprintf("%x", endpointHash)),
		zap.String("policyFingerprint", prepared.PolicyFingerprint),
		zap.String("priceRevision", prepared.PriceRevision),
		zap.String("requestHash", prepared.RequestHash),
	}
}

func (o *Orchestrator) observeBudgetSnapshot(ctx context.Context, ledger budget.Ledger) {
	if o.metrics == nil || ledger == nil {
		return
	}
	if err := o.tryPublishCurrentBudgetSnapshot(ctx, ledger); err != nil {
		o.metrics.RecordLedgerFailure("snapshot")
	}
}

// RefreshBudgetMetrics publishes the current global UTC-day gauges. Startup
// calls it after reconciliation; failures are returned so an enforced process
// cannot claim readiness with an unreadable ledger.
func (o *Orchestrator) RefreshBudgetMetrics(ctx context.Context) error {
	ledger := o.budgetLedger()
	if ledger == nil {
		return budget.ErrLedgerUnavailable
	}
	if err := o.publishCurrentBudgetSnapshot(ctx, ledger); err != nil {
		if o.metrics != nil {
			o.metrics.RecordLedgerFailure("snapshot")
		}
		return err
	}
	return nil
}

func (o *Orchestrator) publishCurrentBudgetSnapshot(ctx context.Context, ledger budget.Ledger) error {
	if ledger == nil {
		return budget.ErrLedgerUnavailable
	}
	select {
	case o.budgetMetricsGate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-o.budgetMetricsGate }()
	return o.publishCurrentBudgetSnapshotLocked(ctx, ledger)
}

// tryPublishCurrentBudgetSnapshot keeps transition telemetry best-effort. A
// transition must never queue behind ledger I/O performed by another refresh;
// the authenticated scrape path will obtain a fresh snapshot before exposing
// the gauges.
func (o *Orchestrator) tryPublishCurrentBudgetSnapshot(ctx context.Context, ledger budget.Ledger) error {
	select {
	case o.budgetMetricsGate <- struct{}{}:
		defer func() { <-o.budgetMetricsGate }()
	default:
		return nil
	}
	return o.publishCurrentBudgetSnapshotLocked(ctx, ledger)
}

func (o *Orchestrator) publishCurrentBudgetSnapshotLocked(ctx context.Context, ledger budget.Ledger) error {
	// Midnight can occur while SQLite is producing the snapshot. Retry once so
	// a historical reservation transition can never publish yesterday as the
	// current, unlabeled gauge series.
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		day := o.budgetDay()
		snapshot, err := ledger.Snapshot(ctx, "", day)
		if err != nil {
			return err
		}
		if day != o.budgetDay() {
			continue
		}
		if o.metrics != nil {
			o.metrics.ObserveBudgetSnapshot(
				int64(snapshot.Global.Settled),
				int64(snapshot.Global.ActiveReserved),
				int64(snapshot.Global.Remaining),
				int64(snapshot.Global.DailyBudgetUSD),
			)
		}
		if day == o.budgetDay() {
			return nil
		}
	}
	return fmt.Errorf("UTC budget day changed repeatedly while publishing metrics")
}

func (o *Orchestrator) complete(ctx context.Context, req CompletionRequest, allowRetryAndFallback bool) (*CompletionResult, error) {
	start := time.Now()
	key := cacheKey(req)
	provider := o.cfg.LLMProvider
	model := modelFromRequest(req, o.cfg)

	cached, hit, err := o.cache.Get(ctx, key)
	if err != nil {
		o.logger.Warn("llm cache get failed", zap.String("error", SanitizeCompletionError(err)))
	} else if hit {
		cachedProvider := cached.Provider
		if cachedProvider == "" {
			cachedProvider = provider
		}
		cachedModel := cached.Model
		if cachedModel == "" {
			cachedModel = model
		}
		result := &CompletionResult{
			CompletionResponse: &CompletionResponse{
				Content:      cached.Content,
				InputTokens:  cached.InputTokens,
				OutputTokens: cached.OutputTokens,
				ResponseID:   cached.ResponseID,
				FinishReason: cached.FinishReason,
			},
			CacheHit: true,
			Provider: cachedProvider,
			Model:    cachedModel,
		}
		if err := TraceCacheHit(ctx, req, result); err != nil {
			return nil, err
		}
		o.recordMetrics(cachedProvider, cachedModel, time.Since(start), cached.InputTokens, cached.OutputTokens, true, nil)
		return result, nil
	}

	usedProvider := provider
	maxRetries := 0
	if allowRetryAndFallback {
		maxRetries = o.cfg.LLMCallMaxRetries
	}
	resp, err := o.completeWithRetry(ctx, req, provider, maxRetries)
	if allowRetryAndFallback && err != nil && !errors.Is(err, ErrCompletionCapture) && o.cfg.LLMFallbackProvider != "" {
		o.logger.Warn("primary llm failed, trying fallback",
			zap.String("error", SanitizeCompletionError(err)),
			zap.String("fallback", o.cfg.LLMFallbackProvider))
		usedProvider = o.cfg.LLMFallbackProvider
		resp, err = o.completeWithRetry(ctx, req, o.cfg.LLMFallbackProvider, 1)
	}
	if err != nil {
		return nil, err
	}

	// H-6: only cache when the primary provider handled the request. A fallback
	// response cached under the primary's key carries wrong model attribution
	// and cannot reconstruct the fallback's distinct lineage. Mirrors the
	// enforced path's guard at line 403 (used.Route == primary.Route).
	if usedProvider != provider {
		if o.cfg.LLMCacheTTL > 0 {
			o.logger.Info("llm fallback response not cached to preserve dispatch lineage",
				zap.String("usedProvider", usedProvider),
				zap.String("primaryProvider", provider),
			)
		}
	} else if cachedContent, cacheSafe := cacheSafeCompletionContent(resp.Content); o.cfg.LLMCacheTTL > 0 && cacheSafe {
		if err := o.cache.Set(ctx, key, &cache.Entry{
			Content:      cachedContent,
			InputTokens:  resp.InputTokens,
			OutputTokens: resp.OutputTokens,
			ResponseID:   SanitizeCompletionMetadata(resp.ResponseID),
			FinishReason: SanitizeCompletionMetadata(resp.FinishReason),
			Provider:     SanitizeCompletionMetadata(usedProvider),
			Model:        SanitizeCompletionMetadata(model),
		}, o.cfg.LLMCacheTTL); err != nil {
			o.logger.Warn("llm cache set failed", zap.String("error", SanitizeCompletionError(err)))
		}
	} else if o.cfg.LLMCacheTTL > 0 && !cacheSafe {
		// Provider responses are cached only when a defensive redaction pass
		// proves persistence would preserve their exact semantics. Content
		// requiring redaction is retained only by the bounded workflow artifact
		// sink and is never written to the response cache.
		o.logger.Warn("llm response not cached because sensitive content was detected")
	}
	return &CompletionResult{CompletionResponse: resp, CacheHit: false, Provider: usedProvider, Model: model}, nil
}

func (o *Orchestrator) completeWithRetry(ctx context.Context, req CompletionRequest, providerName string, maxRetries int) (*CompletionResponse, error) {
	o.providersMu.RLock()
	p, ok := o.providers[providerName]
	o.providersMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", providerName)
	}
	return o.completeProviderWithRetry(ctx, req, providerName, p, maxRetries)
}

func (o *Orchestrator) completeProviderWithRetry(
	ctx context.Context,
	req CompletionRequest,
	providerName string,
	p Provider,
	maxRetries int,
) (*CompletionResponse, error) {
	traceProviderName := SanitizeCompletionMetadata(p.Name())
	if traceProviderName == "" {
		traceProviderName = SanitizeCompletionMetadata(providerName)
	}
	var lastErr error
	model := modelFromRequest(req, o.cfg)
	for attempt := 0; attempt <= maxRetries; attempt++ {
		start := time.Now()
		eventsBefore := 0
		if state := traceState(ctx); state != nil {
			eventsBefore = state.eventCount()
		}
		resp, err := p.Complete(ctx, req)
		if errors.Is(err, ErrCompletionCapture) {
			return nil, err
		}
		emptyResponse := err == nil && resp == nil
		if emptyResponse {
			err = errors.New("provider returned an empty response")
		}
		eventsAfter := eventsBefore
		state := traceState(ctx)
		if state != nil {
			eventsAfter = state.eventCount()
		}
		terminalCaptured := false
		if state != nil && eventsAfter > eventsBefore {
			if err == nil {
				terminalCaptured = state.terminalResponseCapturedSince(
					eventsBefore,
					CompletionBoundaryProviderResponse,
					traceProviderName,
					resp,
				)
			} else {
				terminalCaptured = state.terminalErrorCapturedSince(eventsBefore, err)
			}
		}
		if !terminalCaptured {
			if err == nil {
				if captureErr := TraceProviderResponse(ctx, req, traceProviderName, model, resp, 0); captureErr != nil {
					return nil, captureErr
				}
			} else if err != nil {
				errorCode := "provider_error"
				if emptyResponse {
					errorCode = "empty_response"
				}
				if captureErr := TraceProviderError(ctx, req, traceProviderName, model, 0, errorCode, "", err); captureErr != nil {
					return nil, captureErr
				}
			}
		}
		if err == nil {
			o.recordMetrics(traceProviderName, model, time.Since(start), resp.InputTokens, resp.OutputTokens, false, nil)
			return resp, nil
		}
		lastErr = err
		o.recordMetrics(traceProviderName, model, time.Since(start), 0, 0, false, err)
		o.logger.Warn("llm completion failed",
			zap.Int("attempt", attempt),
			zap.String("error", SanitizeCompletionError(err)))
		if attempt < maxRetries {
			time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
		}
	}
	return nil, WrapCompletionError(
		fmt.Sprintf("provider %s failed after %d retries", providerName, maxRetries),
		lastErr,
	)
}

func cacheSafeCompletionContent(content string) (string, bool) {
	sanitized := redact.String(content)
	return sanitized, sanitized == content
}

func (o *Orchestrator) recordMetrics(provider, model string, latency time.Duration, inputTokens, outputTokens int, cacheHit bool, err error) {
	if o.metrics != nil {
		o.metrics.RecordCompletion(provider, model, latency, inputTokens, outputTokens, cacheHit, err)
	}
}

func modelFromRequest(req CompletionRequest, cfg *config.Config) string {
	if req.Model != "" {
		return req.Model
	}
	return cfg.LLMModel
}

// memoryCache is a simple in-memory Cache implementation used by default for
// unit tests and as a fallback when no persistent cache is configured.
type memoryCache struct {
	mu      sync.RWMutex
	entries map[string]memoryCacheEntry
}

type memoryCacheEntry struct {
	entry   *cache.Entry
	expires time.Time
}

func newMemoryCache() *memoryCache {
	return &memoryCache{entries: map[string]memoryCacheEntry{}}
}

func (m *memoryCache) Get(ctx context.Context, key string) (*cache.Entry, bool, error) {
	m.mu.RLock()
	ent, ok := m.entries[key]
	m.mu.RUnlock()
	if !ok || time.Now().After(ent.expires) {
		return nil, false, nil
	}
	return &cache.Entry{
		Content:      ent.entry.Content,
		InputTokens:  ent.entry.InputTokens,
		OutputTokens: ent.entry.OutputTokens,
		ResponseID:   ent.entry.ResponseID,
		FinishReason: ent.entry.FinishReason,
		Provider:     ent.entry.Provider,
		Model:        ent.entry.Model,
	}, true, nil
}

func (m *memoryCache) Set(ctx context.Context, key string, entry *cache.Entry, ttl time.Duration) error {
	if ttl <= 0 || entry == nil {
		return nil
	}
	if sanitized := redact.String(entry.Content); sanitized != entry.Content {
		return cache.ErrUnsafeCacheContent
	}
	copied := *entry
	copied.Provider = SanitizeCompletionMetadata(copied.Provider)
	copied.Model = SanitizeCompletionMetadata(copied.Model)
	copied.ResponseID = SanitizeCompletionMetadata(copied.ResponseID)
	copied.FinishReason = SanitizeCompletionMetadata(copied.FinishReason)
	m.mu.Lock()
	m.entries[key] = memoryCacheEntry{entry: &copied, expires: time.Now().Add(ttl)}
	m.mu.Unlock()
	return nil
}
