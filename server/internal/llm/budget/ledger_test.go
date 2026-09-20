package budget

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
)

func testRates() Rates {
	return Rates{
		InputUSDPerMillion:       600_000_000,
		CachedInputUSDPerMillion: 150_000_000,
		OutputUSDPerMillion:      2_000_000_000,
	}
}

func testCaps() Caps {
	return Caps{MaxInputTokens: 100_000, MaxOutputTokens: 4_000}
}

func testIdentity() DispatchIdentity {
	return DispatchIdentity{
		WorkspaceID:     "workspace-1",
		OperationKind:   OperationDSL,
		OperationID:     "job-1",
		LogicalAttempt:  1,
		PhysicalOrdinal: OrdinalPrimary,
	}
}

func testReserveRequest(t *testing.T) ReserveRequest {
	t.Helper()
	reserved, err := Reservation(testRates(), testCaps())
	if err != nil {
		t.Fatalf("Reservation returned error: %v", err)
	}
	return ReserveRequest{
		Identity:          testIdentity(),
		BudgetDay:         "2026-07-30",
		RouteSlot:         "primary",
		Provider:          "aliyun",
		Model:             "qwen-max",
		Endpoint:          "https://example.invalid/v1",
		PolicyFingerprint: "fingerprint",
		PriceRevision:     "2026-07-01-contract-v3",
		RequestHash:       "hash",
		Rates:             testRates(),
		Caps:              testCaps(),
		Reserved:          reserved,
	}
}

func TestDispatchIdentityValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*DispatchIdentity)
		wantErr bool
	}{
		{"valid primary", func(*DispatchIdentity) {}, false},
		{"valid fallback", func(d *DispatchIdentity) { d.PhysicalOrdinal = OrdinalFallback }, false},
		{"missing workspace", func(d *DispatchIdentity) { d.WorkspaceID = "" }, true},
		{"blank workspace", func(d *DispatchIdentity) { d.WorkspaceID = "   " }, true},
		{"missing kind", func(d *DispatchIdentity) { d.OperationKind = "" }, true},
		{"missing operation id", func(d *DispatchIdentity) { d.OperationID = "" }, true},
		{"zero attempt", func(d *DispatchIdentity) { d.LogicalAttempt = 0 }, true},
		{"negative attempt", func(d *DispatchIdentity) { d.LogicalAttempt = -1 }, true},
		{"third physical call", func(d *DispatchIdentity) { d.PhysicalOrdinal = 2 }, true},
		{"negative ordinal", func(d *DispatchIdentity) { d.PhysicalOrdinal = -1 }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			identity := testIdentity()
			tc.mutate(&identity)
			err := identity.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("expected an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestReserveRequestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*ReserveRequest)
		wantErr bool
	}{
		{"valid", func(*ReserveRequest) {}, false},
		{"unknown route slot", func(r *ReserveRequest) { r.RouteSlot = "tertiary" }, true},
		{"slot ordinal mismatch", func(r *ReserveRequest) { r.RouteSlot = "fallback" }, true},
		{"missing provider", func(r *ReserveRequest) { r.Provider = "" }, true},
		{"missing model", func(r *ReserveRequest) { r.Model = "" }, true},
		{"missing endpoint", func(r *ReserveRequest) { r.Endpoint = "" }, true},
		{"missing fingerprint", func(r *ReserveRequest) { r.PolicyFingerprint = "" }, true},
		{"missing price revision", func(r *ReserveRequest) { r.PriceRevision = "" }, true},
		{"missing request hash", func(r *ReserveRequest) { r.RequestHash = "" }, true},
		{"zero reservation", func(r *ReserveRequest) { r.Reserved = 0 }, true},
		{"negative reservation", func(r *ReserveRequest) { r.Reserved = -1 }, true},
		{"malformed budget day", func(r *ReserveRequest) { r.BudgetDay = "2026-7-30" }, true},
		{"non positive caps", func(r *ReserveRequest) { r.Caps.MaxInputTokens = 0 }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := testReserveRequest(t)
			tc.mutate(&req)
			err := req.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("expected an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestReserveRequestValidateRejectsUnderReservation(t *testing.T) {
	// A caller must not be able to admit a call at less than the worst-case
	// cost implied by its own rate and cap snapshot.
	req := testReserveRequest(t)
	req.Reserved -= 1
	if err := req.Validate(); err == nil {
		t.Fatal("expected a reservation below the rate and cap snapshot to be rejected")
	}
}

func TestReserveRequestValidateAcceptsMatchingFallback(t *testing.T) {
	req := testReserveRequest(t)
	req.RouteSlot = "fallback"
	req.Identity.PhysicalOrdinal = OrdinalFallback
	if err := req.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEntryCountedAmount(t *testing.T) {
	cases := []struct {
		state string
		want  config.USDNanos
	}{
		{StateReserved, 68_000_000},
		{StatePossiblyDispatched, 68_000_000},
		{StateUsageUncertain, 68_000_000},
		{StateSettled, 1_500_000},
		{StateReleased, 0},
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			entry := Entry{State: tc.state, Reserved: 68_000_000, Settled: 1_500_000}
			if got := entry.CountedAmount(); got != tc.want {
				t.Fatalf("CountedAmount for %s = %d, want %d", tc.state, got, tc.want)
			}
		})
	}
}

func TestEntryTerminalAndCounted(t *testing.T) {
	terminal := map[string]bool{
		StateReserved:           false,
		StatePossiblyDispatched: false,
		StateSettled:            true,
		StateUsageUncertain:     true,
		StateReleased:           true,
	}
	for state, wantTerminal := range terminal {
		entry := Entry{State: state}
		if got := entry.Terminal(); got != wantTerminal {
			t.Fatalf("Terminal for %s = %v, want %v", state, got, wantTerminal)
		}
		wantCounted := state != StateReleased
		if got := entry.Counted(); got != wantCounted {
			t.Fatalf("Counted for %s = %v, want %v", state, got, wantCounted)
		}
	}
}

func TestBudgetDayIsUTC(t *testing.T) {
	// An instant that is already the next day in UTC must report the UTC day,
	// not the local one.
	at := time.Date(2026, 7, 30, 23, 30, 0, 0, time.FixedZone("ahead", -6*60*60))
	if got := BudgetDay(at); got != "2026-07-31" {
		t.Fatalf("BudgetDay = %q, want 2026-07-31", got)
	}
	if got := BudgetDay(time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)); got != "2026-07-30" {
		t.Fatalf("BudgetDay = %q, want 2026-07-30", got)
	}
}

func TestValidateBudgetDay(t *testing.T) {
	for _, day := range []string{"2026-07-30", "2026-01-01", "2026-12-31"} {
		if err := ValidateBudgetDay(day); err != nil {
			t.Fatalf("ValidateBudgetDay(%q) returned error: %v", day, err)
		}
	}
	for _, day := range []string{"", "2026-7-30", "26-07-30", "2026-07-30T00:00:00Z", "2026-13-01", "not-a-day"} {
		if err := ValidateBudgetDay(day); err == nil {
			t.Fatalf("ValidateBudgetDay(%q) accepted a malformed day", day)
		}
	}
}

func TestDeniedErrorCarriesStableCode(t *testing.T) {
	err := error(&DeniedError{Scope: ScopeWorkspace, Limit: LimitDaily, Requested: 10, Remaining: 4})
	if !IsDenied(err) {
		t.Fatal("expected IsDenied to report true")
	}
	if IsUnavailable(err) {
		t.Fatal("a denial must not be reported as ledger unavailability")
	}
	var denied *DeniedError
	if !errors.As(err, &denied) || denied.CodeString() != CodeBudgetExceeded {
		t.Fatalf("expected the stable %s code", CodeBudgetExceeded)
	}
}

func TestUnavailableIsNotDenial(t *testing.T) {
	if !IsUnavailable(ErrLedgerUnavailable) {
		t.Fatal("expected IsUnavailable to report true")
	}
	if IsDenied(ErrLedgerUnavailable) {
		t.Fatal("ledger unavailability must not be reported as a denial")
	}
}

func TestDispatchIdentityStringHasNoSecrets(t *testing.T) {
	identity := testIdentity()
	identity.WorkspaceID = "workspace-secret-sentinel"
	got := identity.String()
	workspaceHash := sha256.Sum256([]byte(identity.WorkspaceID))
	want := fmt.Sprintf("%x/dsl/job-1/1/0", workspaceHash)
	if got != want {
		t.Fatalf("String = %q, want %q", got, want)
	}
	if strings.Contains(got, identity.WorkspaceID) {
		t.Fatalf("String() exposed raw workspace: %q", got)
	}

	err := fmt.Errorf("%w: %s", ErrDuplicateDispatch, identity)
	if !errors.Is(err, ErrDuplicateDispatch) {
		t.Fatalf("wrapped duplicate error lost its sentinel: %v", err)
	}
	if strings.Contains(err.Error(), identity.WorkspaceID) {
		t.Fatalf("duplicate-dispatch error exposed raw workspace: %q", err)
	}
}
