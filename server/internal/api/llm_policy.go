package api

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"go.uber.org/zap"
)

// usdNanosString renders integer USD nanos as an exact decimal USD string. It
// is presentation only; every budget decision uses the integer value.
func usdNanosString(amount config.USDNanos) string {
	negative := amount < 0
	if negative {
		amount = -amount
	}
	whole := int64(amount) / 1_000_000_000
	fraction := int64(amount) % 1_000_000_000
	text := fmt.Sprintf("%d.%09d", whole, fraction)
	text = strings.TrimRight(text, "0")
	text = strings.TrimSuffix(text, ".")
	if text == "" {
		text = "0"
	}
	if negative {
		text = "-" + text
	}
	return text
}

// LLMRouteResponse is one sanitized route in the enforced policy. It never
// contains the credential for the route.
type LLMRouteResponse struct {
	Slot                     string `json:"slot"`
	Provider                 string `json:"provider"`
	Adapter                  string `json:"adapter"`
	Model                    string `json:"model"`
	Endpoint                 string `json:"endpoint"`
	PriceRevision            string `json:"priceRevision"`
	RequestTimeout           string `json:"requestTimeout"`
	Temperature              string `json:"temperature"`
	StrictToolOutput         bool   `json:"strictToolOutput"`
	EnableThinking           bool   `json:"enableThinking"`
	OutputCapDialect         string `json:"outputCapDialect"`
	MaxInputTokens           int    `json:"maxInputTokens"`
	MaxOutputTokens          int    `json:"maxOutputTokens"`
	InputUSDPerMillion       string `json:"inputUsdPerMillion"`
	CachedInputUSDPerMillion string `json:"cachedInputUsdPerMillion"`
	OutputUSDPerMillion      string `json:"outputUsdPerMillion"`
	WorstCaseRequestUSD      string `json:"worstCaseRequestUsd"`
	ProductionEligible       bool   `json:"productionEligible"`
}

// LLMAttemptLimitsResponse reports the four independent automatic-attempt caps.
type LLMAttemptLimitsResponse struct {
	RequirementMaxAttempts   int `json:"requirementMaxAttempts"`
	DSLGenerationMaxAttempts int `json:"dslGenerationMaxAttempts"`
	DSLMaxRepairs            int `json:"dslMaxRepairs"`
	SelectorMaxRepairs       int `json:"selectorMaxRepairs"`
}

// LLMPolicyResponse is the sanitized read-only view of the active LLM policy.
type LLMPolicyResponse struct {
	Mode               string                   `json:"mode"`
	Enabled            bool                     `json:"enabled"`
	ProductionEligible bool                     `json:"productionEligible"`
	Fingerprint        string                   `json:"fingerprint"`
	Primary            *LLMRouteResponse        `json:"primary,omitempty"`
	Fallback           *LLMRouteResponse        `json:"fallback,omitempty"`
	AttemptLimits      LLMAttemptLimitsResponse `json:"attemptLimits"`
	FallbackTaxonomy   []string                 `json:"fallbackTaxonomy"`
	BudgetLedgerBound  bool                     `json:"budgetLedgerBound"`
	Reconciled         bool                     `json:"reconciled"`
}

// LLMBudgetScopeResponse reports one scope's caps and consumption for a UTC day.
type LLMBudgetScopeResponse struct {
	MaxRequestUSD  string `json:"maxRequestUsd"`
	DailyBudgetUSD string `json:"dailyBudgetUsd"`
	SettledUSD     string `json:"settledUsd"`
	ActiveUSD      string `json:"activeReservedUsd"`
	RemainingUSD   string `json:"remainingUsd"`
	UncertainCalls int    `json:"uncertainCalls"`
}

// LLMBudgetResponse is the sanitized read-only budget state for a UTC day.
type LLMBudgetResponse struct {
	BudgetDay string                  `json:"budgetDay"`
	Global    LLMBudgetScopeResponse  `json:"global"`
	Workspace *LLMBudgetScopeResponse `json:"workspace,omitempty"`
}

// routeProductionEligible reports whether a route may serve production traffic.
// A loopback HTTP endpoint is accepted only for deterministic tests and makes
// the reported policy non-production-eligible.
func routeProductionEligible(baseURL string) bool {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "https")
}

func toLLMRouteResponse(route config.RoutePolicy) *LLMRouteResponse {
	worstCase, err := budget.Reservation(
		budget.Rates{
			InputUSDPerMillion:       route.InputUSDPerMillion,
			CachedInputUSDPerMillion: route.CachedInputUSDPerMillion,
			OutputUSDPerMillion:      route.OutputUSDPerMillion,
		},
		budget.Caps{MaxInputTokens: route.MaxInputTokens, MaxOutputTokens: route.MaxOutputTokens},
	)
	if err != nil {
		worstCase = 0
	}
	return &LLMRouteResponse{
		Slot:                     route.Slot,
		Provider:                 route.Provider,
		Adapter:                  route.Adapter,
		Model:                    route.Model,
		Endpoint:                 route.BaseURL,
		PriceRevision:            route.PriceRevision,
		RequestTimeout:           route.RequestTimeout.String(),
		Temperature:              strconv.FormatFloat(route.Temperature, 'g', -1, 64),
		StrictToolOutput:         route.StrictToolOutput,
		EnableThinking:           route.EnableThinking,
		OutputCapDialect:         string(route.OutputCapDialect),
		MaxInputTokens:           route.MaxInputTokens,
		MaxOutputTokens:          route.MaxOutputTokens,
		InputUSDPerMillion:       usdNanosString(route.InputUSDPerMillion),
		CachedInputUSDPerMillion: usdNanosString(route.CachedInputUSDPerMillion),
		OutputUSDPerMillion:      usdNanosString(route.OutputUSDPerMillion),
		WorstCaseRequestUSD:      usdNanosString(worstCase),
		ProductionEligible:       routeProductionEligible(route.BaseURL),
	}
}

// GetLLMPolicy godoc
// @Summary Read the active LLM policy (admin)
// @Description Returns the sanitized enforced-policy contract. Credentials, prompts, and provider response content are never included, and there is no API to mutate the policy.
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Success 200 {object} LLMPolicyResponse
// @Failure 401 {object} ErrorResponse "Unauthorized"
// @Router /admin/llm/policy [get]
func (h *Handler) GetLLMPolicy(w http.ResponseWriter, r *http.Request) {
	resp := LLMPolicyResponse{
		Mode:             string(config.LLMPolicyModeLegacy),
		Enabled:          h.cfg != nil && h.cfg.LLMEnabled,
		FallbackTaxonomy: llmFallbackTaxonomy(),
	}
	policy := h.enforcedPolicy()
	if policy == nil {
		// A legacy LLM-enabled process is never production-eligible.
		writeJSON(w, http.StatusOK, resp)
		return
	}

	resp.Mode = string(config.LLMPolicyModeEnforced)
	resp.Fingerprint = policy.Fingerprint
	resp.Primary = toLLMRouteResponse(policy.Primary)
	if policy.Fallback != nil {
		resp.Fallback = toLLMRouteResponse(*policy.Fallback)
	}
	resp.AttemptLimits = LLMAttemptLimitsResponse{
		RequirementMaxAttempts:   policy.RequirementMaxAttempts,
		DSLGenerationMaxAttempts: policy.DSLGenerationMaxAttempts,
		DSLMaxRepairs:            policy.DSLMaxRepairs,
		SelectorMaxRepairs:       policy.SelectorMaxRepairs,
	}
	resp.BudgetLedgerBound = h.budgetLedger != nil
	resp.Reconciled = h.budgetReconciled
	budgetAdmissible := budget.ValidatePolicyAdmissibility(policy) == nil
	primaryRuntimeReady := false
	if h.llmRuntime != nil {
		primaryRuntimeReady, _ = h.llmRuntime.RouteReady("primary")
	}
	resp.ProductionEligible = resp.Primary.ProductionEligible &&
		(resp.Fallback == nil || resp.Fallback.ProductionEligible) &&
		budgetAdmissible && resp.BudgetLedgerBound && resp.Reconciled && primaryRuntimeReady
	writeJSON(w, http.StatusOK, resp)
}

// llmFallbackTaxonomy is the exact set of conditions that may route to the one
// permitted fallback. It is reported so operators can audit the contract.
func llmFallbackTaxonomy() []string {
	taxonomy := []string{
		"transport_or_network_failure",
		"route_request_timeout",
		"http_408",
		"http_429",
		"http_5xx",
	}
	sort.Strings(taxonomy)
	return taxonomy
}

// GetLLMBudget godoc
// @Summary Read LLM hard-budget state for a UTC day (admin)
// @Description Returns global and authenticated-workspace request/daily limits, settled amount, active reservations, remaining amount, and uncertain-call count. There is no API to refund, release, delete, or override a denial.
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param day query string false "UTC calendar day (YYYY-MM-DD). Defaults to today."
// @Success 200 {object} LLMBudgetResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse "Unauthorized"
// @Failure 503 {object} ErrorResponse "Budget ledger unavailable"
// @Router /admin/llm/budget [get]
func (h *Handler) GetLLMBudget(w http.ResponseWriter, r *http.Request) {
	if h.budgetLedger == nil {
		writeError(w, http.StatusServiceUnavailable, budget.CodeLedgerUnavailable,
			"the LLM hard-budget ledger is not active")
		return
	}
	day := strings.TrimSpace(r.URL.Query().Get("day"))
	if day == "" {
		day = budget.BudgetDay(time.Now().UTC())
	}
	if err := budget.ValidateBudgetDay(day); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "day must be a UTC calendar day formatted YYYY-MM-DD")
		return
	}

	// The workspace comes from the authenticated principal, never from a query
	// parameter, so one workspace cannot read another's consumption.
	workspaceID := authz.WorkspaceID(r.Context())
	snapshot, err := h.budgetLedger.Snapshot(r.Context(), workspaceID, day)
	if err != nil {
		h.logger.Error("read llm budget snapshot failed", zap.Error(err))
		if writeLLMDispatchError(w, err) {
			return
		}
		writeError(w, http.StatusServiceUnavailable, budget.CodeLedgerUnavailable,
			"the LLM hard-budget ledger could not be read")
		return
	}

	resp := LLMBudgetResponse{
		BudgetDay: snapshot.BudgetDay,
		Global:    toLLMBudgetScopeResponse(snapshot.Global),
	}
	if snapshot.Workspace != nil {
		scope := toLLMBudgetScopeResponse(*snapshot.Workspace)
		resp.Workspace = &scope
	}
	writeJSON(w, http.StatusOK, resp)
}

func toLLMBudgetScopeResponse(usage budget.ScopeUsage) LLMBudgetScopeResponse {
	return LLMBudgetScopeResponse{
		MaxRequestUSD:  usdNanosString(usage.MaxRequestUSD),
		DailyBudgetUSD: usdNanosString(usage.DailyBudgetUSD),
		SettledUSD:     usdNanosString(usage.Settled),
		ActiveUSD:      usdNanosString(usage.ActiveReserved),
		RemainingUSD:   usdNanosString(usage.Remaining),
		UncertainCalls: usage.UncertainCalls,
	}
}

func (h *Handler) enforcedPolicy() *config.ProductionLLMPolicy {
	if h.cfg == nil {
		return nil
	}
	return h.cfg.EnforcedLLMPolicy()
}
