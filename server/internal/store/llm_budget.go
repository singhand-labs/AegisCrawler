package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
)

// ledgerRetryDelays are the bounded local retries after the initial attempt.
// Ledger operations therefore attempt the SQLite transaction at most three
// times, always within the caller context. Exhaustion returns
// BUDGET_LEDGER_UNAVAILABLE and never permits a provider call.
var ledgerRetryDelays = []time.Duration{25 * time.Millisecond, 100 * time.Millisecond}

// BudgetLedger is the transactional SQLite implementation of budget.Ledger. It
// stays behind that narrow interface so an external transactional service can
// replace it without changing provider or workflow contracts.
type BudgetLedger struct {
	store  *Store
	limits budget.Limits
	now    func() time.Time
}

// NewBudgetLedger binds a ledger to the immutable startup cap snapshot.
func NewBudgetLedger(s *Store, limits budget.Limits) *BudgetLedger {
	return &BudgetLedger{store: s, limits: limits, now: func() time.Time { return time.Now().UTC() }}
}

// SetClock overrides the ledger clock. It exists for deterministic UTC-rollover
// and reconciliation tests.
func (l *BudgetLedger) SetClock(now func() time.Time) {
	if now != nil {
		l.now = now
	}
}

// Limits returns the immutable cap snapshot this ledger admits against.
func (l *BudgetLedger) Limits() budget.Limits {
	return l.limits
}

// unavailable wraps an infrastructure failure as a ledger availability error.
// Admission denials and caller validation errors are returned unchanged so a
// denial is never misreported as an outage.
func unavailable(operation string, err error) error {
	return fmt.Errorf("%s: %w: %v", operation, budget.ErrLedgerUnavailable, err)
}

// retryableLedgerError reports whether a failure is a transient SQLite lock
// contention that a bounded local retry may clear.
func retryableLedgerError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "sqlite_busy") ||
		strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked")
}

// withImmediateTx runs fn inside one BEGIN IMMEDIATE write transaction on a
// pinned connection, with bounded local retries for lock contention. An
// immediate transaction takes the write lock up front so a reservation never
// upgrades from a read mid-decision.
func (l *BudgetLedger) withImmediateTx(ctx context.Context, operation string, fn func(context.Context, *sql.Conn) error) error {
	var lastErr error
	for attempt := 0; attempt <= len(ledgerRetryDelays); attempt++ {
		if attempt > 0 {
			delay := ledgerRetryDelays[attempt-1]
			select {
			case <-ctx.Done():
				return unavailable(operation, ctx.Err())
			case <-time.After(delay):
			}
		}
		err := l.runImmediateTx(ctx, fn)
		if err == nil {
			return nil
		}
		// A decision was reached; it must not be retried or reported as an outage.
		if budget.IsDenied(err) ||
			errors.Is(err, budget.ErrDuplicateDispatch) ||
			errors.Is(err, budget.ErrNotFound) ||
			errors.Is(err, errLedgerConflict) {
			return err
		}
		lastErr = err
		if !retryableLedgerError(err) {
			break
		}
	}
	return unavailable(operation, lastErr)
}

// errLedgerConflict reports an application-level state violation, such as
// settling an entry that is not marked possibly dispatched. It is a decision,
// not an outage, so it is never retried.
var errLedgerConflict = errors.New("budget ledger state conflict")

func (l *BudgetLedger) runImmediateTx(ctx context.Context, fn func(context.Context, *sql.Conn) error) (err error) {
	conn, connErr := l.store.db.Conn(ctx)
	if connErr != nil {
		return connErr
	}
	defer conn.Close()

	if _, execErr := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); execErr != nil {
		return execErr
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		// Roll back on a detached context so cancellation still releases the
		// write lock rather than leaving it held until the connection closes.
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, rollbackErr := conn.ExecContext(rollbackCtx, `ROLLBACK`); rollbackErr != nil && err == nil {
			err = rollbackErr
		}
	}()

	if fnErr := fn(ctx, conn); fnErr != nil {
		return fnErr
	}
	if _, commitErr := conn.ExecContext(ctx, `COMMIT`); commitErr != nil {
		return commitErr
	}
	committed = true
	return nil
}

const budgetEntryColumns = `
	id, workspace_id, operation_kind, operation_id, logical_attempt, physical_ordinal,
	budget_day, route_slot, provider, model, endpoint, policy_fingerprint,
	price_revision, request_hash, input_usd_per_million, cached_input_usd_per_million,
	output_usd_per_million, max_input_tokens, max_output_tokens, reserved_usd_nanos,
	settled_usd_nanos, uncached_input_tokens, cached_input_tokens, output_tokens,
	cached_input_reported, state, error_class, created_at, updated_at, dispatched_at, terminal_at`

type budgetRowScanner interface {
	Scan(dest ...any) error
}

func scanBudgetEntry(row budgetRowScanner) (*budget.Entry, error) {
	entry := &budget.Entry{}
	var cachedReported int
	var dispatchedAt, terminalAt sql.NullTime
	err := row.Scan(
		&entry.ID, &entry.Identity.WorkspaceID, &entry.Identity.OperationKind,
		&entry.Identity.OperationID, &entry.Identity.LogicalAttempt, &entry.Identity.PhysicalOrdinal,
		&entry.BudgetDay, &entry.RouteSlot, &entry.Provider, &entry.Model, &entry.Endpoint,
		&entry.PolicyFingerprint, &entry.PriceRevision, &entry.RequestHash,
		&entry.Rates.InputUSDPerMillion, &entry.Rates.CachedInputUSDPerMillion,
		&entry.Rates.OutputUSDPerMillion, &entry.Caps.MaxInputTokens, &entry.Caps.MaxOutputTokens,
		&entry.Reserved, &entry.Settled, &entry.Usage.UncachedInputTokens,
		&entry.Usage.CachedInputTokens, &entry.Usage.OutputTokens, &cachedReported,
		&entry.State, &entry.ErrorClass, &entry.CreatedAt, &entry.UpdatedAt,
		&dispatchedAt, &terminalAt,
	)
	if err != nil {
		return nil, err
	}
	entry.Usage.CachedInputReported = cachedReported == 1
	if dispatchedAt.Valid {
		at := dispatchedAt.Time.UTC()
		entry.DispatchedAt = &at
	}
	if terminalAt.Valid {
		at := terminalAt.Time.UTC()
		entry.TerminalAt = &at
	}
	return entry, nil
}

func lookupBudgetEntry(ctx context.Context, conn *sql.Conn, identity budget.DispatchIdentity) (*budget.Entry, error) {
	row := conn.QueryRowContext(ctx, `SELECT `+budgetEntryColumns+`
		FROM llm_budget_reservations
		WHERE workspace_id = ? AND operation_kind = ? AND operation_id = ?
		  AND logical_attempt = ? AND physical_ordinal = ?`,
		identity.WorkspaceID, string(identity.OperationKind), identity.OperationID,
		identity.LogicalAttempt, identity.PhysicalOrdinal)
	entry, err := scanBudgetEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, budget.ErrNotFound
	}
	return entry, err
}

// countedForDay sums the USD nanos already consumed from a daily cap. Released
// entries contribute nothing, settled entries contribute their exact cost, and
// reserved, possibly dispatched, and uncertain entries all contribute their
// full worst-case reservation.
const countedForDayExpression = `
	COALESCE(SUM(CASE
		WHEN state = 'released' THEN 0
		WHEN state IN ('reserved','possibly_dispatched') THEN reserved_usd_nanos
		ELSE settled_usd_nanos
	END), 0)`

func countedForDay(ctx context.Context, conn *sql.Conn, budgetDay, workspaceID string) (config.USDNanos, error) {
	query := `SELECT ` + countedForDayExpression + ` FROM llm_budget_reservations WHERE budget_day = ?`
	args := []any{budgetDay}
	if workspaceID != "" {
		query += ` AND workspace_id = ?`
		args = append(args, workspaceID)
	}
	var counted config.USDNanos
	if err := conn.QueryRowContext(ctx, query, args...).Scan(&counted); err != nil {
		return 0, err
	}
	return counted, nil
}

func appendTransition(ctx context.Context, conn *sql.Conn, entry *budget.Entry, event, fromState, toState, reason string, amount config.USDNanos, at time.Time) error {
	var from any
	if fromState != "" {
		from = fromState
	}
	_, err := conn.ExecContext(ctx, `
		INSERT INTO llm_budget_transitions (
			id, reservation_id, workspace_id, event, from_state, to_state,
			amount_usd_nanos, reason, policy_fingerprint, created_at
		) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		NewID(), entry.ID, entry.Identity.WorkspaceID, event, from, toState,
		int64(amount), reason, entry.PolicyFingerprint, at)
	return err
}

// Reserve atomically admits one physical call. It commits the reservation
// before any network call, so competing admissions serialize through SQLite and
// cannot exceed either cap in aggregate.
func (l *BudgetLedger) Reserve(ctx context.Context, req budget.ReserveRequest) (*budget.Entry, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if !l.limits.Configured() {
		return nil, fmt.Errorf("budget ledger requires configured global caps")
	}
	// Both physical-request caps are exact and independent of history, so they
	// are checked before taking the write lock.
	workspaceLimit, err := l.limits.CheckRequestCaps(req.Identity.WorkspaceID, req.Reserved)
	if err != nil {
		return nil, err
	}

	var entry *budget.Entry
	err = l.withImmediateTx(ctx, "reserve", func(ctx context.Context, conn *sql.Conn) error {
		// The ledger clock, observed only after BEGIN IMMEDIATE owns the UTC
		// accounting day. A queued caller cannot select a stale/future day or
		// cross midnight while waiting for the write lock and charge the wrong
		// window.
		authoritativeBudgetDay := budget.BudgetDay(l.now())

		existing, lookupErr := lookupBudgetEntry(ctx, conn, req.Identity)
		if lookupErr == nil {
			// The tuple already exists. Never admit a second physical call for
			// the same identity; the caller must return the retained result or
			// fail closed.
			entry = existing
			return fmt.Errorf("%w: %s", budget.ErrDuplicateDispatch, req.Identity)
		}
		if !errors.Is(lookupErr, budget.ErrNotFound) {
			return lookupErr
		}

		globalCounted, countErr := countedForDay(ctx, conn, authoritativeBudgetDay, "")
		if countErr != nil {
			return countErr
		}
		if capErr := budget.CheckDailyCap(budget.ScopeGlobal, req.Reserved, globalCounted, l.limits.GlobalDailyBudgetUSD); capErr != nil {
			return capErr
		}
		workspaceCounted, countErr := countedForDay(ctx, conn, authoritativeBudgetDay, req.Identity.WorkspaceID)
		if countErr != nil {
			return countErr
		}
		if capErr := budget.CheckDailyCap(budget.ScopeWorkspace, req.Reserved, workspaceCounted, workspaceLimit.DailyBudgetUSD); capErr != nil {
			return capErr
		}

		now := l.now()
		created := &budget.Entry{
			ID:                NewID(),
			Identity:          req.Identity,
			BudgetDay:         authoritativeBudgetDay,
			RouteSlot:         req.RouteSlot,
			Provider:          req.Provider,
			Model:             req.Model,
			Endpoint:          req.Endpoint,
			PolicyFingerprint: req.PolicyFingerprint,
			PriceRevision:     req.PriceRevision,
			RequestHash:       req.RequestHash,
			Rates:             req.Rates,
			Caps:              req.Caps,
			Reserved:          req.Reserved,
			State:             budget.StateReserved,
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		if _, execErr := conn.ExecContext(ctx, `
			INSERT INTO llm_budget_reservations (
				id, workspace_id, operation_kind, operation_id, logical_attempt,
				physical_ordinal, budget_day, route_slot, provider, model, endpoint,
				policy_fingerprint, price_revision, request_hash,
				input_usd_per_million, cached_input_usd_per_million, output_usd_per_million,
				max_input_tokens, max_output_tokens, reserved_usd_nanos, settled_usd_nanos,
				uncached_input_tokens, cached_input_tokens, output_tokens, cached_input_reported,
				state, error_class, created_at, updated_at, dispatched_at, terminal_at
			) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,0,0,0,0,?,'',?,?,NULL,NULL)`,
			created.ID, created.Identity.WorkspaceID, string(created.Identity.OperationKind),
			created.Identity.OperationID, created.Identity.LogicalAttempt, created.Identity.PhysicalOrdinal,
			created.BudgetDay, created.RouteSlot, created.Provider, created.Model, created.Endpoint,
			created.PolicyFingerprint, created.PriceRevision, created.RequestHash,
			int64(created.Rates.InputUSDPerMillion), int64(created.Rates.CachedInputUSDPerMillion),
			int64(created.Rates.OutputUSDPerMillion), created.Caps.MaxInputTokens, created.Caps.MaxOutputTokens,
			int64(created.Reserved), created.State, created.CreatedAt, created.UpdatedAt,
		); execErr != nil {
			return execErr
		}
		if txErr := appendTransition(ctx, conn, created, "reserve", "", budget.StateReserved, "", created.Reserved, now); txErr != nil {
			return txErr
		}
		entry = created
		return nil
	})
	if err != nil {
		return entry, err
	}
	return entry, nil
}

// MarkPossiblyDispatched persists the durable dispatch marker that must exist
// before the provider network call begins.
func (l *BudgetLedger) MarkPossiblyDispatched(ctx context.Context, identity budget.DispatchIdentity) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	return l.withImmediateTx(ctx, "mark_dispatched", func(ctx context.Context, conn *sql.Conn) error {
		entry, err := lookupBudgetEntry(ctx, conn, identity)
		if err != nil {
			return err
		}
		if entry.State == budget.StatePossiblyDispatched {
			// Idempotent: the marker is already durable.
			return nil
		}
		if entry.State != budget.StateReserved {
			return fmt.Errorf("%w: cannot mark %s entry as possibly dispatched", errLedgerConflict, entry.State)
		}
		now := l.now()
		if _, err := conn.ExecContext(ctx, `
			UPDATE llm_budget_reservations
			SET state = ?, dispatched_at = ?, updated_at = ?
			WHERE id = ? AND state = ?`,
			budget.StatePossiblyDispatched, now, now, entry.ID, budget.StateReserved); err != nil {
			return err
		}
		return appendTransition(ctx, conn, entry, "dispatch", budget.StateReserved, budget.StatePossiblyDispatched, "", entry.Reserved, now)
	})
}

// SettleTrusted records validated usage and its exact cost. The ledger
// recomputes that cost from the immutable persisted rate snapshot; a caller
// cannot under-report settlement to free budget for later admissions.
func (l *BudgetLedger) SettleTrusted(ctx context.Context, identity budget.DispatchIdentity, usage budget.Usage, cost config.USDNanos, errorClass string) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	if cost < 0 {
		return fmt.Errorf("settled cost must not be negative")
	}
	return l.withImmediateTx(ctx, "settle_trusted", func(ctx context.Context, conn *sql.Conn) error {
		entry, err := lookupBudgetEntry(ctx, conn, identity)
		if err != nil {
			return err
		}
		if entry.State != budget.StatePossiblyDispatched {
			return fmt.Errorf("%w: cannot settle a %s entry", errLedgerConflict, entry.State)
		}
		if cost > entry.Reserved {
			return fmt.Errorf("%w: settled cost %d exceeds reservation %d", errLedgerConflict, cost, entry.Reserved)
		}
		if err := budget.ValidateUsage(usage, entry.Caps, nil); err != nil {
			return fmt.Errorf("%w: %v", errLedgerConflict, err)
		}
		expectedCost, err := budget.SettledCost(entry.Rates, usage)
		if err != nil {
			return fmt.Errorf("%w: recompute settled cost: %v", errLedgerConflict, err)
		}
		if cost != expectedCost {
			return fmt.Errorf("%w: settled cost %d does not match persisted rate snapshot cost %d",
				errLedgerConflict, cost, expectedCost)
		}
		cachedReported := 0
		if usage.CachedInputReported {
			cachedReported = 1
		}
		now := l.now()
		if _, err := conn.ExecContext(ctx, `
			UPDATE llm_budget_reservations
			SET state = ?, settled_usd_nanos = ?, uncached_input_tokens = ?, cached_input_tokens = ?,
			    output_tokens = ?, cached_input_reported = ?, error_class = ?, updated_at = ?, terminal_at = ?
			WHERE id = ? AND state = ?`,
			budget.StateSettled, int64(cost), usage.UncachedInputTokens, usage.CachedInputTokens,
			usage.OutputTokens, cachedReported, errorClass, now, now, entry.ID, budget.StatePossiblyDispatched); err != nil {
			return err
		}
		return appendTransition(ctx, conn, entry, "settle", budget.StatePossiblyDispatched, budget.StateSettled, errorClass, cost, now)
	})
}

// SettleUncertain settles the full reservation for a call whose dispatch or
// usage is ambiguous. It is the conservative terminal state.
func (l *BudgetLedger) SettleUncertain(ctx context.Context, identity budget.DispatchIdentity, reason string) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	return l.withImmediateTx(ctx, "settle_uncertain", func(ctx context.Context, conn *sql.Conn) error {
		entry, err := lookupBudgetEntry(ctx, conn, identity)
		if err != nil {
			return err
		}
		if entry.State == budget.StateUsageUncertain {
			return nil
		}
		if entry.State != budget.StatePossiblyDispatched {
			return fmt.Errorf("%w: cannot mark a %s entry uncertain", errLedgerConflict, entry.State)
		}
		now := l.now()
		if _, err := conn.ExecContext(ctx, `
			UPDATE llm_budget_reservations
			SET state = ?, settled_usd_nanos = reserved_usd_nanos, error_class = ?, updated_at = ?, terminal_at = ?
			WHERE id = ? AND state = ?`,
			budget.StateUsageUncertain, reason, now, now, entry.ID, budget.StatePossiblyDispatched); err != nil {
			return err
		}
		return appendTransition(ctx, conn, entry, "settle", budget.StatePossiblyDispatched, budget.StateUsageUncertain, reason, entry.Reserved, now)
	})
}

// Release returns a reservation that is definitely pre-dispatch. It fails for
// an entry already marked possibly dispatched, because that call may have
// billed.
func (l *BudgetLedger) Release(ctx context.Context, identity budget.DispatchIdentity, reason string) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	return l.withImmediateTx(ctx, "release", func(ctx context.Context, conn *sql.Conn) error {
		entry, err := lookupBudgetEntry(ctx, conn, identity)
		if err != nil {
			return err
		}
		if entry.State == budget.StateReleased {
			return nil
		}
		if entry.State != budget.StateReserved {
			return fmt.Errorf("%w: cannot release a %s entry", errLedgerConflict, entry.State)
		}
		now := l.now()
		if _, err := conn.ExecContext(ctx, `
			UPDATE llm_budget_reservations
			SET state = ?, error_class = ?, updated_at = ?, terminal_at = ?
			WHERE id = ? AND state = ?`,
			budget.StateReleased, reason, now, now, entry.ID, budget.StateReserved); err != nil {
			return err
		}
		return appendTransition(ctx, conn, entry, "release", budget.StateReserved, budget.StateReleased, reason, 0, now)
	})
}

// Lookup returns the persisted entry for an identity, or nil when absent.
func (l *BudgetLedger) Lookup(ctx context.Context, identity budget.DispatchIdentity) (*budget.Entry, error) {
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	conn, err := l.store.db.Conn(ctx)
	if err != nil {
		return nil, unavailable("lookup", err)
	}
	defer conn.Close()
	entry, err := lookupBudgetEntry(ctx, conn, identity)
	if errors.Is(err, budget.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, unavailable("lookup", err)
	}
	return entry, nil
}

// Recover reconciles every nonterminal entry to a conservative terminal state.
// It never reconstructs or resends an incomplete dispatch: an entry that never
// crossed the network boundary is released, and one that may have billed
// settles its full reservation as uncertain.
func (l *BudgetLedger) Recover(ctx context.Context) (budget.RecoveryReport, error) {
	report := budget.RecoveryReport{}
	err := l.withImmediateTx(ctx, "recover", func(ctx context.Context, conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, `SELECT `+budgetEntryColumns+`
			FROM llm_budget_reservations
			WHERE state IN (?, ?)
			ORDER BY created_at`,
			budget.StateReserved, budget.StatePossiblyDispatched)
		if err != nil {
			return err
		}
		var pending []*budget.Entry
		for rows.Next() {
			entry, scanErr := scanBudgetEntry(rows)
			if scanErr != nil {
				rows.Close()
				return scanErr
			}
			pending = append(pending, entry)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			rows.Close()
			return rowsErr
		}
		rows.Close()

		report = budget.RecoveryReport{Inspected: len(pending)}
		now := l.now()
		for _, entry := range pending {
			switch entry.State {
			case budget.StateReserved:
				// No network boundary was crossed, so the reservation is safe
				// to release in full.
				if _, err := conn.ExecContext(ctx, `
					UPDATE llm_budget_reservations
					SET state = ?, error_class = ?, updated_at = ?, terminal_at = ?
					WHERE id = ? AND state = ?`,
					budget.StateReleased, "startup_reconciliation", now, now, entry.ID, budget.StateReserved); err != nil {
					return err
				}
				if err := appendTransition(ctx, conn, entry, "recover", budget.StateReserved, budget.StateReleased,
					"released undispatched reservation at startup", 0, now); err != nil {
					return err
				}
				report.Released++
			case budget.StatePossiblyDispatched:
				// The call may have billed, so it settles at its full
				// reservation and is marked uncertain.
				if _, err := conn.ExecContext(ctx, `
					UPDATE llm_budget_reservations
					SET state = ?, settled_usd_nanos = reserved_usd_nanos, error_class = ?, updated_at = ?, terminal_at = ?
					WHERE id = ? AND state = ?`,
					budget.StateUsageUncertain, "startup_reconciliation", now, now, entry.ID, budget.StatePossiblyDispatched); err != nil {
					return err
				}
				if err := appendTransition(ctx, conn, entry, "recover", budget.StatePossiblyDispatched, budget.StateUsageUncertain,
					"settled possibly dispatched call at full reservation", entry.Reserved, now); err != nil {
					return err
				}
				report.MarkedUncertain++
			}
		}
		return nil
	})
	if err != nil {
		return budget.RecoveryReport{}, err
	}
	return report, nil
}

// Snapshot returns the sanitized read-only ledger state for a UTC day. It never
// returns credentials, prompts, or provider response content.
func (l *BudgetLedger) Snapshot(ctx context.Context, workspaceID, budgetDay string) (budget.Snapshot, error) {
	return l.snapshotWithReadObserver(ctx, workspaceID, budgetDay, nil)
}

// snapshotWithReadObserver keeps the global and workspace reads on one SQLite
// snapshot. The observer is an internal deterministic test seam used to commit
// through another Store handle between the two reads.
func (l *BudgetLedger) snapshotWithReadObserver(
	ctx context.Context,
	workspaceID, budgetDay string,
	afterGlobalRead func(),
) (budget.Snapshot, error) {
	if err := budget.ValidateBudgetDay(budgetDay); err != nil {
		return budget.Snapshot{}, err
	}
	tx, err := l.store.db.BeginTx(ctx, nil)
	if err != nil {
		return budget.Snapshot{}, unavailable("snapshot", err)
	}
	defer tx.Rollback()

	snapshot := budget.Snapshot{BudgetDay: budgetDay}
	global, err := scopeUsage(ctx, tx, budgetDay, "", l.limits.GlobalMaxRequestUSD, l.limits.GlobalDailyBudgetUSD)
	if err != nil {
		return budget.Snapshot{}, unavailable("snapshot", err)
	}
	snapshot.Global = global
	if afterGlobalRead != nil {
		afterGlobalRead()
	}

	if workspaceID != "" {
		limit, ok := l.limits.Workspace(workspaceID)
		if !ok {
			// An unbudgeted workspace reports zero caps rather than leaking
			// another workspace's configuration.
			limit = config.WorkspaceBudget{}
		}
		workspace, err := scopeUsage(ctx, tx, budgetDay, workspaceID, limit.MaxRequestUSD, limit.DailyBudgetUSD)
		if err != nil {
			return budget.Snapshot{}, unavailable("snapshot", err)
		}
		snapshot.Workspace = &workspace
	}
	if err := tx.Commit(); err != nil {
		return budget.Snapshot{}, unavailable("snapshot", err)
	}
	return snapshot, nil
}

type budgetQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func scopeUsage(ctx context.Context, queryer budgetQueryer, budgetDay, workspaceID string, maxRequest, dailyBudget config.USDNanos) (budget.ScopeUsage, error) {
	query := `
		SELECT
			COALESCE(SUM(CASE WHEN state IN ('settled','usage_uncertain') THEN settled_usd_nanos ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state IN ('reserved','possibly_dispatched') THEN reserved_usd_nanos ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'usage_uncertain' THEN 1 ELSE 0 END), 0)
		FROM llm_budget_reservations
		WHERE budget_day = ?`
	args := []any{budgetDay}
	if workspaceID != "" {
		query += ` AND workspace_id = ?`
		args = append(args, workspaceID)
	}
	usage := budget.ScopeUsage{MaxRequestUSD: maxRequest, DailyBudgetUSD: dailyBudget}
	if err := queryer.QueryRowContext(ctx, query, args...).Scan(&usage.Settled, &usage.ActiveReserved, &usage.UncertainCalls); err != nil {
		return budget.ScopeUsage{}, err
	}
	remaining := dailyBudget - usage.Settled - usage.ActiveReserved
	if remaining < 0 {
		remaining = 0
	}
	usage.Remaining = remaining
	return usage, nil
}

// Verify at compile time that the SQLite implementation satisfies the narrow
// ledger boundary the orchestration layer depends on.
var _ budget.Ledger = (*BudgetLedger)(nil)
