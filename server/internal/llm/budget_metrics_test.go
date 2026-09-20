package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"go.uber.org/zap"
)

type rolloverSnapshotLedger struct {
	*fakeLedger
	requestedDays []string
	afterFirst    func()
}

type blockingSnapshotLedger struct {
	*fakeLedger
	entered chan struct{}
	release chan struct{}
}

func (l *blockingSnapshotLedger) Snapshot(
	ctx context.Context,
	workspaceID string,
	budgetDay string,
) (budget.Snapshot, error) {
	select {
	case l.entered <- struct{}{}:
	default:
	}
	select {
	case <-l.release:
		return l.fakeLedger.Snapshot(ctx, workspaceID, budgetDay)
	case <-ctx.Done():
		return budget.Snapshot{}, ctx.Err()
	}
}

func (l *rolloverSnapshotLedger) Snapshot(
	ctx context.Context,
	workspaceID string,
	budgetDay string,
) (budget.Snapshot, error) {
	snapshot, err := l.fakeLedger.Snapshot(ctx, workspaceID, budgetDay)
	l.requestedDays = append(l.requestedDays, budgetDay)
	if len(l.requestedDays) == 1 && l.afterFirst != nil {
		l.afterFirst()
	}
	return snapshot, err
}

func TestBudgetMetricsRecordDenialWithoutWorkspaceLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetricsWithRegistry(reg)

	m.RecordBudgetDenial(budget.ScopeWorkspace, budget.LimitDaily)
	m.RecordBudgetDenial(budget.ScopeGlobal, budget.LimitRequest)

	if got := testutil.ToFloat64(m.BudgetRejections.WithLabelValues(budget.ScopeWorkspace, budget.LimitDaily)); got != 1 {
		t.Fatalf("workspace daily denials = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.BudgetRejections.WithLabelValues(budget.ScopeGlobal, budget.LimitRequest)); got != 1 {
		t.Fatalf("global request denials = %v, want 1", got)
	}

	// Cardinality must stay bounded: the only labels are scope and limit.
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "opencrawler_llm_budget_rejections_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() != "scope" && label.GetName() != "limit" {
					t.Fatalf("unexpected label %q on budget rejections", label.GetName())
				}
			}
		}
	}
}

func TestBudgetMetricsObserveSnapshot(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetricsWithRegistry(reg)

	m.ObserveBudgetSnapshot(2_000_000_000, 1_000_000_000, 7_000_000_000, 10_000_000_000)

	if got := testutil.ToFloat64(m.BudgetSettled); got != 2_000_000_000 {
		t.Fatalf("settled = %v", got)
	}
	if got := testutil.ToFloat64(m.BudgetActive); got != 1_000_000_000 {
		t.Fatalf("active = %v", got)
	}
	if got := testutil.ToFloat64(m.BudgetRemaining); got != 7_000_000_000 {
		t.Fatalf("remaining = %v", got)
	}
	if got := testutil.ToFloat64(m.BudgetUtilization); got != 0.3 {
		t.Fatalf("utilization = %v, want 0.3", got)
	}
}

func TestBudgetMetricsUtilizationHandlesZeroCap(t *testing.T) {
	m := NewMetricsWithRegistry(prometheus.NewRegistry())
	m.ObserveBudgetSnapshot(1, 1, 0, 0)
	if got := testutil.ToFloat64(m.BudgetUtilization); got != 0 {
		t.Fatalf("utilization = %v, want 0 for an unconfigured cap", got)
	}
}

func TestNilBudgetMetricsAreSafe(t *testing.T) {
	var m *Metrics
	m.RecordBudgetDenial("global", "daily")
	m.RecordLedgerFailure("reserve")
	m.RecordUncertainUsage("timeout")
	m.RecordPhysicalCall("primary", "success")
	m.RecordFallbackDenial()
	m.ObserveBudgetSnapshot(1, 2, 3, 4)
}

func TestOrchestratorRecordsBudgetDenialMetrics(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	m := NewMetricsWithRegistry(prometheus.NewRegistry())
	o.SetMetrics(m)
	ledger := newFakeLedger()
	ledger.denyOrdinals[budget.OrdinalPrimary] = true
	o.SetBudgetLedger(ledger)
	o.RegisterProviderAs("primary", &fakeProvider{name: "aliyun"})

	if _, err := o.Complete(enforcedDispatchContext("op-1"), CompletionRequest{User: "test"}); !budget.IsDenied(err) {
		t.Fatalf("expected a denial, got %v", err)
	}
	if got := testutil.ToFloat64(m.BudgetRejections.WithLabelValues(budget.ScopeWorkspace, budget.LimitDaily)); got != 1 {
		t.Fatalf("denial counter = %v, want 1", got)
	}
	for _, outcome := range []string{"success", "failed", "unavailable", "other"} {
		if got := testutil.ToFloat64(m.PhysicalCalls.WithLabelValues("primary", outcome)); got != 0 {
			t.Fatalf("a denied admission must not count as a physical call; %s = %v", outcome, got)
		}
	}
}

func TestOrchestratorRecordsFallbackDenialMetric(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	m := NewMetricsWithRegistry(prometheus.NewRegistry())
	o.SetMetrics(m)
	ledger := newFakeLedger()
	ledger.denyOrdinals[budget.OrdinalFallback] = true
	o.SetBudgetLedger(ledger)
	o.RegisterProviderAs("primary", &fakeProvider{name: "aliyun", err: availabilityHTTPError{status: 503}})
	o.RegisterProviderAs("fallback", &fakeProvider{name: "anthropic-secondary"})

	if _, err := o.Complete(enforcedDispatchContext("op-1"), CompletionRequest{User: "test"}); !budget.IsDenied(err) {
		t.Fatalf("expected the terminal fallback denial, got %v", err)
	}
	if got := testutil.ToFloat64(m.FallbackDenials); got != 1 {
		t.Fatalf("fallback denial counter = %v, want 1", got)
	}
	// The primary was admitted, dispatched, and charged conservatively.
	if got := testutil.ToFloat64(m.PhysicalCalls.WithLabelValues("primary", "unavailable")); got != 1 {
		t.Fatalf("primary unavailable counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.UncertainUsage.WithLabelValues("availability_failure")); got != 1 {
		t.Fatalf("uncertain counter = %v, want 1", got)
	}
}

func TestOrchestratorRecordsLedgerUnavailableMetric(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	m := NewMetricsWithRegistry(prometheus.NewRegistry())
	o.SetMetrics(m)
	ledger := newFakeLedger()
	ledger.unavailable = true
	o.SetBudgetLedger(ledger)
	provider := &fakeProvider{name: "aliyun"}
	o.RegisterProviderAs("primary", provider)

	if _, err := o.Complete(enforcedDispatchContext("op-1"), CompletionRequest{User: "test"}); !budget.IsUnavailable(err) {
		t.Fatalf("expected ledger unavailability, got %v", err)
	}
	if provider.calls != 0 {
		t.Fatalf("provider must not be reached, got %d calls", provider.calls)
	}
	if got := testutil.ToFloat64(m.LedgerFailures.WithLabelValues("reserve")); got != 1 {
		t.Fatalf("ledger failure counter = %v, want 1", got)
	}
}

func TestOrchestratorRecordsSettlementFailureMetric(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	m := NewMetricsWithRegistry(prometheus.NewRegistry())
	o.SetMetrics(m)
	ledger := newFakeLedger()
	ledger.failSettle = true
	o.SetBudgetLedger(ledger)
	o.RegisterProviderAs("primary", &fakeProvider{
		name: "aliyun",
		resp: &CompletionResponse{Content: "ok", InputTokens: 100, OutputTokens: 10},
	})

	// A failed settlement must not fail the call; the reservation simply stays
	// counted until startup reconciliation.
	if _, err := o.Complete(enforcedDispatchContext("op-1"), CompletionRequest{User: "test"}); err != nil {
		t.Fatalf("a settlement failure must not fail the completion, got %v", err)
	}
	if got := testutil.ToFloat64(m.LedgerFailures.WithLabelValues("settle_trusted")); got != 1 {
		t.Fatalf("settlement failure counter = %v, want 1", got)
	}

	entry, err := ledger.Lookup(context.Background(), budget.DispatchIdentity{
		WorkspaceID: "default", OperationKind: budget.OperationEnhance,
		OperationID: "op-1", LogicalAttempt: 1, PhysicalOrdinal: budget.OrdinalPrimary,
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != budget.StatePossiblyDispatched {
		t.Fatalf("state = %q, want the reservation to stay counted", entry.State)
	}
}

func TestOrchestratorPublishesBudgetGaugesFromLedgerTransitions(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	m := NewMetricsWithRegistry(prometheus.NewRegistry())
	o.SetMetrics(m)
	ledger := newFakeLedger()
	o.SetBudgetLedger(ledger)
	o.RegisterProviderAs("primary", &fakeProvider{
		name: "aliyun",
		resp: &CompletionResponse{Content: "ok", InputTokens: 100, OutputTokens: 10},
	})

	if _, err := o.Complete(enforcedDispatchContext("op-gauges"), CompletionRequest{User: "test"}); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(m.BudgetSettled); got <= 0 {
		t.Fatalf("settled gauge = %v, want a positive exact settlement", got)
	}
	if got := testutil.ToFloat64(m.BudgetActive); got != 0 {
		t.Fatalf("active gauge = %v, want 0 after settlement", got)
	}
	if got := testutil.ToFloat64(m.BudgetRemaining); got >= 10_000_000_000 {
		t.Fatalf("remaining gauge = %v, want less than the daily cap", got)
	}
	if got := testutil.ToFloat64(m.BudgetUtilization); got <= 0 {
		t.Fatalf("utilization = %v, want a positive ratio", got)
	}
}

func TestOrchestratorOldBudgetDayCannotOverwriteCurrentGauges(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-rollover")
	o := NewOrchestrator(cfg, zap.NewNop())
	m := NewMetricsWithRegistry(prometheus.NewRegistry())
	o.SetMetrics(m)
	ledger := newFakeLedger()
	o.SetBudgetLedger(ledger)

	current := time.Date(2026, time.July, 31, 0, 0, 1, 0, time.UTC)
	o.SetClock(func() time.Time { return current })
	ledger.entries["old"] = &budget.Entry{
		BudgetDay: "2026-07-30",
		State:     budget.StateSettled,
		Settled:   9_000_000_000,
	}
	ledger.entries["current"] = &budget.Entry{
		BudgetDay: "2026-07-31",
		State:     budget.StateSettled,
		Settled:   1_000_000_000,
	}

	if err := o.RefreshBudgetMetrics(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(m.BudgetSettled); got != 1_000_000_000 {
		t.Fatalf("current-day settled gauge = %v", got)
	}

	o.observeBudgetSnapshot(context.Background(), ledger)
	if got := testutil.ToFloat64(m.BudgetSettled); got != 1_000_000_000 {
		t.Fatalf("old-day settlement overwrote current gauge: %v", got)
	}
}

func TestBudgetMetricRefreshRetriesWhenUTCDayChangesDuringSnapshot(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-rollover-retry")
	o := NewOrchestrator(cfg, zap.NewNop())
	m := NewMetricsWithRegistry(prometheus.NewRegistry())
	o.SetMetrics(m)
	current := time.Date(2026, time.July, 30, 23, 59, 59, 0, time.UTC)
	o.SetClock(func() time.Time { return current })

	base := newFakeLedger()
	base.entries["old"] = &budget.Entry{
		BudgetDay: "2026-07-30",
		State:     budget.StateSettled,
		Settled:   111,
	}
	base.entries["new"] = &budget.Entry{
		BudgetDay: "2026-07-31",
		State:     budget.StateSettled,
		Settled:   222,
	}
	ledger := &rolloverSnapshotLedger{
		fakeLedger: base,
		afterFirst: func() {
			current = time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC)
		},
	}
	o.SetBudgetLedger(ledger)

	if err := o.RefreshBudgetMetrics(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ledger.requestedDays) != 2 ||
		ledger.requestedDays[0] != "2026-07-30" ||
		ledger.requestedDays[1] != "2026-07-31" {
		t.Fatalf("snapshot days = %v, want rollover retry", ledger.requestedDays)
	}
	if got := testutil.ToFloat64(m.BudgetSettled); got != 222 {
		t.Fatalf("settled gauge = %v, want new UTC day value 222", got)
	}
}

func TestBudgetMetricRefreshHonorsContextWhileAnotherSnapshotIsRunning(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-refresh-timeout")
	o := NewOrchestrator(cfg, zap.NewNop())
	o.SetMetrics(NewMetricsWithRegistry(prometheus.NewRegistry()))
	ledger := &blockingSnapshotLedger{
		fakeLedger: newFakeLedger(),
		entered:    make(chan struct{}, 1),
		release:    make(chan struct{}),
	}
	o.SetBudgetLedger(ledger)

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- o.RefreshBudgetMetrics(context.Background())
	}()
	select {
	case <-ledger.entered:
	case <-time.After(time.Second):
		t.Fatal("first budget snapshot did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- o.RefreshBudgetMetrics(ctx)
	}()

	select {
	case err := <-secondDone:
		close(ledger.release)
		if firstErr := <-firstDone; firstErr != nil {
			t.Fatalf("first refresh failed: %v", firstErr)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("contended refresh error = %v, want context deadline exceeded", err)
		}
	case <-time.After(250 * time.Millisecond):
		close(ledger.release)
		<-firstDone
		<-secondDone
		t.Fatal("contended refresh ignored its context deadline")
	}
}

func TestBudgetMetricLabelsCollapseUnknownValues(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetricsWithRegistry(reg)
	m.RecordBudgetDenial("tenant-from-request", "provider-defined-limit")
	m.RecordLedgerFailure("workspace-user-input")
	m.RecordUncertainUsage("provider-body-value")
	m.RecordPhysicalCall("tenant-route", "provider-body-value")

	if got := testutil.ToFloat64(m.BudgetRejections.WithLabelValues("unknown", "unknown")); got != 1 {
		t.Fatalf("unknown budget denial labels = %v, want bounded unknown/unknown", got)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "opencrawler_llm_budget_rejections_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetValue() == "tenant-from-request" || label.GetValue() == "provider-defined-limit" {
					t.Fatalf("budget denial metric retained unbounded label value %q", label.GetValue())
				}
			}
		}
	}
	if got := testutil.ToFloat64(m.LedgerFailures.WithLabelValues("other")); got != 1 {
		t.Fatalf("unknown ledger operation = %v, want bounded other label", got)
	}
	if got := testutil.ToFloat64(m.UncertainUsage.WithLabelValues("other")); got != 1 {
		t.Fatalf("unknown uncertain reason = %v, want bounded other label", got)
	}
	if got := testutil.ToFloat64(m.PhysicalCalls.WithLabelValues("unknown", "other")); got != 1 {
		t.Fatalf("unknown physical call labels = %v, want bounded unknown/other", got)
	}
}
