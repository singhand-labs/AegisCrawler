package budget

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
)

// Stable error codes surfaced to synchronous APIs and persisted on durable
// jobs. Synchronous handlers map CodeBudgetExceeded to HTTP 429 and
// CodeLedgerUnavailable to HTTP 503. Neither schedules an automatic retry.
const (
	CodeBudgetExceeded        = "LLM_BUDGET_EXCEEDED"
	CodeLedgerUnavailable     = "BUDGET_LEDGER_UNAVAILABLE"
	CodeWorkflowBudgetExceeded = "WORKFLOW_BUDGET_EXCEEDED"
)

// Ledger state machine:
//
//	reserved
//	  ├─> released
//	  └─> possibly_dispatched
//	        ├─> settled
//	        └─> usage_uncertain
const (
	StateReserved           = "reserved"
	StateReleased           = "released"
	StatePossiblyDispatched = "possibly_dispatched"
	StateSettled            = "settled"
	StateUsageUncertain     = "usage_uncertain"
)

// Physical ordinals within one logical attempt. Ordinal 0 is the primary route
// and ordinal 1 is the single permitted fallback.
const (
	OrdinalPrimary  = 0
	OrdinalFallback = 1
)

// Scope and limit kinds for denial reporting and bounded-cardinality metrics.
const (
	ScopeGlobal    = "global"
	ScopeWorkspace = "workspace"

	LimitRequest = "request"
	LimitDaily   = "daily"
)

// ErrLedgerUnavailable reports that the ledger could not reach a decision. It
// never permits a provider call.
var ErrLedgerUnavailable = errors.New(CodeLedgerUnavailable)

// ErrDuplicateDispatch reports that the dispatch identity already exists. The
// caller must return the retained terminal result or fail closed; it must never
// send the provider request again.
var ErrDuplicateDispatch = errors.New("duplicate dispatch identity")

// ErrNotFound reports that no entry exists for a dispatch identity.
var ErrNotFound = errors.New("budget ledger entry not found")

// DeniedError reports that admission was refused by an exact cap. It is
// terminal for the physical call that requested it.
type DeniedError struct {
	Scope     string
	Limit     string
	Requested config.USDNanos
	Remaining config.USDNanos
	// Reason carries sanitized detail for audit and logs, such as a workspace
	// having no configured budget at all.
	Reason string
	// Code carries a stable error code that overrides CodeBudgetExceeded when
	// set, so callers can distinguish the source of the denial (for example,
	// the per-workflow envelope vs. the global/workspace cap). Empty falls
	// back to CodeBudgetExceeded for backward compatibility.
	Code string
}

func (e *DeniedError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("%s: %s %s cap would be exceeded (requested %d usd nanos, remaining %d): %s",
			e.CodeString(), e.Scope, e.Limit, e.Requested, e.Remaining, e.Reason)
	}
	return fmt.Sprintf("%s: %s %s cap would be exceeded (requested %d usd nanos, remaining %d)",
		e.CodeString(), e.Scope, e.Limit, e.Requested, e.Remaining)
}

// CodeString returns the stable error code for API and durable-job mapping.
// It returns the Code field when set, otherwise the historical default.
func (e *DeniedError) CodeString() string {
	if e.Code != "" {
		return e.Code
	}
	return CodeBudgetExceeded
}

// IsDenied reports whether an error is an exact-cap admission denial.
func IsDenied(err error) bool {
	var denied *DeniedError
	return errors.As(err, &denied)
}

// IsUnavailable reports whether an error is a ledger availability failure.
func IsUnavailable(err error) bool {
	return errors.Is(err, ErrLedgerUnavailable)
}

// OperationKind identifies the workflow family that owns a logical attempt. It
// is part of the durable dispatch identity, so values are stable strings.
type OperationKind string

const (
	OperationEnhance     OperationKind = "enhance"
	OperationRequirement OperationKind = "requirement"
	OperationDSL         OperationKind = "dsl"
	OperationIntent      OperationKind = "intent"
	OperationEval        OperationKind = "eval"
)

// DispatchIdentity is the stable logical identity of one physical call. The
// tuple is unique in the ledger and is shared with attempt artifacts,
// completion traces, cache lineage, and logs.
type DispatchIdentity struct {
	WorkspaceID     string
	OperationKind   OperationKind
	OperationID     string
	LogicalAttempt  int
	PhysicalOrdinal int
}

// Validate rejects an identity that could not be attributed to an
// authenticated workspace and a server-owned operation. Enforced mode must
// never substitute a default workspace for unattributable work.
func (d DispatchIdentity) Validate() error {
	if strings.TrimSpace(d.WorkspaceID) == "" {
		return fmt.Errorf("dispatch identity requires an authenticated workspace")
	}
	if strings.TrimSpace(string(d.OperationKind)) == "" {
		return fmt.Errorf("dispatch identity requires an operation kind")
	}
	if strings.TrimSpace(d.OperationID) == "" {
		return fmt.Errorf("dispatch identity requires an operation id")
	}
	if d.LogicalAttempt < 1 {
		return fmt.Errorf("dispatch identity requires a positive logical attempt")
	}
	if d.PhysicalOrdinal != OrdinalPrimary && d.PhysicalOrdinal != OrdinalFallback {
		return fmt.Errorf("dispatch identity physical ordinal must be %d or %d", OrdinalPrimary, OrdinalFallback)
	}
	return nil
}

// String renders a stable identity for map keys and bounded error context. The
// workspace is hashed so duplicate-dispatch errors never expose a tenant ID.
func (d DispatchIdentity) String() string {
	workspaceHash := sha256.Sum256([]byte(d.WorkspaceID))
	return fmt.Sprintf("%x/%s/%s/%d/%d", workspaceHash, d.OperationKind, d.OperationID, d.LogicalAttempt, d.PhysicalOrdinal)
}

// ReserveRequest is the immutable admission request for one physical call. It
// carries the exact rate and cap snapshot so a later restart never re-prices
// history.
type ReserveRequest struct {
	Identity          DispatchIdentity
	BudgetDay         string
	RouteSlot         string
	Provider          string
	Model             string
	Endpoint          string
	PolicyFingerprint string
	PriceRevision     string
	RequestHash       string
	Rates             Rates
	Caps              Caps
	Reserved          config.USDNanos
}

// Validate rejects a structurally unusable reservation before it reaches SQLite.
func (r ReserveRequest) Validate() error {
	if err := r.Identity.Validate(); err != nil {
		return err
	}
	if err := ValidateBudgetDay(r.BudgetDay); err != nil {
		return err
	}
	if r.RouteSlot != "primary" && r.RouteSlot != "fallback" {
		return fmt.Errorf("route slot must be primary or fallback")
	}
	if (r.RouteSlot == "primary") != (r.Identity.PhysicalOrdinal == OrdinalPrimary) {
		return fmt.Errorf("route slot %q does not match physical ordinal %d", r.RouteSlot, r.Identity.PhysicalOrdinal)
	}
	for name, value := range map[string]string{
		"provider":           r.Provider,
		"model":              r.Model,
		"endpoint":           r.Endpoint,
		"policy fingerprint": r.PolicyFingerprint,
		"price revision":     r.PriceRevision,
		"request hash":       r.RequestHash,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("reservation requires a %s", name)
		}
	}
	if r.Caps.MaxInputTokens <= 0 || r.Caps.MaxOutputTokens <= 0 {
		return fmt.Errorf("reservation requires positive token caps")
	}
	if r.Reserved <= 0 {
		return fmt.Errorf("reservation amount must be positive")
	}
	expected, err := Reservation(r.Rates, r.Caps)
	if err != nil {
		return err
	}
	if expected != r.Reserved {
		return fmt.Errorf("reservation amount %d does not match the rate and cap snapshot %d", r.Reserved, expected)
	}
	return nil
}

// Entry is one persisted physical-call identity and its ledger state.
type Entry struct {
	ID                string
	Identity          DispatchIdentity
	BudgetDay         string
	RouteSlot         string
	Provider          string
	Model             string
	Endpoint          string
	PolicyFingerprint string
	PriceRevision     string
	RequestHash       string
	Rates             Rates
	Caps              Caps
	Reserved          config.USDNanos
	Settled           config.USDNanos
	Usage             Usage
	State             string
	ErrorClass        string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	DispatchedAt      *time.Time
	TerminalAt        *time.Time
}

// Terminal reports whether the entry can no longer change.
func (e Entry) Terminal() bool {
	return e.State == StateReleased || e.State == StateSettled || e.State == StateUsageUncertain
}

// Counted reports whether the entry's reserved amount still consumes budget.
// Released entries do not count; usage_uncertain counts its full reservation.
func (e Entry) Counted() bool {
	return e.State != StateReleased
}

// CountedAmount is the USD nanos this entry consumes from its daily caps.
func (e Entry) CountedAmount() config.USDNanos {
	switch e.State {
	case StateReleased:
		return 0
	case StateSettled:
		return e.Settled
	default:
		// reserved, possibly_dispatched, and usage_uncertain all count the
		// full worst-case reservation.
		return e.Reserved
	}
}

// ScopeUsage is the settled, active, and remaining amounts for one scope.
type ScopeUsage struct {
	MaxRequestUSD  config.USDNanos
	DailyBudgetUSD config.USDNanos
	Settled        config.USDNanos
	ActiveReserved config.USDNanos
	Remaining      config.USDNanos
	UncertainCalls int
}

// Snapshot is the sanitized read-only ledger view for Admin and metrics. It
// contains no credentials, prompts, or provider response content.
type Snapshot struct {
	BudgetDay string
	Global    ScopeUsage
	Workspace *ScopeUsage
}

// RecoveryReport summarizes startup reconciliation of nonterminal entries.
type RecoveryReport struct {
	Released        int
	MarkedUncertain int
	Inspected       int
}

// Ledger is the narrow persistence boundary the orchestration layer depends
// on. The SQLite implementation lives in the store package so an external
// transactional service can replace it without changing provider or workflow
// contracts.
type Ledger interface {
	// Reserve atomically admits one physical call against both request caps
	// and both UTC-daily caps, committing before any network call. It returns
	// a *DeniedError for an exact-cap denial and ErrLedgerUnavailable when it
	// cannot reach a decision.
	Reserve(ctx context.Context, req ReserveRequest) (*Entry, error)

	// MarkPossiblyDispatched persists the dispatch marker that must be durable
	// before the provider network call begins.
	MarkPossiblyDispatched(ctx context.Context, identity DispatchIdentity) error

	// SettleTrusted records validated usage and its exact cost.
	SettleTrusted(ctx context.Context, identity DispatchIdentity, usage Usage, cost config.USDNanos, errorClass string) error

	// SettleUncertain settles the full reservation for a call whose dispatch or
	// usage is ambiguous.
	SettleUncertain(ctx context.Context, identity DispatchIdentity, reason string) error

	// Release returns a reservation that is definitely pre-dispatch. It must
	// fail for an entry already marked possibly dispatched.
	Release(ctx context.Context, identity DispatchIdentity, reason string) error

	// Lookup returns the persisted entry for an identity, or nil when absent.
	Lookup(ctx context.Context, identity DispatchIdentity) (*Entry, error)

	// Recover reconciles every nonterminal entry to a conservative terminal
	// state at startup. It never reconstructs or resends a dispatch.
	Recover(ctx context.Context) (RecoveryReport, error)

	// Snapshot returns the sanitized state for a UTC day. An empty workspace
	// returns global state only.
	Snapshot(ctx context.Context, workspaceID, budgetDay string) (Snapshot, error)
}

// WorkflowBudgetLedger admits a single DSL workflow call against its
// per-workflow envelope and records exact settled spend. It is the narrow
// store-side boundary the orchestrator depends on; the implementation lives in
// the store package so the budget package never imports store.
type WorkflowBudgetLedger interface {
	// CheckAndReserveWorkflowBudget returns nil if the workflow's spent plus
	// estimateNanos does not exceed its persisted budget. On denial it returns
	// a *DeniedError with Code=CodeWorkflowBudgetExceeded.
	CheckAndReserveWorkflowBudget(ctx context.Context, workflowID string, estimateNanos config.USDNanos) error
	// AddWorkflowSpend increments the workflow's spent_usd_nanos by costNanos.
	// It is best-effort: a failure is logged by the caller but must not fail
	// the response path.
	AddWorkflowSpend(ctx context.Context, workflowID string, costNanos config.USDNanos) error
}

// BudgetDay renders the UTC budget day key for an instant. Budget windows are
// exact UTC calendar days; there is no monthly or rolling window.
func BudgetDay(at time.Time) string {
	return at.UTC().Format("2006-01-02")
}

// ValidateBudgetDay rejects a malformed or non-canonical budget day key.
func ValidateBudgetDay(day string) error {
	parsed, err := time.Parse("2006-01-02", day)
	if err != nil {
		return fmt.Errorf("budget day must be a UTC calendar day: %w", err)
	}
	if parsed.UTC().Format("2006-01-02") != day {
		return fmt.Errorf("budget day %q is not canonical", day)
	}
	return nil
}
