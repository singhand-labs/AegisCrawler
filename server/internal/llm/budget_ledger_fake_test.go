package llm

import (
	"context"
	"fmt"
	"sync"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
)

// fakeLedger is an in-memory budget.Ledger for orchestrator tests. It records
// the exact call sequence so tests can assert that a physical call is always
// reserved and dispatch-marked before the provider is reached, and settled
// afterwards. It intentionally has no SQLite dependency.
type fakeLedger struct {
	mu sync.Mutex

	// entries is keyed by the dispatch identity string.
	entries map[string]*budget.Entry
	// events records every ledger operation in order.
	events []string

	// denyOrdinals refuses admission for the given physical ordinals.
	denyOrdinals map[int]bool
	// unavailable makes every operation report a ledger outage.
	unavailable bool
	// failSettle makes settlement fail so the reservation stays counted.
	failSettle bool
	// failMark fails only the durable dispatch marker, after reservation.
	failMark bool
}

func newFakeLedger() *fakeLedger {
	return &fakeLedger{entries: map[string]*budget.Entry{}, denyOrdinals: map[int]bool{}}
}

func (l *fakeLedger) record(event string) {
	l.events = append(l.events, event)
}

// Events returns the recorded operation sequence.
func (l *fakeLedger) Events() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

func (l *fakeLedger) entry(identity budget.DispatchIdentity) (*budget.Entry, bool) {
	entry, ok := l.entries[identity.String()]
	return entry, ok
}

func (l *fakeLedger) Reserve(ctx context.Context, req budget.ReserveRequest) (*budget.Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := req.Validate(); err != nil {
		return nil, err
	}
	l.record(fmt.Sprintf("reserve:%d", req.Identity.PhysicalOrdinal))
	if l.unavailable {
		return nil, fmt.Errorf("fake ledger: %w", budget.ErrLedgerUnavailable)
	}
	if l.denyOrdinals[req.Identity.PhysicalOrdinal] {
		return nil, &budget.DeniedError{
			Scope:     budget.ScopeWorkspace,
			Limit:     budget.LimitDaily,
			Requested: req.Reserved,
			Remaining: 0,
		}
	}
	if existing, ok := l.entry(req.Identity); ok {
		return existing, fmt.Errorf("%w: %s", budget.ErrDuplicateDispatch, req.Identity)
	}
	entry := &budget.Entry{
		ID:                "entry-" + req.Identity.String(),
		Identity:          req.Identity,
		BudgetDay:         req.BudgetDay,
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
	}
	l.entries[req.Identity.String()] = entry
	return entry, nil
}

func (l *fakeLedger) MarkPossiblyDispatched(ctx context.Context, identity budget.DispatchIdentity) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.record(fmt.Sprintf("dispatch:%d", identity.PhysicalOrdinal))
	if l.unavailable {
		return fmt.Errorf("fake ledger: %w", budget.ErrLedgerUnavailable)
	}
	if l.failMark {
		return fmt.Errorf("fake dispatch marker: %w", budget.ErrLedgerUnavailable)
	}
	entry, ok := l.entry(identity)
	if !ok {
		return budget.ErrNotFound
	}
	entry.State = budget.StatePossiblyDispatched
	return nil
}

func (l *fakeLedger) SettleTrusted(ctx context.Context, identity budget.DispatchIdentity, usage budget.Usage, cost config.USDNanos, errorClass string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.record(fmt.Sprintf("settle_trusted:%d:%d", identity.PhysicalOrdinal, cost))
	if l.failSettle {
		return fmt.Errorf("fake ledger settlement failure")
	}
	entry, ok := l.entry(identity)
	if !ok {
		return budget.ErrNotFound
	}
	if entry.State != budget.StatePossiblyDispatched {
		return fmt.Errorf("cannot settle a %s entry", entry.State)
	}
	entry.State = budget.StateSettled
	entry.Settled = cost
	entry.Usage = usage
	return nil
}

func (l *fakeLedger) SettleUncertain(ctx context.Context, identity budget.DispatchIdentity, reason string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.record(fmt.Sprintf("settle_uncertain:%d:%s", identity.PhysicalOrdinal, reason))
	if l.failSettle {
		return fmt.Errorf("fake ledger settlement failure")
	}
	entry, ok := l.entry(identity)
	if !ok {
		return budget.ErrNotFound
	}
	if entry.State != budget.StatePossiblyDispatched {
		return fmt.Errorf("cannot mark a %s entry uncertain", entry.State)
	}
	entry.State = budget.StateUsageUncertain
	entry.Settled = entry.Reserved
	entry.ErrorClass = reason
	return nil
}

func (l *fakeLedger) Release(ctx context.Context, identity budget.DispatchIdentity, reason string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.record(fmt.Sprintf("release:%d", identity.PhysicalOrdinal))
	entry, ok := l.entry(identity)
	if !ok {
		return budget.ErrNotFound
	}
	if entry.State != budget.StateReserved {
		return fmt.Errorf("cannot release a %s entry", entry.State)
	}
	entry.State = budget.StateReleased
	return nil
}

func (l *fakeLedger) Lookup(ctx context.Context, identity budget.DispatchIdentity) (*budget.Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entry(identity)
	if !ok {
		return nil, nil
	}
	return entry, nil
}

func (l *fakeLedger) Recover(ctx context.Context) (budget.RecoveryReport, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.record("recover")
	return budget.RecoveryReport{}, nil
}

func (l *fakeLedger) Snapshot(ctx context.Context, workspaceID, budgetDay string) (budget.Snapshot, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	const dailyCap config.USDNanos = 10_000_000_000
	snapshot := budget.Snapshot{
		BudgetDay: budgetDay,
		Global: budget.ScopeUsage{
			DailyBudgetUSD: dailyCap,
			Remaining:      dailyCap,
		},
	}
	for _, entry := range l.entries {
		if entry.BudgetDay != budgetDay {
			continue
		}
		switch entry.State {
		case budget.StateReserved, budget.StatePossiblyDispatched:
			snapshot.Global.ActiveReserved += entry.Reserved
		case budget.StateSettled, budget.StateUsageUncertain:
			snapshot.Global.Settled += entry.Settled
		}
		if entry.State == budget.StateUsageUncertain {
			snapshot.Global.UncertainCalls++
		}
	}
	counted := snapshot.Global.Settled + snapshot.Global.ActiveReserved
	if counted >= dailyCap {
		snapshot.Global.Remaining = 0
	} else {
		snapshot.Global.Remaining = dailyCap - counted
	}
	return snapshot, nil
}

var _ budget.Ledger = (*fakeLedger)(nil)

// enforcedDispatchContext binds a valid authenticated dispatch identity so an
// enforced completion can be admitted.
func enforcedDispatchContext(operationID string) context.Context {
	return WithDispatchOperation(context.Background(), DispatchOperation{
		Kind:           budget.OperationEnhance,
		ID:             operationID,
		LogicalAttempt: 1,
		WorkspaceID:    "default",
	})
}
