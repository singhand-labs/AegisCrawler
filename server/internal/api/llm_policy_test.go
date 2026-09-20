package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

type staticLLMRuntime struct {
	ready  bool
	reason string
}

func (r staticLLMRuntime) RouteReady(string) (bool, string) {
	return r.ready, r.reason
}

func setEnforcedPolicyEnvironment(t *testing.T, endpoint string) {
	t.Helper()
	for key, value := range map[string]string{
		"LLM_ENABLED":                        "true",
		"LLM_POLICY_MODE":                    "enforced",
		"LLM_PRIMARY_PROVIDER":               "aliyun",
		"LLM_PRIMARY_ADAPTER":                "openai",
		"LLM_PRIMARY_MODEL":                  "qwen-test",
		"LLM_PRIMARY_BASE_URL":               endpoint,
		"LLM_PRIMARY_API_KEY":                "synthetic-primary-key",
		"LLM_PRIMARY_REQUEST_TIMEOUT":        "30s",
		"LLM_PRIMARY_TEMPERATURE":            "0",
		"LLM_PRIMARY_STRICT_TOOL_OUTPUT":     "false",
		"LLM_PRIMARY_ENABLE_THINKING":        "false",
		"LLM_PRIMARY_INPUT_USD_PER_MILLION":  "0.6",
		"LLM_PRIMARY_OUTPUT_USD_PER_MILLION": "2",
		"LLM_PRIMARY_MAX_INPUT_TOKENS":       "100000",
		"LLM_PRIMARY_MAX_OUTPUT_TOKENS":      "4000",
		"LLM_PRIMARY_PRICE_REVISION":         "2026-07-01-contract-v3",
		"LLM_GLOBAL_MAX_REQUEST_USD":         "1",
		"LLM_GLOBAL_DAILY_BUDGET_USD":        "10",
		"LLM_WORKSPACE_BUDGETS_JSON":         `{"default":{"maxRequestUSD":"0.60","dailyBudgetUSD":"3.00"},"tenant-b":{"maxRequestUSD":"0.60","dailyBudgetUSD":"3.00"}}`,
		"LLM_REQUIREMENT_MAX_ATTEMPTS":       "2",
		"LLM_DSL_GENERATION_MAX_ATTEMPTS":    "2",
		"LLM_DSL_MAX_REPAIRS":                "1",
		"LLM_SELECTOR_MAX_REPAIRS":           "1",
	} {
		t.Setenv(key, value)
	}
}

func newLLMPolicyTestHandler(t *testing.T, endpoint string) (*Handler, *store.BudgetLedger) {
	t.Helper()
	setEnforcedPolicyEnvironment(t, endpoint)
	cfg := config.Load()
	if err := cfg.ValidateLLMPolicy(); err != nil {
		t.Fatalf("enforced policy did not load: %v", err)
	}

	f, err := os.CreateTemp("", "api-llm-budget-*.db")
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

	ledger := store.NewBudgetLedger(s, budget.LimitsFromPolicy(cfg.EnforcedLLMPolicy()))
	h := NewHandler(s, nil, nil, nil, cfg, zap.NewNop())
	h.SetMetrics(NewMetrics())
	h.SetLLMBudget(ledger, true)
	h.SetLLMRuntime(staticLLMRuntime{ready: true})
	return h, ledger
}

func TestGetLLMPolicyReportsSanitizedEnforcedContract(t *testing.T) {
	h, _ := newLLMPolicyTestHandler(t, "https://provider.example.test/v1")

	rec := httptest.NewRecorder()
	h.GetLLMPolicy(rec, httptest.NewRequest(http.MethodGet, "/admin/llm/policy", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp LLMPolicyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Mode != "enforced" || !resp.Enabled {
		t.Fatalf("mode = %q enabled = %v", resp.Mode, resp.Enabled)
	}
	if resp.Fingerprint == "" {
		t.Fatal("expected a policy fingerprint")
	}
	if resp.Primary == nil || resp.Primary.Provider != "aliyun" || resp.Primary.Model != "qwen-test" {
		t.Fatalf("primary = %+v", resp.Primary)
	}
	if resp.Primary.PriceRevision != "2026-07-01-contract-v3" {
		t.Fatalf("price revision = %q", resp.Primary.PriceRevision)
	}
	if resp.Primary.OutputCapDialect != "max_tokens" {
		t.Fatalf("output-cap dialect = %q, want max_tokens", resp.Primary.OutputCapDialect)
	}
	// 100000 input at 0.6/M + 4000 output at 2/M, plus 1 nano of
	// cached/uncached split-rounding headroom.
	if resp.Primary.WorstCaseRequestUSD != "0.068000001" {
		t.Fatalf("worst case = %q, want 0.068000001", resp.Primary.WorstCaseRequestUSD)
	}
	if resp.AttemptLimits.RequirementMaxAttempts != 2 || resp.AttemptLimits.SelectorMaxRepairs != 1 {
		t.Fatalf("attempt limits = %+v", resp.AttemptLimits)
	}
	if !resp.BudgetLedgerBound || !resp.Reconciled || !resp.ProductionEligible {
		t.Fatalf("expected a production-eligible bound policy, got %+v", resp)
	}
	if resp.Fallback != nil {
		t.Fatal("no fallback route was configured")
	}

	// The credential must never appear anywhere in the response.
	if strings.Contains(rec.Body.String(), "synthetic-primary-key") {
		t.Fatal("the policy response leaked a credential")
	}
}

func TestLLMAdminRoutesEnforceConfiguredAuthentication(t *testing.T) {
	h, _ := newLLMPolicyTestHandler(t, "https://provider.example.test/v1")
	const adminKey = "llm-status-admin-key"
	h.cfg.AdminAPIKey = adminKey
	router := NewRouter(h, h.cfg, zap.NewNop(), NewMetrics())

	for _, path := range []string{"/admin/llm/policy", "/admin/llm/budget"} {
		t.Run(strings.TrimPrefix(path, "/admin/llm/")+"/missing_token", func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body %s", rec.Code, rec.Body.String())
			}
		})

		t.Run(strings.TrimPrefix(path, "/admin/llm/")+"/wrong_token", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer wrong-key")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body %s", rec.Code, rec.Body.String())
			}
		})

		t.Run(strings.TrimPrefix(path, "/admin/llm/")+"/valid_token", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer "+adminKey)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestGetLLMPolicyMarksLoopbackNonProductionEligible(t *testing.T) {
	h, _ := newLLMPolicyTestHandler(t, "http://127.0.0.1:9099/v1")

	rec := httptest.NewRecorder()
	h.GetLLMPolicy(rec, httptest.NewRequest(http.MethodGet, "/admin/llm/policy", nil))

	var resp LLMPolicyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Primary.ProductionEligible {
		t.Fatal("a loopback endpoint must not be production eligible")
	}
	if resp.ProductionEligible {
		t.Fatal("a policy with a loopback route must not be production eligible")
	}
}

func TestGetLLMPolicyPreservesExactConfiguredTemperature(t *testing.T) {
	setEnforcedPolicyEnvironment(t, "https://provider.example.test/v1")
	t.Setenv("LLM_PRIMARY_TEMPERATURE", "0.123456789012345")
	cfg := config.Load()
	if err := cfg.ValidateLLMPolicy(); err != nil {
		t.Fatal(err)
	}
	resp := toLLMRouteResponse(cfg.EnforcedLLMPolicy().Primary)
	if resp.Temperature != "0.123456789012345" {
		t.Fatalf("temperature = %q, want exact configured value", resp.Temperature)
	}
}

func TestLLMRouteResponsePreservesCompletionCapDialect(t *testing.T) {
	resp := toLLMRouteResponse(config.RoutePolicy{
		OutputCapDialect: config.OutputCapDialectMaxCompletionTokens,
		MaxInputTokens:   1,
		MaxOutputTokens:  config.AliyunMaxCompletionTokensTolerance + 1,
	})
	if resp.OutputCapDialect != "max_completion_tokens" {
		t.Fatalf("output-cap dialect = %q, want max_completion_tokens", resp.OutputCapDialect)
	}
}

func TestClosedPrimaryRouteDisablesPolicyEligibilityAndReadiness(t *testing.T) {
	h, _ := newLLMPolicyTestHandler(t, "https://provider.example.test/v1")
	h.SetLLMRuntime(staticLLMRuntime{reason: "usage_contract_violation"})

	rec := httptest.NewRecorder()
	h.GetLLMPolicy(rec, httptest.NewRequest(http.MethodGet, "/admin/llm/policy", nil))
	var resp LLMPolicyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ProductionEligible {
		t.Fatal("a closed primary route must not be production eligible")
	}
	if check, ready := h.enforcedLLMReadiness(context.Background()); ready || check != "primary_route_closed" {
		t.Fatalf("readiness = %v, %q; want primary_route_closed", ready, check)
	}
}

func TestAlwaysDeniedBudgetPolicyDisablesEligibilityAndReadiness(t *testing.T) {
	h, _ := newLLMPolicyTestHandler(t, "https://provider.example.test/v1")
	h.cfg.EnforcedLLMPolicy().GlobalMaxRequestUSD = 1

	rec := httptest.NewRecorder()
	h.GetLLMPolicy(rec, httptest.NewRequest(http.MethodGet, "/admin/llm/policy", nil))
	var response LLMPolicyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ProductionEligible {
		t.Fatal("a policy that cannot admit one route reservation must not be production eligible")
	}
	if check, ready := h.enforcedLLMReadiness(context.Background()); ready ||
		check != "budget_policy_inadmissible" {
		t.Fatalf("readiness = %v, %q; want budget_policy_inadmissible", ready, check)
	}
}

func TestGetLLMPolicyReportsLegacyModeAsNonProductionEligible(t *testing.T) {
	cfg := &config.Config{LLMEnabled: true}
	h := NewHandler(nil, nil, nil, nil, cfg, zap.NewNop())

	rec := httptest.NewRecorder()
	h.GetLLMPolicy(rec, httptest.NewRequest(http.MethodGet, "/admin/llm/policy", nil))

	var resp LLMPolicyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Mode != "legacy" {
		t.Fatalf("mode = %q, want legacy", resp.Mode)
	}
	if resp.ProductionEligible {
		t.Fatal("a legacy LLM-enabled process must never be production eligible")
	}
	if len(resp.FallbackTaxonomy) == 0 {
		t.Fatal("expected the fallback taxonomy to be reported")
	}
}

func TestGetLLMBudgetReportsScopedConsumption(t *testing.T) {
	ctx := context.Background()
	h, ledger := newLLMPolicyTestHandler(t, "https://provider.example.test/v1")

	rates := budget.Rates{InputUSDPerMillion: 600_000_000, OutputUSDPerMillion: 2_000_000_000}
	caps := budget.Caps{MaxInputTokens: 100_000, MaxOutputTokens: 4_000}
	reserved, err := budget.Reservation(rates, caps)
	if err != nil {
		t.Fatal(err)
	}
	identity := budget.DispatchIdentity{
		WorkspaceID: "default", OperationKind: budget.OperationEnhance,
		OperationID: "job-1", LogicalAttempt: 1, PhysicalOrdinal: budget.OrdinalPrimary,
	}
	day := budget.BudgetDay(time.Now().UTC())
	if _, err := ledger.Reserve(ctx, budget.ReserveRequest{
		Identity: identity, BudgetDay: day, RouteSlot: "primary",
		Provider: "aliyun", Model: "qwen-test", Endpoint: "https://provider.example.test/v1",
		PolicyFingerprint: "fingerprint", PriceRevision: "2026-07-01-contract-v3",
		RequestHash: "hash", Rates: rates, Caps: caps, Reserved: reserved,
	}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/llm/budget", nil)
	req = req.WithContext(authz.WithPrincipal(ctx, authz.Principal{WorkspaceID: "default", Subject: "admin"}))
	rec := httptest.NewRecorder()
	h.GetLLMBudget(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}

	var resp LLMBudgetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.BudgetDay != day {
		t.Fatalf("budget day = %q, want %q", resp.BudgetDay, day)
	}
	if resp.Global.ActiveUSD != "0.068000001" {
		t.Fatalf("global active = %q, want 0.068000001", resp.Global.ActiveUSD)
	}
	if resp.Global.DailyBudgetUSD != "10" {
		t.Fatalf("global daily = %q, want 10", resp.Global.DailyBudgetUSD)
	}
	if resp.Global.RemainingUSD != "9.931999999" {
		t.Fatalf("global remaining = %q, want 9.931999999", resp.Global.RemainingUSD)
	}
	if resp.Workspace == nil {
		t.Fatal("expected workspace scope for an authenticated principal")
	}
	if resp.Workspace.DailyBudgetUSD != "3" || resp.Workspace.MaxRequestUSD != "0.6" {
		t.Fatalf("workspace caps = %+v", resp.Workspace)
	}
	if resp.Workspace.RemainingUSD != "2.931999999" {
		t.Fatalf("workspace remaining = %q, want 2.931999999", resp.Workspace.RemainingUSD)
	}
}

func TestGetLLMBudgetRejectsMalformedDay(t *testing.T) {
	h, _ := newLLMPolicyTestHandler(t, "https://provider.example.test/v1")

	req := httptest.NewRequest(http.MethodGet, "/admin/llm/budget?day=2026-7-30", nil)
	rec := httptest.NewRecorder()
	h.GetLLMBudget(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestGetLLMBudgetReportsUnavailableWithoutLedger(t *testing.T) {
	cfg := &config.Config{}
	h := NewHandler(nil, nil, nil, nil, cfg, zap.NewNop())

	rec := httptest.NewRecorder()
	h.GetLLMBudget(rec, httptest.NewRequest(http.MethodGet, "/admin/llm/budget", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), budget.CodeLedgerUnavailable) {
		t.Fatalf("expected %s, got %s", budget.CodeLedgerUnavailable, rec.Body.String())
	}
}

func TestGetLLMBudgetIgnoresClientSuppliedWorkspace(t *testing.T) {
	ctx := context.Background()
	h, _ := newLLMPolicyTestHandler(t, "https://provider.example.test/v1")

	// A client-supplied workspace parameter must not select the scope.
	req := httptest.NewRequest(http.MethodGet, "/admin/llm/budget?workspaceId=someone-else", nil)
	req = req.WithContext(authz.WithPrincipal(ctx, authz.Principal{WorkspaceID: "default"}))
	rec := httptest.NewRecorder()
	h.GetLLMBudget(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp LLMBudgetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// The authenticated workspace has configured caps; an arbitrary one would not.
	if resp.Workspace == nil || resp.Workspace.DailyBudgetUSD != "3" {
		t.Fatalf("workspace scope = %+v, want the authenticated workspace caps", resp.Workspace)
	}
}

func TestGetLLMBudgetIsolatesTwoAuthenticatedWorkspaces(t *testing.T) {
	ctx := context.Background()
	h, ledger := newLLMPolicyTestHandler(t, "https://provider.example.test/v1")
	if err := h.store.CreateWorkspace(ctx, &models.Workspace{ID: "tenant-b", Name: "Tenant B"}); err != nil {
		t.Fatal(err)
	}
	policy := h.cfg.EnforcedLLMPolicy()
	rates := budget.Rates{
		InputUSDPerMillion:       policy.Primary.InputUSDPerMillion,
		CachedInputUSDPerMillion: policy.Primary.CachedInputUSDPerMillion,
		OutputUSDPerMillion:      policy.Primary.OutputUSDPerMillion,
	}
	caps := budget.Caps{
		MaxInputTokens: policy.Primary.MaxInputTokens, MaxOutputTokens: policy.Primary.MaxOutputTokens,
	}
	reserved, err := budget.Reservation(rates, caps)
	if err != nil {
		t.Fatal(err)
	}
	day := budget.BudgetDay(time.Now().UTC())
	for i, workspace := range []string{"default", "tenant-b", "tenant-b"} {
		if _, err := ledger.Reserve(ctx, budget.ReserveRequest{
			Identity: budget.DispatchIdentity{
				WorkspaceID: workspace, OperationKind: budget.OperationIntent,
				OperationID: "operation-" + workspace + "-" + strconv.Itoa(i), LogicalAttempt: 1, PhysicalOrdinal: 0,
			},
			BudgetDay: day, RouteSlot: "primary", Provider: policy.Primary.Provider,
			Model: policy.Primary.Model, Endpoint: policy.Primary.BaseURL,
			PolicyFingerprint: policy.Fingerprint, PriceRevision: policy.Primary.PriceRevision,
			RequestHash: "hash-" + workspace, Rates: rates, Caps: caps, Reserved: reserved,
		}); err != nil {
			t.Fatalf("reserve %s: %v", workspace, err)
		}
	}

	const defaultAdminKey = "key-for-default"
	h.cfg.AdminAPIKey = defaultAdminKey
	defaultRouter := NewRouter(h, h.cfg, zap.NewNop(), NewMetrics())

	read := func(workspace, clientSelectedWorkspace string) LLMBudgetResponse {
		key := "key-for-" + workspace
		var authenticatedRoute http.Handler
		if workspace == authz.DefaultWorkspaceID {
			// Exercise the production Admin route and its configured-key
			// middleware for the default authenticated workspace.
			authenticatedRoute = defaultRouter
			key = defaultAdminKey
		} else {
			// The current deployment auth contract has one global Admin key,
			// which binds the default workspace. Reuse the same production
			// principal middleware with a server-owned tenant principal to
			// prove the handler's multi-workspace isolation contract.
			principal := authz.Principal{
				WorkspaceID: workspace,
				Subject:     workspace,
				Roles:       []authz.Role{authz.RoleAdmin},
				Kind:        authz.PrincipalAdmin,
			}
			mux := http.NewServeMux()
			mux.Handle("GET /admin/llm/budget",
				principalAuthMiddleware(key, principal)(http.HandlerFunc(h.GetLLMBudget)))
			authenticatedRoute = mux
		}

		req := httptest.NewRequest(http.MethodGet,
			"/admin/llm/budget?workspaceId="+clientSelectedWorkspace, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		authenticatedRoute.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("read %s: %d %s", workspace, rec.Code, rec.Body.String())
		}
		var response LLMBudgetResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	defaultBudget := read("default", "tenant-b")
	tenantBudget := read("tenant-b", "default")
	if defaultBudget.Workspace == nil || tenantBudget.Workspace == nil {
		t.Fatal("both authenticated workspaces must receive a scoped view")
	}
	if defaultBudget.Workspace.ActiveUSD != "0.068000001" || tenantBudget.Workspace.ActiveUSD != "0.136000002" {
		t.Fatalf("workspace active values = %q/%q, want independently scoped 0.068000001/0.136000002",
			defaultBudget.Workspace.ActiveUSD, tenantBudget.Workspace.ActiveUSD)
	}
	if defaultBudget.Global.ActiveUSD != "0.204000003" || tenantBudget.Global.ActiveUSD != "0.204000003" {
		t.Fatalf("global active values = %q/%q, want shared global 0.204000003",
			defaultBudget.Global.ActiveUSD, tenantBudget.Global.ActiveUSD)
	}
}

func TestEnforcedReadinessRequiresBoundReconciledLedger(t *testing.T) {
	ctx := context.Background()
	h, ledger := newLLMPolicyTestHandler(t, "https://provider.example.test/v1")

	if check, ready := h.enforcedLLMReadiness(ctx); !ready {
		t.Fatalf("expected readiness, got %q", check)
	}

	h.SetLLMBudget(ledger, false)
	if check, ready := h.enforcedLLMReadiness(ctx); ready || check != "reconciliation_incomplete" {
		t.Fatalf("check = %q ready = %v, want reconciliation_incomplete", check, ready)
	}

	h.SetLLMBudget(nil, true)
	if check, ready := h.enforcedLLMReadiness(ctx); ready || check != "budget_ledger_unbound" {
		t.Fatalf("check = %q ready = %v, want budget_ledger_unbound", check, ready)
	}

	h.SetLLMBudget(ledger, true)
	h.SetLLMRuntime(nil)
	if check, ready := h.enforcedLLMReadiness(ctx); ready || check != "primary_route_not_constructed" {
		t.Fatalf("check = %q ready = %v, want primary_route_not_constructed", check, ready)
	}
}

func TestReadinessIgnoresLLMWhenDisabled(t *testing.T) {
	h := NewHandler(nil, nil, nil, nil, &config.Config{}, zap.NewNop())
	if check, ready := h.enforcedLLMReadiness(context.Background()); !ready {
		t.Fatalf("a disabled LLM must not block readiness, got %q", check)
	}
}

func TestReadinessAllowsLegacyLLMMode(t *testing.T) {
	// Legacy mode stays locally ready for migration and development even though
	// it is not production eligible.
	h := NewHandler(nil, nil, nil, nil, &config.Config{LLMEnabled: true}, zap.NewNop())
	if check, ready := h.enforcedLLMReadiness(context.Background()); !ready {
		t.Fatalf("legacy mode must stay locally ready, got %q", check)
	}
}

func TestUSDNanosString(t *testing.T) {
	cases := []struct {
		in   config.USDNanos
		want string
	}{
		{0, "0"},
		{1, "0.000000001"},
		{1_000_000_000, "1"},
		{68_000_000, "0.068"},
		{600_000_000, "0.6"},
		{3_000_000_000, "3"},
		{9_932_000_000, "9.932"},
		{1_500_000_000, "1.5"},
	}
	for _, tc := range cases {
		if got := usdNanosString(tc.in); got != tc.want {
			t.Fatalf("usdNanosString(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
