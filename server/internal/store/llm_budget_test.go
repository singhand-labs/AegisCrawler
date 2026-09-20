package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

const (
	// One call reserves 68_000_001 USD nanos under the test rates and caps.
	testCallReservation = config.USDNanos(68_000_001)
	testBudgetDay       = "2026-07-30"
)

func testLedgerRates() budget.Rates {
	return budget.Rates{
		InputUSDPerMillion:       600_000_000,
		CachedInputUSDPerMillion: 150_000_000,
		OutputUSDPerMillion:      2_000_000_000,
	}
}

func testLedgerCaps() budget.Caps {
	return budget.Caps{MaxInputTokens: 100_000, MaxOutputTokens: 4_000}
}

// testLimits allows 10 calls globally and 5 per workspace on a single UTC day.
func testLimits() budget.Limits {
	return budget.Limits{
		GlobalMaxRequestUSD:  100_000_000,
		GlobalDailyBudgetUSD: testCallReservation * 10,
		Workspaces: map[string]config.WorkspaceBudget{
			"default": {MaxRequestUSD: 100_000_000, DailyBudgetUSD: testCallReservation * 5},
			"other":   {MaxRequestUSD: 100_000_000, DailyBudgetUSD: testCallReservation * 5},
		},
	}
}

func newTestLedger(t *testing.T, s *Store, limits budget.Limits) *BudgetLedger {
	t.Helper()
	ledger := NewBudgetLedger(s, limits)
	ledger.SetClock(func() time.Time { return time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC) })
	return ledger
}

// newSharedTestStores opens count independent Store handles over one database
// file so tests can prove that genuinely separate connections cannot overspend.
func newSharedTestStores(t *testing.T, count int) []*Store {
	t.Helper()
	f, err := os.CreateTemp("", "aegis-budget-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	stores := make([]*Store, 0, count)
	for i := 0; i < count; i++ {
		s, err := New(f.Name(), "")
		if err != nil {
			t.Fatalf("open store %d: %v", i, err)
		}
		t.Cleanup(func() { s.Close() })
		stores = append(stores, s)
	}
	return stores
}

func setBudgetTestBusyTimeout(t *testing.T, ctx context.Context, s *Store, timeout time.Duration) {
	t.Helper()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatalf("open connection for busy timeout: %v", err)
	}
	defer conn.Close()

	timeoutMillis := timeout.Milliseconds()
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA busy_timeout = %d`, timeoutMillis)); err != nil {
		t.Fatalf("set busy timeout: %v", err)
	}
	var configured int64
	if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&configured); err != nil {
		t.Fatalf("read busy timeout: %v", err)
	}
	if configured != timeoutMillis {
		t.Fatalf("busy timeout = %dms, want %dms", configured, timeoutMillis)
	}
}

func copyStoppedSQLiteDB(t *testing.T, source, destination string) {
	t.Helper()

	src, err := os.Open(source)
	if err != nil {
		t.Fatalf("open stopped source database: %v", err)
	}
	defer src.Close()

	dst, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create restored database: %v", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		t.Fatalf("copy stopped database: %v", err)
	}
	if err := dst.Close(); err != nil {
		t.Fatalf("close restored database: %v", err)
	}
}

func testReserveFor(workspaceID, operationID string, ordinal int) budget.ReserveRequest {
	slot := "primary"
	if ordinal == budget.OrdinalFallback {
		slot = "fallback"
	}
	reserved, err := budget.Reservation(testLedgerRates(), testLedgerCaps())
	if err != nil {
		panic(err)
	}
	return budget.ReserveRequest{
		Identity: budget.DispatchIdentity{
			WorkspaceID:     workspaceID,
			OperationKind:   budget.OperationDSL,
			OperationID:     operationID,
			LogicalAttempt:  1,
			PhysicalOrdinal: ordinal,
		},
		BudgetDay:         testBudgetDay,
		RouteSlot:         slot,
		Provider:          "aliyun",
		Model:             "qwen-max",
		Endpoint:          "https://example.invalid/v1",
		PolicyFingerprint: "fingerprint",
		PriceRevision:     "2026-07-01-contract-v3",
		RequestHash:       "hash-" + operationID,
		Rates:             testLedgerRates(),
		Caps:              testLedgerCaps(),
		Reserved:          reserved,
	}
}

func TestBudgetLedgerReserveCommitsBeforeDispatch(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := newTestLedger(t, s, testLimits())

	entry, err := ledger.Reserve(ctx, testReserveFor("default", "job-1", budget.OrdinalPrimary))
	if err != nil {
		t.Fatalf("Reserve returned error: %v", err)
	}
	if entry.State != budget.StateReserved {
		t.Fatalf("state = %q, want %q", entry.State, budget.StateReserved)
	}
	if entry.Reserved != testCallReservation {
		t.Fatalf("reserved = %d, want %d", entry.Reserved, testCallReservation)
	}
	if entry.DispatchedAt != nil {
		t.Fatal("a fresh reservation must not carry a dispatch marker")
	}

	// The reservation is committed and already counts against the day.
	snapshot, err := ledger.Snapshot(ctx, "default", testBudgetDay)
	if err != nil {
		t.Fatalf("Snapshot returned error: %v", err)
	}
	if snapshot.Global.ActiveReserved != testCallReservation {
		t.Fatalf("global active reserved = %d, want %d", snapshot.Global.ActiveReserved, testCallReservation)
	}
	if snapshot.Global.Settled != 0 {
		t.Fatalf("global settled = %d, want 0", snapshot.Global.Settled)
	}
}

func TestBudgetLedgerFullLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := newTestLedger(t, s, testLimits())
	req := testReserveFor("default", "job-1", budget.OrdinalPrimary)

	if _, err := ledger.Reserve(ctx, req); err != nil {
		t.Fatalf("Reserve returned error: %v", err)
	}
	if err := ledger.MarkPossiblyDispatched(ctx, req.Identity); err != nil {
		t.Fatalf("MarkPossiblyDispatched returned error: %v", err)
	}

	usage := budget.Usage{UncachedInputTokens: 10_000, OutputTokens: 500}
	cost, err := budget.SettledCost(testLedgerRates(), usage)
	if err != nil {
		t.Fatalf("SettledCost returned error: %v", err)
	}
	if err := ledger.SettleTrusted(ctx, req.Identity, usage, cost, ""); err != nil {
		t.Fatalf("SettleTrusted returned error: %v", err)
	}

	entry, err := ledger.Lookup(ctx, req.Identity)
	if err != nil {
		t.Fatalf("Lookup returned error: %v", err)
	}
	if entry.State != budget.StateSettled {
		t.Fatalf("state = %q, want %q", entry.State, budget.StateSettled)
	}
	if entry.Settled != cost {
		t.Fatalf("settled = %d, want %d", entry.Settled, cost)
	}
	if entry.DispatchedAt == nil || entry.TerminalAt == nil {
		t.Fatal("a settled entry must carry dispatch and terminal timestamps")
	}

	// Settlement replaces the active reservation with the exact cost.
	snapshot, err := ledger.Snapshot(ctx, "default", testBudgetDay)
	if err != nil {
		t.Fatalf("Snapshot returned error: %v", err)
	}
	if snapshot.Global.ActiveReserved != 0 {
		t.Fatalf("global active reserved = %d, want 0", snapshot.Global.ActiveReserved)
	}
	if snapshot.Global.Settled != cost {
		t.Fatalf("global settled = %d, want %d", snapshot.Global.Settled, cost)
	}

	// Every transition is recorded in the append-only audit table.
	var transitions int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM llm_budget_transitions WHERE reservation_id = ?`, entry.ID,
	).Scan(&transitions); err != nil {
		t.Fatal(err)
	}
	if transitions != 3 {
		t.Fatalf("expected reserve, dispatch, and settle transitions, got %d", transitions)
	}
}

func TestBudgetLedgerUncertainSettlesFullReservation(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := newTestLedger(t, s, testLimits())
	req := testReserveFor("default", "job-1", budget.OrdinalPrimary)

	if _, err := ledger.Reserve(ctx, req); err != nil {
		t.Fatalf("Reserve returned error: %v", err)
	}
	if err := ledger.MarkPossiblyDispatched(ctx, req.Identity); err != nil {
		t.Fatalf("MarkPossiblyDispatched returned error: %v", err)
	}
	if err := ledger.SettleUncertain(ctx, req.Identity, "missing_usage"); err != nil {
		t.Fatalf("SettleUncertain returned error: %v", err)
	}

	entry, err := ledger.Lookup(ctx, req.Identity)
	if err != nil {
		t.Fatalf("Lookup returned error: %v", err)
	}
	if entry.State != budget.StateUsageUncertain {
		t.Fatalf("state = %q, want %q", entry.State, budget.StateUsageUncertain)
	}
	if entry.Settled != entry.Reserved {
		t.Fatalf("uncertain settled = %d, want the full reservation %d", entry.Settled, entry.Reserved)
	}

	snapshot, err := ledger.Snapshot(ctx, "default", testBudgetDay)
	if err != nil {
		t.Fatalf("Snapshot returned error: %v", err)
	}
	if snapshot.Global.UncertainCalls != 1 {
		t.Fatalf("uncertain calls = %d, want 1", snapshot.Global.UncertainCalls)
	}
	if snapshot.Global.Settled != testCallReservation {
		t.Fatalf("settled = %d, want the full reservation %d", snapshot.Global.Settled, testCallReservation)
	}
}

func TestBudgetLedgerReleaseOnlyBeforeDispatch(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := newTestLedger(t, s, testLimits())

	released := testReserveFor("default", "job-1", budget.OrdinalPrimary)
	if _, err := ledger.Reserve(ctx, released); err != nil {
		t.Fatalf("Reserve returned error: %v", err)
	}
	if err := ledger.Release(ctx, released.Identity, "pre_dispatch_validation"); err != nil {
		t.Fatalf("Release returned error: %v", err)
	}
	entry, err := ledger.Lookup(ctx, released.Identity)
	if err != nil {
		t.Fatalf("Lookup returned error: %v", err)
	}
	if entry.State != budget.StateReleased || entry.Settled != 0 {
		t.Fatalf("released entry = %q with settled %d, want released with 0", entry.State, entry.Settled)
	}

	// A released reservation must not consume any budget.
	snapshot, err := ledger.Snapshot(ctx, "default", testBudgetDay)
	if err != nil {
		t.Fatalf("Snapshot returned error: %v", err)
	}
	if snapshot.Global.ActiveReserved != 0 || snapshot.Global.Settled != 0 {
		t.Fatalf("a released reservation must not be counted, got reserved %d settled %d",
			snapshot.Global.ActiveReserved, snapshot.Global.Settled)
	}

	// After the dispatch marker, release must fail because the call may have billed.
	dispatched := testReserveFor("default", "job-2", budget.OrdinalPrimary)
	if _, err := ledger.Reserve(ctx, dispatched); err != nil {
		t.Fatalf("Reserve returned error: %v", err)
	}
	if err := ledger.MarkPossiblyDispatched(ctx, dispatched.Identity); err != nil {
		t.Fatalf("MarkPossiblyDispatched returned error: %v", err)
	}
	if err := ledger.Release(ctx, dispatched.Identity, "too_late"); err == nil {
		t.Fatal("expected releasing a possibly dispatched entry to fail")
	}
}

func TestBudgetLedgerRejectsDuplicateDispatchIdentity(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := newTestLedger(t, s, testLimits())
	req := testReserveFor("default", "job-1", budget.OrdinalPrimary)

	first, err := ledger.Reserve(ctx, req)
	if err != nil {
		t.Fatalf("Reserve returned error: %v", err)
	}

	existing, err := ledger.Reserve(ctx, req)
	if !errors.Is(err, budget.ErrDuplicateDispatch) {
		t.Fatalf("expected ErrDuplicateDispatch, got %v", err)
	}
	if existing == nil || existing.ID != first.ID {
		t.Fatal("a duplicate reservation must return the retained entry so the caller can fail closed")
	}

	// The duplicate attempt must not have created a second reservation.
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM llm_budget_reservations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one reservation, got %d", count)
	}
}

func TestBudgetLedgerDeniesAtRequestCaps(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	limits := testLimits()
	// One call costs more than the global per-request cap allows.
	limits.GlobalMaxRequestUSD = testCallReservation - 1
	ledger := newTestLedger(t, s, limits)

	_, err := ledger.Reserve(ctx, testReserveFor("default", "job-1", budget.OrdinalPrimary))
	if !budget.IsDenied(err) {
		t.Fatalf("expected a denial, got %v", err)
	}
	var denied *budget.DeniedError
	if !errors.As(err, &denied) || denied.Scope != budget.ScopeGlobal || denied.Limit != budget.LimitRequest {
		t.Fatalf("expected a global request denial, got %v", err)
	}

	// No provider call may be admitted, so nothing is persisted.
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM llm_budget_reservations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("a denied reservation must not persist a row, got %d", count)
	}
}

func TestBudgetLedgerDeniesAtWorkspaceRequestCap(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	limits := testLimits()
	limits.Workspaces["default"] = config.WorkspaceBudget{
		MaxRequestUSD:  testCallReservation - 1,
		DailyBudgetUSD: testCallReservation * 5,
	}
	ledger := newTestLedger(t, s, limits)

	_, err := ledger.Reserve(ctx, testReserveFor("default", "job-1", budget.OrdinalPrimary))
	var denied *budget.DeniedError
	if !errors.As(err, &denied) || denied.Scope != budget.ScopeWorkspace || denied.Limit != budget.LimitRequest {
		t.Fatalf("expected a workspace request denial, got %v", err)
	}
}

func TestBudgetLedgerRequestCapBoundaryIsInclusive(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	limits := testLimits()
	// A reservation exactly equal to both request caps must be admitted.
	limits.GlobalMaxRequestUSD = testCallReservation
	limits.Workspaces["default"] = config.WorkspaceBudget{
		MaxRequestUSD:  testCallReservation,
		DailyBudgetUSD: testCallReservation * 5,
	}
	ledger := newTestLedger(t, s, limits)

	if _, err := ledger.Reserve(ctx, testReserveFor("default", "job-1", budget.OrdinalPrimary)); err != nil {
		t.Fatalf("a reservation exactly at the request cap must be admitted, got %v", err)
	}
}

func TestBudgetLedgerDeniesAtDailyCapBoundary(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	limits := testLimits()
	// Exactly three calls fit in the workspace daily cap.
	limits.Workspaces["default"] = config.WorkspaceBudget{
		MaxRequestUSD:  100_000_000,
		DailyBudgetUSD: testCallReservation * 3,
	}
	ledger := newTestLedger(t, s, limits)

	for i := 0; i < 3; i++ {
		if _, err := ledger.Reserve(ctx, testReserveFor("default", fmt.Sprintf("job-%d", i), budget.OrdinalPrimary)); err != nil {
			t.Fatalf("call %d must be admitted, got %v", i, err)
		}
	}

	_, err := ledger.Reserve(ctx, testReserveFor("default", "job-overflow", budget.OrdinalPrimary))
	var denied *budget.DeniedError
	if !errors.As(err, &denied) || denied.Scope != budget.ScopeWorkspace || denied.Limit != budget.LimitDaily {
		t.Fatalf("expected a workspace daily denial, got %v", err)
	}
	if denied.Remaining != 0 {
		t.Fatalf("remaining = %d, want 0", denied.Remaining)
	}
}

func TestBudgetLedgerDeniesAtGlobalDailyCap(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateWorkspace(context.Background(), &models.Workspace{ID: "other", Name: "other"}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	limits := testLimits()
	// Two calls globally, but each workspace could otherwise afford five.
	limits.GlobalDailyBudgetUSD = testCallReservation * 2
	ledger := newTestLedger(t, s, limits)

	if _, err := ledger.Reserve(ctx, testReserveFor("default", "job-1", budget.OrdinalPrimary)); err != nil {
		t.Fatalf("first call must be admitted: %v", err)
	}
	if _, err := ledger.Reserve(ctx, testReserveFor("other", "job-2", budget.OrdinalPrimary)); err != nil {
		t.Fatalf("second call must be admitted: %v", err)
	}

	// The global emergency cap binds even though the workspace cap has room.
	_, err := ledger.Reserve(ctx, testReserveFor("default", "job-3", budget.OrdinalPrimary))
	var denied *budget.DeniedError
	if !errors.As(err, &denied) || denied.Scope != budget.ScopeGlobal || denied.Limit != budget.LimitDaily {
		t.Fatalf("expected a global daily denial, got %v", err)
	}
}

func TestBudgetLedgerCountsActiveReservationsAgainstDailyCap(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	limits := testLimits()
	limits.Workspaces["default"] = config.WorkspaceBudget{
		MaxRequestUSD:  100_000_000,
		DailyBudgetUSD: testCallReservation,
	}
	ledger := newTestLedger(t, s, limits)

	// The first reservation is never settled, so it stays active and must still
	// block the second admission.
	if _, err := ledger.Reserve(ctx, testReserveFor("default", "job-1", budget.OrdinalPrimary)); err != nil {
		t.Fatalf("first call must be admitted: %v", err)
	}
	if _, err := ledger.Reserve(ctx, testReserveFor("default", "job-2", budget.OrdinalPrimary)); !budget.IsDenied(err) {
		t.Fatalf("an active reservation must consume the daily cap, got %v", err)
	}
}

func TestBudgetLedgerReleasedReservationFreesDailyCap(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	limits := testLimits()
	limits.Workspaces["default"] = config.WorkspaceBudget{
		MaxRequestUSD:  100_000_000,
		DailyBudgetUSD: testCallReservation,
	}
	ledger := newTestLedger(t, s, limits)

	first := testReserveFor("default", "job-1", budget.OrdinalPrimary)
	if _, err := ledger.Reserve(ctx, first); err != nil {
		t.Fatalf("first call must be admitted: %v", err)
	}
	if err := ledger.Release(ctx, first.Identity, "pre_dispatch"); err != nil {
		t.Fatalf("Release returned error: %v", err)
	}
	if _, err := ledger.Reserve(ctx, testReserveFor("default", "job-2", budget.OrdinalPrimary)); err != nil {
		t.Fatalf("a released reservation must free the cap, got %v", err)
	}
}

func TestBudgetLedgerUncertainKeepsConsumingDailyCap(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	limits := testLimits()
	limits.Workspaces["default"] = config.WorkspaceBudget{
		MaxRequestUSD:  100_000_000,
		DailyBudgetUSD: testCallReservation,
	}
	ledger := newTestLedger(t, s, limits)

	first := testReserveFor("default", "job-1", budget.OrdinalPrimary)
	if _, err := ledger.Reserve(ctx, first); err != nil {
		t.Fatalf("first call must be admitted: %v", err)
	}
	if err := ledger.MarkPossiblyDispatched(ctx, first.Identity); err != nil {
		t.Fatalf("MarkPossiblyDispatched returned error: %v", err)
	}
	if err := ledger.SettleUncertain(ctx, first.Identity, "timeout"); err != nil {
		t.Fatalf("SettleUncertain returned error: %v", err)
	}

	// An uncertain call keeps its full worst-case cost, so the cap stays consumed.
	if _, err := ledger.Reserve(ctx, testReserveFor("default", "job-2", budget.OrdinalPrimary)); !budget.IsDenied(err) {
		t.Fatalf("an uncertain call must keep consuming the cap, got %v", err)
	}
}

func TestBudgetLedgerFallbackAdmittedSeparatelyAndCanBeDenied(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	limits := testLimits()
	// Exactly one call fits, so the primary consumes the whole cap and the
	// fallback admission is denied. That denial is terminal.
	limits.Workspaces["default"] = config.WorkspaceBudget{
		MaxRequestUSD:  100_000_000,
		DailyBudgetUSD: testCallReservation,
	}
	ledger := newTestLedger(t, s, limits)

	primary := testReserveFor("default", "job-1", budget.OrdinalPrimary)
	if _, err := ledger.Reserve(ctx, primary); err != nil {
		t.Fatalf("primary must be admitted: %v", err)
	}
	if err := ledger.MarkPossiblyDispatched(ctx, primary.Identity); err != nil {
		t.Fatalf("MarkPossiblyDispatched returned error: %v", err)
	}
	// The primary may have billed, so its full reservation stays settled before
	// fallback admission.
	if err := ledger.SettleUncertain(ctx, primary.Identity, "timeout"); err != nil {
		t.Fatalf("SettleUncertain returned error: %v", err)
	}

	fallback := testReserveFor("default", "job-1", budget.OrdinalFallback)
	if _, err := ledger.Reserve(ctx, fallback); !budget.IsDenied(err) {
		t.Fatalf("expected the fallback admission to be denied, got %v", err)
	}
}

func TestBudgetLedgerFallbackAdmittedWhenBudgetRemains(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := newTestLedger(t, s, testLimits())

	primary := testReserveFor("default", "job-1", budget.OrdinalPrimary)
	if _, err := ledger.Reserve(ctx, primary); err != nil {
		t.Fatalf("primary must be admitted: %v", err)
	}
	if err := ledger.MarkPossiblyDispatched(ctx, primary.Identity); err != nil {
		t.Fatalf("MarkPossiblyDispatched returned error: %v", err)
	}
	if err := ledger.SettleUncertain(ctx, primary.Identity, "timeout"); err != nil {
		t.Fatalf("SettleUncertain returned error: %v", err)
	}

	fallback := testReserveFor("default", "job-1", budget.OrdinalFallback)
	entry, err := ledger.Reserve(ctx, fallback)
	if err != nil {
		t.Fatalf("fallback must be admitted separately: %v", err)
	}
	if entry.RouteSlot != "fallback" || entry.Identity.PhysicalOrdinal != budget.OrdinalFallback {
		t.Fatalf("fallback entry = %q ordinal %d", entry.RouteSlot, entry.Identity.PhysicalOrdinal)
	}
}

func TestBudgetLedgerDeniesUnbudgetedWorkspace(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateWorkspace(ctx, &models.Workspace{ID: "unbudgeted", Name: "unbudgeted"}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	ledger := newTestLedger(t, s, testLimits())

	_, err := ledger.Reserve(ctx, testReserveFor("unbudgeted", "job-1", budget.OrdinalPrimary))
	var denied *budget.DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected a denial for an unbudgeted workspace, got %v", err)
	}
	if denied.Scope != budget.ScopeWorkspace {
		t.Fatalf("scope = %q, want %q", denied.Scope, budget.ScopeWorkspace)
	}
}

func TestBudgetLedgerIsolatesWorkspaceDailyCaps(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateWorkspace(ctx, &models.Workspace{ID: "other", Name: "other"}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	limits := testLimits()
	limits.Workspaces["default"] = config.WorkspaceBudget{
		MaxRequestUSD:  100_000_000,
		DailyBudgetUSD: testCallReservation,
	}
	ledger := newTestLedger(t, s, limits)

	if _, err := ledger.Reserve(ctx, testReserveFor("default", "job-1", budget.OrdinalPrimary)); err != nil {
		t.Fatalf("first workspace call must be admitted: %v", err)
	}
	if _, err := ledger.Reserve(ctx, testReserveFor("default", "job-2", budget.OrdinalPrimary)); !budget.IsDenied(err) {
		t.Fatal("expected the exhausted workspace to be denied")
	}
	// A different workspace has its own cap and is unaffected.
	if _, err := ledger.Reserve(ctx, testReserveFor("other", "job-3", budget.OrdinalPrimary)); err != nil {
		t.Fatalf("an independent workspace must keep its own cap: %v", err)
	}

	snapshot, err := ledger.Snapshot(ctx, "other", testBudgetDay)
	if err != nil {
		t.Fatalf("Snapshot returned error: %v", err)
	}
	if snapshot.Workspace.ActiveReserved != testCallReservation {
		t.Fatalf("other workspace active = %d, want %d", snapshot.Workspace.ActiveReserved, testCallReservation)
	}
	if snapshot.Global.ActiveReserved != testCallReservation*2 {
		t.Fatalf("global active = %d, want %d", snapshot.Global.ActiveReserved, testCallReservation*2)
	}
}

func TestBudgetLedgerSeparatesUTCDays(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	limits := testLimits()
	limits.Workspaces["default"] = config.WorkspaceBudget{
		MaxRequestUSD:  100_000_000,
		DailyBudgetUSD: testCallReservation,
	}
	ledger := newTestLedger(t, s, limits)

	if _, err := ledger.Reserve(ctx, testReserveFor("default", "job-1", budget.OrdinalPrimary)); err != nil {
		t.Fatalf("first day call must be admitted: %v", err)
	}
	if _, err := ledger.Reserve(ctx, testReserveFor("default", "job-2", budget.OrdinalPrimary)); !budget.IsDenied(err) {
		t.Fatal("expected the exhausted day to be denied")
	}

	// The next UTC day starts with a fresh cap.
	ledger.SetClock(func() time.Time {
		return time.Date(2026, 7, 31, 0, 0, 1, 0, time.UTC)
	})
	nextDay := testReserveFor("default", "job-3", budget.OrdinalPrimary)
	nextDay.BudgetDay = "2026-07-31"
	if _, err := ledger.Reserve(ctx, nextDay); err != nil {
		t.Fatalf("the next UTC day must have a fresh cap: %v", err)
	}

	// Each day reports independently.
	first, err := ledger.Snapshot(ctx, "default", testBudgetDay)
	if err != nil {
		t.Fatalf("Snapshot returned error: %v", err)
	}
	second, err := ledger.Snapshot(ctx, "default", "2026-07-31")
	if err != nil {
		t.Fatalf("Snapshot returned error: %v", err)
	}
	if first.Global.ActiveReserved != testCallReservation || second.Global.ActiveReserved != testCallReservation {
		t.Fatalf("each day must count only its own calls, got %d and %d",
			first.Global.ActiveReserved, second.Global.ActiveReserved)
	}
}

// TestBudgetLedgerConcurrentAdmissionCannotOverspend proves that many
// goroutines racing for the last slots cannot admit aggregate worst-case cost
// beyond the configured daily cap.
func TestBudgetLedgerConcurrentAdmissionCannotOverspend(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	const allowed = 5
	limits := testLimits()
	limits.GlobalDailyBudgetUSD = testCallReservation * allowed
	limits.Workspaces["default"] = config.WorkspaceBudget{
		MaxRequestUSD:  100_000_000,
		DailyBudgetUSD: testCallReservation * allowed,
	}
	ledger := newTestLedger(t, s, limits)

	const attempts = 40
	var admitted, denied int64
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := ledger.Reserve(ctx, testReserveFor("default", fmt.Sprintf("job-%d", i), budget.OrdinalPrimary))
			switch {
			case err == nil:
				atomic.AddInt64(&admitted, 1)
			case budget.IsDenied(err):
				atomic.AddInt64(&denied, 1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if admitted != allowed {
		t.Fatalf("admitted %d calls, want exactly %d", admitted, allowed)
	}
	if admitted+denied != attempts {
		t.Fatalf("admitted %d + denied %d != %d attempts", admitted, denied, attempts)
	}

	snapshot, err := ledger.Snapshot(ctx, "default", testBudgetDay)
	if err != nil {
		t.Fatalf("Snapshot returned error: %v", err)
	}
	total := snapshot.Global.Settled + snapshot.Global.ActiveReserved
	if total > limits.GlobalDailyBudgetUSD {
		t.Fatalf("admitted worst-case cost %d exceeds the global daily cap %d", total, limits.GlobalDailyBudgetUSD)
	}
}

// TestBudgetLedgerConcurrentAdmissionAcrossConnectionsCannotOverspend runs the
// same race across genuinely independent database connections, which is the
// case a single-connection pool would otherwise hide.
func TestBudgetLedgerConcurrentAdmissionAcrossConnectionsCannotOverspend(t *testing.T) {
	ctx := context.Background()
	const connections = 4
	stores := newSharedTestStores(t, connections)

	const allowed = 6
	limits := testLimits()
	limits.GlobalDailyBudgetUSD = testCallReservation * allowed
	limits.Workspaces["default"] = config.WorkspaceBudget{
		MaxRequestUSD:  100_000_000,
		DailyBudgetUSD: testCallReservation * allowed,
	}

	ledgers := make([]*BudgetLedger, 0, connections)
	for _, s := range stores {
		ledgers = append(ledgers, newTestLedger(t, s, limits))
	}

	const perLedger = 10
	var admitted, denied int64
	var wg sync.WaitGroup
	for index, ledger := range ledgers {
		for i := 0; i < perLedger; i++ {
			wg.Add(1)
			go func(ledger *BudgetLedger, index, i int) {
				defer wg.Done()
				operationID := fmt.Sprintf("job-%d-%d", index, i)
				_, err := ledger.Reserve(ctx, testReserveFor("default", operationID, budget.OrdinalPrimary))
				switch {
				case err == nil:
					atomic.AddInt64(&admitted, 1)
				case budget.IsDenied(err):
					atomic.AddInt64(&denied, 1)
				default:
					t.Errorf("unexpected error: %v", err)
				}
			}(ledger, index, i)
		}
	}
	wg.Wait()

	if admitted != allowed {
		t.Fatalf("admitted %d calls across %d connections, want exactly %d", admitted, connections, allowed)
	}

	snapshot, err := ledgers[0].Snapshot(ctx, "default", testBudgetDay)
	if err != nil {
		t.Fatalf("Snapshot returned error: %v", err)
	}
	total := snapshot.Global.Settled + snapshot.Global.ActiveReserved
	if total > limits.GlobalDailyBudgetUSD {
		t.Fatalf("admitted worst-case cost %d exceeds the global daily cap %d", total, limits.GlobalDailyBudgetUSD)
	}
}

func TestBudgetLedgerWriteLockRetryExhaustionFailsClosed(t *testing.T) {
	stores := newSharedTestStores(t, 2)
	holder, candidate := stores[0], stores[1]
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	setBudgetTestBusyTimeout(t, ctx, candidate, 0)

	holderConn, err := holder.db.Conn(ctx)
	if err != nil {
		t.Fatalf("open independent holder connection: %v", err)
	}
	defer holderConn.Close()
	if _, err := holderConn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("hold independent write lock: %v", err)
	}
	lockHeld := true
	defer func() {
		if lockHeld {
			rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer rollbackCancel()
			_, _ = holderConn.ExecContext(rollbackCtx, `ROLLBACK`)
		}
	}()

	ledger := newTestLedger(t, candidate, testLimits())
	started := time.Now()
	entry, err := ledger.Reserve(ctx, testReserveFor("default", "locked-job", budget.OrdinalPrimary))
	elapsed := time.Since(started)
	if entry != nil {
		t.Fatalf("locked Reserve returned an entry: %+v", entry)
	}
	if !errors.Is(err, budget.ErrLedgerUnavailable) {
		t.Fatalf("locked Reserve error = %v, want ErrLedgerUnavailable", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("bounded ledger retries outlived their 10s test deadline: %v", ctx.Err())
	}
	var configuredRetryWait time.Duration
	for _, delay := range ledgerRetryDelays {
		configuredRetryWait += delay
	}
	if elapsed < configuredRetryWait {
		t.Fatalf("Reserve returned after %s, before configured retry waits totaling %s",
			elapsed, configuredRetryWait)
	}

	if _, err := holderConn.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatalf("release independent write lock: %v", err)
	}
	lockHeld = false

	var reservations, transitions int
	if err := candidate.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM llm_budget_reservations`).Scan(&reservations); err != nil {
		t.Fatalf("count reservations after retry exhaustion: %v", err)
	}
	if err := candidate.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM llm_budget_transitions`).Scan(&transitions); err != nil {
		t.Fatalf("count transitions after retry exhaustion: %v", err)
	}
	if reservations != 0 || transitions != 0 {
		t.Fatalf("retry exhaustion committed reservations/transitions = %d/%d, want 0/0",
			reservations, transitions)
	}
}

func TestBudgetLedgerWriteLockReleasedWithinRetryWindowSucceeds(t *testing.T) {
	stores := newSharedTestStores(t, 2)
	holder, candidate := stores[0], stores[1]
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Keep the production retry count and ordering, but widen the first wait so
	// the test can observe the initial busy failure before releasing the lock.
	// Tests in this package are serial; the channel receive below synchronizes
	// the worker before the original slice is restored.
	originalRetryDelays := ledgerRetryDelays
	ledgerRetryDelays = []time.Duration{500 * time.Millisecond, time.Second}
	defer func() { ledgerRetryDelays = originalRetryDelays }()
	setBudgetTestBusyTimeout(t, ctx, candidate, 100*time.Millisecond)

	holderConn, err := holder.db.Conn(ctx)
	if err != nil {
		t.Fatalf("open independent holder connection: %v", err)
	}
	defer holderConn.Close()
	if _, err := holderConn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("hold independent write lock: %v", err)
	}
	lockHeld := true
	defer func() {
		if lockHeld {
			rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer rollbackCancel()
			_, _ = holderConn.ExecContext(rollbackCtx, `ROLLBACK`)
		}
	}()

	type reserveResult struct {
		entry *budget.Entry
		err   error
	}
	result := make(chan reserveResult, 1)
	ledger := newTestLedger(t, candidate, testLimits())
	started := time.Now()
	go func() {
		entry, err := ledger.Reserve(
			ctx, testReserveFor("default", "unlocked-job", budget.OrdinalPrimary),
		)
		result <- reserveResult{entry: entry, err: err}
	}()

	// busy_timeout keeps the first BEGIN IMMEDIATE in use long enough to
	// observe. Returning to zero in-use connections proves that attempt failed
	// and the ledger is inside its bounded retry wait.
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	observedFirstAttempt := false
	for {
		select {
		case early := <-result:
			t.Fatalf("Reserve completed before the initial busy attempt was observed: entry=%+v err=%v",
				early.entry, early.err)
		case <-ticker.C:
			inUse := candidate.db.Stats().InUse
			if !observedFirstAttempt {
				observedFirstAttempt = inUse > 0
				continue
			}
			if inUse == 0 {
				goto releaseLock
			}
		case <-ctx.Done():
			t.Fatalf("did not observe the initial busy attempt complete: %v", ctx.Err())
		}
	}

releaseLock:
	rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if _, err := holderConn.ExecContext(rollbackCtx, `ROLLBACK`); err != nil {
		rollbackCancel()
		t.Fatalf("release independent write lock: %v", err)
	}
	rollbackCancel()
	lockHeld = false

	var reserved reserveResult
	select {
	case reserved = <-result:
	case <-ctx.Done():
		t.Fatalf("Reserve did not finish inside the retry window: %v", ctx.Err())
	}
	if reserved.err != nil {
		t.Fatalf("Reserve after lock release returned error: %v", reserved.err)
	}
	if reserved.entry == nil || reserved.entry.State != budget.StateReserved {
		t.Fatalf("Reserve after lock release = %+v, want a reserved entry", reserved.entry)
	}
	if elapsed := time.Since(started); elapsed < ledgerRetryDelays[0] {
		t.Fatalf("Reserve succeeded after %s, before the confirmed retry wait %s",
			elapsed, ledgerRetryDelays[0])
	}

	var reservations, transitions int
	if err := candidate.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM llm_budget_reservations`).Scan(&reservations); err != nil {
		t.Fatalf("count reservations after lock release: %v", err)
	}
	if err := candidate.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM llm_budget_transitions`).Scan(&transitions); err != nil {
		t.Fatalf("count transitions after lock release: %v", err)
	}
	if reservations != 1 || transitions != 1 {
		t.Fatalf("successful retry committed reservations/transitions = %d/%d, want 1/1",
			reservations, transitions)
	}
}

func TestBudgetLedgerRecoveryReleasesUndispatchedAndChargesDispatched(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := newTestLedger(t, s, testLimits())

	undispatched := testReserveFor("default", "job-1", budget.OrdinalPrimary)
	if _, err := ledger.Reserve(ctx, undispatched); err != nil {
		t.Fatalf("Reserve returned error: %v", err)
	}

	dispatched := testReserveFor("default", "job-2", budget.OrdinalPrimary)
	if _, err := ledger.Reserve(ctx, dispatched); err != nil {
		t.Fatalf("Reserve returned error: %v", err)
	}
	if err := ledger.MarkPossiblyDispatched(ctx, dispatched.Identity); err != nil {
		t.Fatalf("MarkPossiblyDispatched returned error: %v", err)
	}

	// A settled entry is already terminal and must be left alone.
	settled := testReserveFor("default", "job-3", budget.OrdinalPrimary)
	if _, err := ledger.Reserve(ctx, settled); err != nil {
		t.Fatalf("Reserve returned error: %v", err)
	}
	if err := ledger.MarkPossiblyDispatched(ctx, settled.Identity); err != nil {
		t.Fatalf("MarkPossiblyDispatched returned error: %v", err)
	}
	if err := ledger.SettleTrusted(ctx, settled.Identity, budget.Usage{UncachedInputTokens: 10, OutputTokens: 5}, 16_000, ""); err != nil {
		t.Fatalf("SettleTrusted returned error: %v", err)
	}

	report, err := ledger.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover returned error: %v", err)
	}
	if report.Inspected != 2 || report.Released != 1 || report.MarkedUncertain != 1 {
		t.Fatalf("report = %+v, want 2 inspected, 1 released, 1 uncertain", report)
	}

	undispatchedEntry, err := ledger.Lookup(ctx, undispatched.Identity)
	if err != nil {
		t.Fatalf("Lookup returned error: %v", err)
	}
	if undispatchedEntry.State != budget.StateReleased {
		t.Fatalf("undispatched entry = %q, want released", undispatchedEntry.State)
	}

	dispatchedEntry, err := ledger.Lookup(ctx, dispatched.Identity)
	if err != nil {
		t.Fatalf("Lookup returned error: %v", err)
	}
	if dispatchedEntry.State != budget.StateUsageUncertain {
		t.Fatalf("dispatched entry = %q, want usage_uncertain", dispatchedEntry.State)
	}
	if dispatchedEntry.Settled != dispatchedEntry.Reserved {
		t.Fatalf("recovered dispatch settled %d, want the full reservation %d",
			dispatchedEntry.Settled, dispatchedEntry.Reserved)
	}

	settledEntry, err := ledger.Lookup(ctx, settled.Identity)
	if err != nil {
		t.Fatalf("Lookup returned error: %v", err)
	}
	if settledEntry.State != budget.StateSettled || settledEntry.Settled != 16_000 {
		t.Fatalf("a terminal settled entry must not be rewritten, got %q with %d",
			settledEntry.State, settledEntry.Settled)
	}
}

func TestBudgetLedgerRecoveryIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := newTestLedger(t, s, testLimits())

	req := testReserveFor("default", "job-1", budget.OrdinalPrimary)
	if _, err := ledger.Reserve(ctx, req); err != nil {
		t.Fatalf("Reserve returned error: %v", err)
	}
	if _, err := ledger.Recover(ctx); err != nil {
		t.Fatalf("first Recover returned error: %v", err)
	}
	report, err := ledger.Recover(ctx)
	if err != nil {
		t.Fatalf("second Recover returned error: %v", err)
	}
	if report.Inspected != 0 {
		t.Fatalf("a second reconciliation must find nothing, got %+v", report)
	}
}

func TestBudgetLedgerStoppedDatabaseCopyPreservesAndRecoversState(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.db")
	restoredPath := filepath.Join(dir, "restored.db")

	limits := testLimits()
	source, err := New(sourcePath, "")
	if err != nil {
		t.Fatalf("open source store: %v", err)
	}
	ledger := newTestLedger(t, source, limits)

	settled := testReserveFor("default", "settled-job", budget.OrdinalPrimary)
	if _, err := ledger.Reserve(ctx, settled); err != nil {
		t.Fatalf("Reserve returned error: %v", err)
	}
	if err := ledger.MarkPossiblyDispatched(ctx, settled.Identity); err != nil {
		t.Fatalf("MarkPossiblyDispatched returned error: %v", err)
	}
	if err := ledger.SettleTrusted(ctx, settled.Identity, budget.Usage{UncachedInputTokens: 100, OutputTokens: 10}, 80_000, ""); err != nil {
		t.Fatalf("SettleTrusted returned error: %v", err)
	}

	reserved := testReserveFor("default", "reserved-job", budget.OrdinalPrimary)
	if _, err := ledger.Reserve(ctx, reserved); err != nil {
		t.Fatalf("Reserve returned error: %v", err)
	}

	dispatched := testReserveFor("default", "dispatched-job", budget.OrdinalPrimary)
	if _, err := ledger.Reserve(ctx, dispatched); err != nil {
		t.Fatalf("Reserve returned error: %v", err)
	}
	if err := ledger.MarkPossiblyDispatched(ctx, dispatched.Identity); err != nil {
		t.Fatalf("MarkPossiblyDispatched returned error: %v", err)
	}

	before, err := ledger.Snapshot(ctx, "default", testBudgetDay)
	if err != nil {
		t.Fatalf("Snapshot returned error: %v", err)
	}
	if before.Global.Settled != 80_000 || before.Global.ActiveReserved != 2*testCallReservation {
		t.Fatalf("source snapshot settled/active = %d/%d, want %d/%d",
			before.Global.Settled, before.Global.ActiveReserved, 80_000, 2*testCallReservation)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("stop source store before copy: %v", err)
	}

	// Copy only after every source connection is stopped. Reopening a distinct
	// path exercises the actual backup/restore shape rather than a same-file
	// process restart.
	copyStoppedSQLiteDB(t, sourcePath, restoredPath)
	restoredStore, err := New(restoredPath, "")
	if err != nil {
		t.Fatalf("open restored store: %v", err)
	}
	t.Cleanup(func() { restoredStore.Close() })
	restored := newTestLedger(t, restoredStore, limits)

	after, err := restored.Snapshot(ctx, "default", testBudgetDay)
	if err != nil {
		t.Fatalf("Snapshot returned error: %v", err)
	}
	if after.Global.Settled != before.Global.Settled {
		t.Fatalf("settled did not survive restart: %d then %d", before.Global.Settled, after.Global.Settled)
	}
	if after.Global.ActiveReserved != before.Global.ActiveReserved {
		t.Fatalf("active reservation did not survive restart: %d then %d",
			before.Global.ActiveReserved, after.Global.ActiveReserved)
	}
	if after.Global.Remaining != before.Global.Remaining {
		t.Fatalf("remaining did not survive restore: %d then %d", before.Global.Remaining, after.Global.Remaining)
	}

	var reservationCount, transitionCount int
	if err := restoredStore.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM llm_budget_reservations`).Scan(&reservationCount); err != nil {
		t.Fatalf("count restored reservations: %v", err)
	}
	if err := restoredStore.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM llm_budget_transitions`).Scan(&transitionCount); err != nil {
		t.Fatalf("count restored transitions: %v", err)
	}
	if reservationCount != 3 || transitionCount != 6 {
		t.Fatalf("restored reservations/transitions = %d/%d, want 3/6",
			reservationCount, transitionCount)
	}

	// Terminal and active states, plus their immutable policy and pricing
	// snapshots, must survive the stopped-file copy exactly.
	settledEntry, err := restored.Lookup(ctx, settled.Identity)
	if err != nil {
		t.Fatalf("Lookup returned error: %v", err)
	}
	if settledEntry.State != budget.StateSettled || !settledEntry.Terminal() || settledEntry.Settled != 80_000 {
		t.Fatalf("restored settled entry = %+v", settledEntry)
	}
	if settledEntry.PolicyFingerprint != settled.PolicyFingerprint ||
		settledEntry.PriceRevision != settled.PriceRevision ||
		settledEntry.Rates != testLedgerRates() ||
		settledEntry.Caps != testLedgerCaps() {
		t.Fatal("restored terminal entry did not retain its policy, rate, and cap snapshots")
	}
	reservedEntry, err := restored.Lookup(ctx, reserved.Identity)
	if err != nil {
		t.Fatalf("Lookup reserved entry: %v", err)
	}
	if reservedEntry.State != budget.StateReserved || reservedEntry.Terminal() {
		t.Fatalf("restored reserved entry = %+v, want active reserved", reservedEntry)
	}
	dispatchedEntry, err := restored.Lookup(ctx, dispatched.Identity)
	if err != nil {
		t.Fatalf("Lookup dispatched entry: %v", err)
	}
	if dispatchedEntry.State != budget.StatePossiblyDispatched || dispatchedEntry.Terminal() {
		t.Fatalf("restored dispatched entry = %+v, want active possibly_dispatched", dispatchedEntry)
	}
	if dispatchedEntry.PolicyFingerprint != dispatched.PolicyFingerprint ||
		dispatchedEntry.PriceRevision != dispatched.PriceRevision ||
		dispatchedEntry.Rates != testLedgerRates() ||
		dispatchedEntry.Caps != testLedgerCaps() {
		t.Fatal("restored active entry did not retain its policy, rate, and cap snapshots")
	}

	report, err := restored.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover restored ledger: %v", err)
	}
	if report.Inspected != 2 || report.Released != 1 || report.MarkedUncertain != 1 {
		t.Fatalf("recovery report = %+v, want 2 inspected, 1 released, 1 uncertain", report)
	}
	reservedEntry, err = restored.Lookup(ctx, reserved.Identity)
	if err != nil {
		t.Fatalf("Lookup recovered reserved entry: %v", err)
	}
	if reservedEntry.State != budget.StateReleased || !reservedEntry.Terminal() || reservedEntry.Settled != 0 {
		t.Fatalf("recovered undispatched entry = %+v, want terminal released with zero settlement", reservedEntry)
	}
	dispatchedEntry, err = restored.Lookup(ctx, dispatched.Identity)
	if err != nil {
		t.Fatalf("Lookup recovered dispatched entry: %v", err)
	}
	if dispatchedEntry.State != budget.StateUsageUncertain ||
		!dispatchedEntry.Terminal() ||
		dispatchedEntry.Settled != dispatchedEntry.Reserved {
		t.Fatalf("recovered dispatched entry = %+v, want full-reservation usage_uncertain", dispatchedEntry)
	}

	recoveredSnapshot, err := restored.Snapshot(ctx, "default", testBudgetDay)
	if err != nil {
		t.Fatalf("Snapshot after recovery: %v", err)
	}
	if recoveredSnapshot.Global.ActiveReserved != 0 ||
		recoveredSnapshot.Global.Settled != 80_000+testCallReservation {
		t.Fatalf("recovered settled/active = %d/%d, want %d/0",
			recoveredSnapshot.Global.Settled, recoveredSnapshot.Global.ActiveReserved,
			80_000+testCallReservation)
	}
	if err := restoredStore.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM llm_budget_transitions`).Scan(&transitionCount); err != nil {
		t.Fatalf("count transitions after recovery: %v", err)
	}
	if transitionCount != 8 {
		t.Fatalf("transitions after recovery = %d, want 8", transitionCount)
	}

	duplicate, err := restored.Reserve(ctx, dispatched)
	if !errors.Is(err, budget.ErrDuplicateDispatch) {
		t.Fatalf("Reserve recovered identity error = %v, want ErrDuplicateDispatch", err)
	}
	if duplicate == nil || duplicate.ID != dispatchedEntry.ID || duplicate.State != budget.StateUsageUncertain {
		t.Fatalf("duplicate Reserve returned %+v, want retained recovered entry %q",
			duplicate, dispatchedEntry.ID)
	}
}

func TestBudgetLedgerRejectsSettlementAboveReservation(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := newTestLedger(t, s, testLimits())
	req := testReserveFor("default", "job-1", budget.OrdinalPrimary)

	if _, err := ledger.Reserve(ctx, req); err != nil {
		t.Fatalf("Reserve returned error: %v", err)
	}
	if err := ledger.MarkPossiblyDispatched(ctx, req.Identity); err != nil {
		t.Fatalf("MarkPossiblyDispatched returned error: %v", err)
	}
	if err := ledger.SettleTrusted(ctx, req.Identity, budget.Usage{UncachedInputTokens: 10}, testCallReservation+1, ""); err == nil {
		t.Fatal("expected a settlement above the reservation to be rejected")
	}
}

func TestBudgetLedgerRejectsSettlementThatDoesNotMatchPersistedRates(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := newTestLedger(t, s, testLimits())
	req := testReserveFor("default", "job-1", budget.OrdinalPrimary)

	if _, err := ledger.Reserve(ctx, req); err != nil {
		t.Fatalf("Reserve returned error: %v", err)
	}
	if err := ledger.MarkPossiblyDispatched(ctx, req.Identity); err != nil {
		t.Fatalf("MarkPossiblyDispatched returned error: %v", err)
	}
	usage := budget.Usage{UncachedInputTokens: 10, OutputTokens: 5}
	exact, err := budget.SettledCost(testLedgerRates(), usage)
	if err != nil {
		t.Fatal(err)
	}
	for _, wrong := range []config.USDNanos{exact - 1, exact + 1} {
		if err := ledger.SettleTrusted(ctx, req.Identity, usage, wrong, ""); err == nil {
			t.Fatalf("expected settlement %d to be rejected; exact cost is %d", wrong, exact)
		}
		entry, lookupErr := ledger.Lookup(ctx, req.Identity)
		if lookupErr != nil {
			t.Fatal(lookupErr)
		}
		if entry.State != budget.StatePossiblyDispatched || entry.Settled != 0 {
			t.Fatalf("wrong settlement mutated entry: %+v", entry)
		}
	}
	if err := ledger.SettleTrusted(ctx, req.Identity, usage, exact, ""); err != nil {
		t.Fatalf("exact settlement failed: %v", err)
	}
}

func TestBudgetLedgerUsesTransactionTimeUTCday(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := newTestLedger(t, s, testLimits())
	ledger.SetClock(func() time.Time {
		return time.Date(2026, 7, 31, 0, 0, 1, 0, time.UTC)
	})
	req := testReserveFor("default", "job-1", budget.OrdinalPrimary)
	// Simulate a request prepared before midnight. The ledger must charge the
	// day observed after it owns the write transaction, not this stale value.
	req.BudgetDay = "2026-07-30"

	entry, err := ledger.Reserve(ctx, req)
	if err != nil {
		t.Fatalf("Reserve returned error: %v", err)
	}
	if entry.BudgetDay != "2026-07-31" {
		t.Fatalf("budget day = %q, want transaction-time day 2026-07-31", entry.BudgetDay)
	}
	oldDay, err := ledger.Snapshot(ctx, "default", "2026-07-30")
	if err != nil {
		t.Fatal(err)
	}
	newDay, err := ledger.Snapshot(ctx, "default", "2026-07-31")
	if err != nil {
		t.Fatal(err)
	}
	if oldDay.Global.ActiveReserved != 0 || newDay.Global.ActiveReserved != req.Reserved {
		t.Fatalf("stale/current active reservations = %d/%d, want 0/%d",
			oldDay.Global.ActiveReserved, newDay.Global.ActiveReserved, req.Reserved)
	}
}

func TestBudgetLedgerRejectsSettlementBeforeDispatchMarker(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := newTestLedger(t, s, testLimits())
	req := testReserveFor("default", "job-1", budget.OrdinalPrimary)

	if _, err := ledger.Reserve(ctx, req); err != nil {
		t.Fatalf("Reserve returned error: %v", err)
	}
	if err := ledger.SettleTrusted(ctx, req.Identity, budget.Usage{UncachedInputTokens: 10}, 1_000, ""); err == nil {
		t.Fatal("expected settling a reserved entry to be rejected")
	}
	if err := ledger.SettleUncertain(ctx, req.Identity, "reason"); err == nil {
		t.Fatal("expected marking a reserved entry uncertain to be rejected")
	}
}

func TestBudgetLedgerRejectsUsageBeyondPreparedBounds(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := newTestLedger(t, s, testLimits())
	req := testReserveFor("default", "job-1", budget.OrdinalPrimary)

	if _, err := ledger.Reserve(ctx, req); err != nil {
		t.Fatalf("Reserve returned error: %v", err)
	}
	if err := ledger.MarkPossiblyDispatched(ctx, req.Identity); err != nil {
		t.Fatalf("MarkPossiblyDispatched returned error: %v", err)
	}
	over := budget.Usage{UncachedInputTokens: testLedgerCaps().MaxInputTokens + 1}
	if err := ledger.SettleTrusted(ctx, req.Identity, over, 1_000, ""); err == nil {
		t.Fatal("expected usage beyond the prepared bounds to be rejected as trusted")
	}
}

func TestBudgetLedgerMarkDispatchedIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := newTestLedger(t, s, testLimits())
	req := testReserveFor("default", "job-1", budget.OrdinalPrimary)

	if _, err := ledger.Reserve(ctx, req); err != nil {
		t.Fatalf("Reserve returned error: %v", err)
	}
	if err := ledger.MarkPossiblyDispatched(ctx, req.Identity); err != nil {
		t.Fatalf("first MarkPossiblyDispatched returned error: %v", err)
	}
	if err := ledger.MarkPossiblyDispatched(ctx, req.Identity); err != nil {
		t.Fatalf("repeating the dispatch marker must be idempotent, got %v", err)
	}

	entry, err := ledger.Lookup(ctx, req.Identity)
	if err != nil {
		t.Fatalf("Lookup returned error: %v", err)
	}
	if entry.State != budget.StatePossiblyDispatched {
		t.Fatalf("state = %q, want %q", entry.State, budget.StatePossiblyDispatched)
	}
}

func TestBudgetLedgerRejectsUnknownIdentityOperations(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := newTestLedger(t, s, testLimits())
	identity := budget.DispatchIdentity{
		WorkspaceID:     "default",
		OperationKind:   budget.OperationDSL,
		OperationID:     "missing",
		LogicalAttempt:  1,
		PhysicalOrdinal: budget.OrdinalPrimary,
	}

	if err := ledger.MarkPossiblyDispatched(ctx, identity); !errors.Is(err, budget.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	entry, err := ledger.Lookup(ctx, identity)
	if err != nil {
		t.Fatalf("Lookup returned error: %v", err)
	}
	if entry != nil {
		t.Fatal("expected a nil entry for an unknown identity")
	}
}

func TestBudgetLedgerRejectsUnattributableIdentity(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := newTestLedger(t, s, testLimits())

	req := testReserveFor("default", "job-1", budget.OrdinalPrimary)
	req.Identity.WorkspaceID = ""
	_, err := ledger.Reserve(ctx, req)
	if err == nil {
		t.Fatal("expected a reservation without an authenticated workspace to be rejected")
	}
	if budget.IsDenied(err) {
		t.Fatal("an unattributable identity is a caller error, not a budget denial")
	}
}

func TestBudgetLedgerSnapshotOmitsWorkspaceWhenUnscoped(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := newTestLedger(t, s, testLimits())

	snapshot, err := ledger.Snapshot(ctx, "", testBudgetDay)
	if err != nil {
		t.Fatalf("Snapshot returned error: %v", err)
	}
	if snapshot.Workspace != nil {
		t.Fatal("an unscoped snapshot must not include workspace state")
	}
	if snapshot.Global.DailyBudgetUSD != testLimits().GlobalDailyBudgetUSD {
		t.Fatalf("global daily cap = %d, want %d",
			snapshot.Global.DailyBudgetUSD, testLimits().GlobalDailyBudgetUSD)
	}
	if snapshot.Global.Remaining != testLimits().GlobalDailyBudgetUSD {
		t.Fatalf("an empty day must report the full cap remaining, got %d", snapshot.Global.Remaining)
	}
}

func TestBudgetLedgerSnapshotUsesOneReadTransactionAcrossStores(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stores := newSharedTestStores(t, 2)
	reader := newTestLedger(t, stores[0], testLimits())
	writer := newTestLedger(t, stores[1], testLimits())
	if _, err := writer.Reserve(ctx, testReserveFor("default", "snapshot-before", budget.OrdinalPrimary)); err != nil {
		t.Fatalf("seed reservation: %v", err)
	}

	globalRead := make(chan struct{})
	continueWorkspaceRead := make(chan struct{})
	workspaceReadReleased := false
	defer func() {
		if !workspaceReadReleased {
			close(continueWorkspaceRead)
		}
	}()
	type snapshotResult struct {
		snapshot budget.Snapshot
		err      error
	}
	result := make(chan snapshotResult, 1)
	go func() {
		snapshot, err := reader.snapshotWithReadObserver(ctx, "default", testBudgetDay, func() {
			close(globalRead)
			<-continueWorkspaceRead
		})
		result <- snapshotResult{snapshot: snapshot, err: err}
	}()

	select {
	case <-globalRead:
	case <-ctx.Done():
		t.Fatalf("snapshot did not complete its global read: %v", ctx.Err())
	}
	if _, err := writer.Reserve(ctx, testReserveFor("default", "snapshot-between", budget.OrdinalPrimary)); err != nil {
		t.Fatalf("commit reservation between snapshot reads: %v", err)
	}
	close(continueWorkspaceRead)
	workspaceReadReleased = true

	read := <-result
	if read.err != nil {
		t.Fatalf("Snapshot returned error: %v", read.err)
	}
	if read.snapshot.Workspace == nil {
		t.Fatal("Snapshot omitted requested workspace")
	}
	if read.snapshot.Global.ActiveReserved != testCallReservation ||
		read.snapshot.Workspace.ActiveReserved != testCallReservation {
		t.Fatalf("cross-store snapshot mixed database versions: global=%d workspace=%d, want %d/%d",
			read.snapshot.Global.ActiveReserved, read.snapshot.Workspace.ActiveReserved,
			testCallReservation, testCallReservation)
	}

	fresh, err := reader.Snapshot(ctx, "default", testBudgetDay)
	if err != nil {
		t.Fatalf("fresh Snapshot returned error: %v", err)
	}
	wantFresh := testCallReservation * 2
	if fresh.Global.ActiveReserved != wantFresh ||
		fresh.Workspace == nil || fresh.Workspace.ActiveReserved != wantFresh {
		t.Fatalf("fresh snapshot did not observe committed reservation: %+v, want global/workspace=%d",
			fresh, wantFresh)
	}
}

func TestBudgetLedgerSnapshotRejectsMalformedDay(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := newTestLedger(t, s, testLimits())

	if _, err := ledger.Snapshot(ctx, "default", "2026-7-30"); err == nil {
		t.Fatal("expected a malformed budget day to be rejected")
	}
}

func TestBudgetLedgerRequiresConfiguredGlobalCaps(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := newTestLedger(t, s, budget.Limits{})

	if _, err := ledger.Reserve(ctx, testReserveFor("default", "job-1", budget.OrdinalPrimary)); err == nil {
		t.Fatal("expected an unconfigured ledger to refuse admission")
	}
}

func TestBudgetLedgerFailsClosedOnCancelledContext(t *testing.T) {
	s := newTestStore(t)
	ledger := newTestLedger(t, s, testLimits())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ledger.Reserve(ctx, testReserveFor("default", "job-1", budget.OrdinalPrimary))
	if err == nil {
		t.Fatal("expected a cancelled context to fail closed")
	}
	if budget.IsDenied(err) {
		t.Fatal("a cancelled context must not be reported as a budget denial")
	}

	// No reservation may have been committed.
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM llm_budget_reservations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected no committed reservation, got %d", count)
	}
}
