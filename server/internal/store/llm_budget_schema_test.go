package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/models"
)

func migration026TableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatalf("read %s columns: %v", table, err)
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatalf("scan %s column: %v", table, err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s columns: %v", table, err)
	}
	if len(columns) == 0 {
		t.Fatalf("table %s has no columns", table)
	}
	return columns
}

func migration026RowSnapshot(t *testing.T, db *sql.DB, table, id string, columns []string) []any {
	t.Helper()
	quotedColumns := make([]string, len(columns))
	for i, column := range columns {
		quotedColumns[i] = `"` + strings.ReplaceAll(column, `"`, `""`) + `"`
	}
	query := fmt.Sprintf(
		`SELECT %s FROM "%s" WHERE id = ?`,
		strings.Join(quotedColumns, ", "),
		strings.ReplaceAll(table, `"`, `""`),
	)
	values := make([]any, len(columns))
	destinations := make([]any, len(columns))
	for i := range values {
		destinations[i] = &values[i]
	}
	if err := db.QueryRow(query, id).Scan(destinations...); err != nil {
		t.Fatalf("snapshot %s row %s: %v", table, id, err)
	}
	for i, value := range values {
		if bytes, ok := value.([]byte); ok {
			values[i] = append([]byte(nil), bytes...)
		}
	}
	return values
}

// budgetRow mirrors one llm_budget_reservations row so schema tests can assert
// constraint and trigger behavior directly, independent of the ledger API.
type budgetRow struct {
	ID                  string
	WorkspaceID         string
	OperationKind       string
	OperationID         string
	LogicalAttempt      int
	PhysicalOrdinal     int
	BudgetDay           string
	RouteSlot           string
	Provider            string
	Model               string
	Endpoint            string
	PolicyFingerprint   string
	PriceRevision       string
	RequestHash         string
	InputRate           int64
	CachedInputRate     int64
	OutputRate          int64
	MaxInputTokens      int
	MaxOutputTokens     int
	Reserved            int64
	Settled             int64
	UncachedInputTokens int
	CachedInputTokens   int
	OutputTokens        int
	CachedInputReported int
	State               string
	ErrorClass          string
	DispatchedAt        *time.Time
	TerminalAt          *time.Time
}

func validBudgetRow() budgetRow {
	return budgetRow{
		ID:                "reservation-1",
		WorkspaceID:       "default",
		OperationKind:     "dsl",
		OperationID:       "job-1",
		LogicalAttempt:    1,
		PhysicalOrdinal:   0,
		BudgetDay:         "2026-07-30",
		RouteSlot:         "primary",
		Provider:          "aliyun",
		Model:             "qwen-max",
		Endpoint:          "https://example.invalid/v1",
		PolicyFingerprint: "fingerprint",
		PriceRevision:     "2026-07-01-contract-v3",
		RequestHash:       "hash",
		InputRate:         600_000_000,
		CachedInputRate:   150_000_000,
		OutputRate:        2_000_000_000,
		MaxInputTokens:    100_000,
		MaxOutputTokens:   4_000,
		Reserved:          68_000_000,
		State:             "reserved",
	}
}

func insertBudgetRow(t *testing.T, db *sql.DB, row budgetRow) error {
	t.Helper()
	now := time.Now().UTC()
	_, err := db.Exec(`
		INSERT INTO llm_budget_reservations (
			id, workspace_id, operation_kind, operation_id, logical_attempt,
			physical_ordinal, budget_day, route_slot, provider, model, endpoint,
			policy_fingerprint, price_revision, request_hash,
			input_usd_per_million, cached_input_usd_per_million, output_usd_per_million,
			max_input_tokens, max_output_tokens, reserved_usd_nanos, settled_usd_nanos,
			uncached_input_tokens, cached_input_tokens, output_tokens, cached_input_reported,
			state, error_class, created_at, updated_at, dispatched_at, terminal_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		row.ID, row.WorkspaceID, row.OperationKind, row.OperationID, row.LogicalAttempt,
		row.PhysicalOrdinal, row.BudgetDay, row.RouteSlot, row.Provider, row.Model, row.Endpoint,
		row.PolicyFingerprint, row.PriceRevision, row.RequestHash,
		row.InputRate, row.CachedInputRate, row.OutputRate,
		row.MaxInputTokens, row.MaxOutputTokens, row.Reserved, row.Settled,
		row.UncachedInputTokens, row.CachedInputTokens, row.OutputTokens, row.CachedInputReported,
		row.State, row.ErrorClass, now, now, row.DispatchedAt, row.TerminalAt)
	return err
}

func mustInsertBudgetRow(t *testing.T, db *sql.DB, row budgetRow) {
	t.Helper()
	if err := insertBudgetRow(t, db, row); err != nil {
		t.Fatalf("insert budget row: %v", err)
	}
}

func TestMigration026CreatesLedgerTablesAndIndexes(t *testing.T) {
	s := newTestStore(t)

	for _, table := range []string{"llm_budget_reservations", "llm_budget_transitions"} {
		var name string
		if err := s.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
		).Scan(&name); err != nil {
			t.Fatalf("expected table %s: %v", table, err)
		}
	}

	wantIndexes := []string{
		"idx_llm_provider_calls_dispatch",
		"idx_llm_budget_reservations_identity",
		"idx_llm_budget_reservations_global_day",
		"idx_llm_budget_reservations_workspace_day",
		"idx_llm_budget_reservations_state",
		"idx_llm_budget_reservations_operation",
		"idx_llm_budget_transitions_reservation",
		"idx_llm_budget_transitions_workspace",
	}
	for _, index := range wantIndexes {
		var name string
		if err := s.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, index,
		).Scan(&name); err != nil {
			t.Fatalf("expected index %s: %v", index, err)
		}
	}

	for _, column := range []string{
		"operation_kind",
		"operation_id",
		"logical_attempt",
		"physical_ordinal",
		"route_slot",
		"policy_fingerprint",
		"price_revision",
	} {
		var name string
		if err := s.db.QueryRow(
			`SELECT name FROM pragma_table_info('llm_provider_calls') WHERE name = ?`, column,
		).Scan(&name); err != nil {
			t.Fatalf("expected llm_provider_calls column %s: %v", column, err)
		}
	}

	var lineageTrigger string
	if err := s.db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type = 'trigger' AND name = 'trg_llm_provider_calls_dispatch_lineage'
	`).Scan(&lineageTrigger); err != nil {
		t.Fatalf("expected provider-call dispatch-lineage trigger: %v", err)
	}

	var providerCallUnique, providerCallPartial int
	if err := s.db.QueryRow(`
		SELECT "unique", partial
		FROM pragma_index_list('llm_provider_calls')
		WHERE name = 'idx_llm_provider_calls_dispatch'
	`).Scan(&providerCallUnique, &providerCallPartial); err != nil {
		t.Fatalf("read provider-call dispatch index: %v", err)
	}
	if providerCallUnique != 1 || providerCallPartial != 1 {
		t.Fatalf("provider-call dispatch index unique/partial = %d/%d, want 1/1",
			providerCallUnique, providerCallPartial)
	}

	var unique int
	if err := s.db.QueryRow(
		`SELECT "unique" FROM pragma_index_list('llm_budget_reservations') WHERE name = 'idx_llm_budget_reservations_identity'`,
	).Scan(&unique); err != nil {
		t.Fatalf("read identity index: %v", err)
	}
	if unique != 1 {
		t.Fatal("the dispatch identity index must be unique")
	}
}

func TestMigration026ProviderCallDispatchIdentityIsUniqueButLegacyCallsRemainAppendable(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("migration", "default")
	job := createClaimedRequirementArtifactJob(t, s, ctx, "migration-026-provider-identity", 1)

	lineaged, lineagedArtifact := providerCallForAttempt(
		models.LLMJobTypeRequirement, job.ID, job.AttemptCount, 1,
		"migration-026-lineaged-provider-call",
	)
	lineaged.Dispatch = &models.LLMDispatchLineage{
		OperationKind:     string(models.LLMJobTypeRequirement),
		OperationID:       job.ID + ":analysis:0",
		LogicalAttempt:    job.AttemptCount,
		PhysicalOrdinal:   0,
		RouteSlot:         "primary",
		PolicyFingerprint: "migration-026-policy",
		PriceRevision:     "migration-026-price",
	}
	reservation := validBudgetRow()
	now := time.Now().UTC()
	reservation.ID = "migration-026-provider-identity-reservation"
	reservation.OperationKind = lineaged.Dispatch.OperationKind
	reservation.OperationID = lineaged.Dispatch.OperationID
	reservation.LogicalAttempt = lineaged.Dispatch.LogicalAttempt
	reservation.PhysicalOrdinal = lineaged.Dispatch.PhysicalOrdinal
	reservation.RouteSlot = lineaged.Dispatch.RouteSlot
	reservation.Provider = lineaged.Provider
	reservation.Model = lineaged.Model
	reservation.PolicyFingerprint = lineaged.Dispatch.PolicyFingerprint
	reservation.PriceRevision = lineaged.Dispatch.PriceRevision
	reservation.RequestHash = lineaged.RequestHash
	reservation.State = "possibly_dispatched"
	reservation.DispatchedAt = &now
	mustInsertBudgetRow(t, s.db, reservation)

	if err := s.CreateLLMProviderCall(ctx, lineaged, lineagedArtifact); err != nil {
		t.Fatalf("insert first lineaged provider call: %v", err)
	}
	duplicate, duplicateArtifact := providerCallForAttempt(
		models.LLMJobTypeRequirement, job.ID, job.AttemptCount, 2,
		"migration-026-duplicate-lineaged-provider-call",
	)
	duplicate.RequestHash = lineaged.RequestHash
	duplicateDispatch := *lineaged.Dispatch
	duplicate.Dispatch = &duplicateDispatch
	if err := s.CreateLLMProviderCall(ctx, duplicate, duplicateArtifact); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "unique") {
		t.Fatalf("duplicate lineaged provider call was not rejected by unique identity: %v", err)
	}

	for callIndex, marker := range []string{
		"migration-026-legacy-provider-call-1",
		"migration-026-legacy-provider-call-2",
	} {
		legacyCall, legacyArtifact := providerCallForAttempt(
			models.LLMJobTypeRequirement, job.ID, job.AttemptCount, callIndex+2, marker,
		)
		if err := s.CreateLLMProviderCall(ctx, legacyCall, legacyArtifact); err != nil {
			t.Fatalf("insert legacy NULL-lineage provider call %d: %v", callIndex+1, err)
		}
	}

	var lineagedCount, legacyCount int
	if err := s.db.QueryRow(`
		SELECT
			COUNT(*) FILTER (WHERE operation_kind IS NOT NULL),
			COUNT(*) FILTER (
				WHERE operation_kind IS NULL AND operation_id IS NULL
				  AND logical_attempt IS NULL AND physical_ordinal IS NULL
				  AND route_slot IS NULL AND policy_fingerprint IS NULL
				  AND price_revision IS NULL
			)
		FROM llm_provider_calls
		WHERE workspace_id = 'default' AND job_type = ? AND job_id = ?
		  AND attempt_number = ?
	`, models.LLMJobTypeRequirement, job.ID, job.AttemptCount).Scan(
		&lineagedCount, &legacyCount,
	); err != nil {
		t.Fatalf("count provider-call lineage rows: %v", err)
	}
	if lineagedCount != 1 || legacyCount != 2 {
		t.Fatalf("provider-call lineage counts = lineaged:%d legacy:%d, want 1/2",
			lineagedCount, legacyCount)
	}
}

func TestMigration026UpgradesPopulatedV25WithoutRewritingHistory(t *testing.T) {
	file, err := os.CreateTemp("", "aegis-migration-026-*.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(file.Name()) })

	rawDB, err := sql.Open("sqlite", file.Name()+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer rawDB.Close()
	rawDB.SetMaxOpenConns(1)

	if _, err := rawDB.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		t.Fatalf("create migration registry: %v", err)
	}
	for _, item := range migrations {
		if item.version > 25 {
			break
		}
		tx, err := rawDB.Begin()
		if err != nil {
			t.Fatalf("begin migration %d: %v", item.version, err)
		}
		if err := item.up(tx); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply migration %d: %v", item.version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, item.version); err != nil {
			_ = tx.Rollback()
			t.Fatalf("record migration %d: %v", item.version, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit migration %d: %v", item.version, err)
		}
	}

	legacy := &Store{
		db: rawDB, encryptionKey: "recording-encryption-key-for-tests",
	}
	ctx := workspaceContext("migration", "default")
	historyJob := createClaimedRequirementArtifactJob(t, legacy, ctx, "migration-026-history", 2)
	historyCall, historyCallArtifact := providerCallForAttempt(
		models.LLMJobTypeRequirement, historyJob.ID, historyJob.AttemptCount, 1,
		"migration-026-history-response",
	)
	historyCall.ID = "migration-026-history-call"
	if err := legacy.CreateLLMProviderCall(ctx, historyCall, historyCallArtifact); err != nil {
		t.Fatalf("insert v25 provider call: %v", err)
	}
	historyReport, historyReportArtifact := attemptReportForJob(
		models.LLMJobTypeRequirement, historyJob.ID, historyJob.AttemptCount,
	)
	historyReport.ID = "migration-026-history-report"
	if err := legacy.CreateLLMAttemptReport(ctx, historyReport, historyReportArtifact); err != nil {
		t.Fatalf("insert v25 attempt report: %v", err)
	}
	postUpgradeJob := createClaimedRequirementArtifactJob(t, legacy, ctx, "migration-026-post", 2)

	callColumns := migration026TableColumns(t, rawDB, "llm_provider_calls")
	reportColumns := migration026TableColumns(t, rawDB, "llm_attempt_reports")
	beforeCall := migration026RowSnapshot(
		t, rawDB, "llm_provider_calls", historyCall.ID, callColumns,
	)
	beforeReport := migration026RowSnapshot(
		t, rawDB, "llm_attempt_reports", historyReport.ID, reportColumns,
	)

	if err := migrate(rawDB); err != nil {
		t.Fatalf("upgrade populated v25 database: %v", err)
	}
	var currentVersion int
	if err := rawDB.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&currentVersion); err != nil {
		t.Fatalf("read migrated version: %v", err)
	}
	if currentVersion != 31 {
		t.Fatalf("schema version = %d, want 31", currentVersion)
	}
	var providerCallUnique, providerCallPartial int
	if err := rawDB.QueryRow(`
		SELECT "unique", partial
		FROM pragma_index_list('llm_provider_calls')
		WHERE name = 'idx_llm_provider_calls_dispatch'
	`).Scan(&providerCallUnique, &providerCallPartial); err != nil {
		t.Fatalf("read upgraded provider-call dispatch index: %v", err)
	}
	if providerCallUnique != 1 || providerCallPartial != 1 {
		t.Fatalf("upgraded provider-call dispatch index unique/partial = %d/%d, want 1/1",
			providerCallUnique, providerCallPartial)
	}

	afterCall := migration026RowSnapshot(
		t, rawDB, "llm_provider_calls", historyCall.ID, callColumns,
	)
	afterReport := migration026RowSnapshot(
		t, rawDB, "llm_attempt_reports", historyReport.ID, reportColumns,
	)
	if !reflect.DeepEqual(beforeCall, afterCall) {
		t.Fatalf("migration rewrote legacy provider call:\nbefore=%#v\nafter=%#v", beforeCall, afterCall)
	}
	if !reflect.DeepEqual(beforeReport, afterReport) {
		t.Fatalf("migration rewrote legacy attempt report:\nbefore=%#v\nafter=%#v", beforeReport, afterReport)
	}

	var operationKind, operationID, routeSlot, policyFingerprint, priceRevision sql.NullString
	var logicalAttempt, physicalOrdinal sql.NullInt64
	if err := rawDB.QueryRow(`
		SELECT operation_kind, operation_id, logical_attempt, physical_ordinal,
		       route_slot, policy_fingerprint, price_revision
		FROM llm_provider_calls WHERE id = ?
	`, historyCall.ID).Scan(
		&operationKind, &operationID, &logicalAttempt, &physicalOrdinal,
		&routeSlot, &policyFingerprint, &priceRevision,
	); err != nil {
		t.Fatalf("read migrated provider lineage: %v", err)
	}
	if operationKind.Valid || operationID.Valid || logicalAttempt.Valid || physicalOrdinal.Valid ||
		routeSlot.Valid || policyFingerprint.Valid || priceRevision.Valid {
		t.Fatalf("legacy provider call acquired invented lineage: kind=%+v id=%+v attempt=%+v ordinal=%+v slot=%+v policy=%+v price=%+v",
			operationKind, operationID, logicalAttempt, physicalOrdinal,
			routeSlot, policyFingerprint, priceRevision)
	}
	var reportPolicy sql.NullString
	if err := rawDB.QueryRow(`
		SELECT policy_fingerprint FROM llm_attempt_reports WHERE id = ?
	`, historyReport.ID).Scan(&reportPolicy); err != nil {
		t.Fatalf("read migrated report lineage: %v", err)
	}
	if reportPolicy.Valid {
		t.Fatalf("legacy attempt report acquired invented policy lineage: %+v", reportPolicy)
	}

	postCall, postCallArtifact := providerCallForAttempt(
		models.LLMJobTypeRequirement, postUpgradeJob.ID, postUpgradeJob.AttemptCount, 1,
		"migration-026-post-response",
	)
	postCall.ID = "migration-026-post-call"
	if err := legacy.CreateLLMProviderCall(ctx, postCall, postCallArtifact); err != nil {
		t.Fatalf("post-upgrade legacy provider insert failed: %v", err)
	}
	secondPostCall, secondPostCallArtifact := providerCallForAttempt(
		models.LLMJobTypeRequirement, postUpgradeJob.ID, postUpgradeJob.AttemptCount, 2,
		"migration-026-second-post-response",
	)
	secondPostCall.ID = "migration-026-second-post-call"
	if err := legacy.CreateLLMProviderCall(ctx, secondPostCall, secondPostCallArtifact); err != nil {
		t.Fatalf("second post-upgrade legacy provider insert failed: %v", err)
	}
	postReport, postReportArtifact := attemptReportForJob(
		models.LLMJobTypeRequirement, postUpgradeJob.ID, postUpgradeJob.AttemptCount,
	)
	postReport.ID = "migration-026-post-report"
	if err := legacy.CreateLLMAttemptReport(ctx, postReport, postReportArtifact); err != nil {
		t.Fatalf("post-upgrade legacy report insert failed: %v", err)
	}
	var postLegacyNulls int
	if err := rawDB.QueryRow(`
		SELECT COUNT(*)
		FROM llm_provider_calls
		WHERE job_id = ?
		  AND operation_kind IS NULL AND operation_id IS NULL
		  AND logical_attempt IS NULL AND physical_ordinal IS NULL
		  AND route_slot IS NULL AND policy_fingerprint IS NULL
		  AND price_revision IS NULL
	`, postUpgradeJob.ID).Scan(&postLegacyNulls); err != nil {
		t.Fatalf("read post-upgrade provider lineage: %v", err)
	}
	if postLegacyNulls != 2 {
		t.Fatalf("post-upgrade legacy provider insert count with NULL lineage = %d, want 2",
			postLegacyNulls)
	}
	if err := rawDB.QueryRow(`
		SELECT policy_fingerprint IS NULL
		FROM llm_attempt_reports WHERE id = ?
	`, postReport.ID).Scan(&postLegacyNulls); err != nil {
		t.Fatalf("read post-upgrade report lineage: %v", err)
	}
	if postLegacyNulls != 1 {
		t.Fatal("post-upgrade legacy report insert did not retain NULL policy lineage")
	}

	for _, object := range []struct {
		kind string
		name string
	}{
		{kind: "table", name: "llm_budget_reservations"},
		{kind: "table", name: "llm_budget_transitions"},
		{kind: "index", name: "idx_llm_provider_calls_dispatch"},
		{kind: "index", name: "idx_llm_budget_reservations_identity"},
		{kind: "index", name: "idx_llm_budget_reservations_global_day"},
		{kind: "index", name: "idx_llm_budget_reservations_workspace_day"},
		{kind: "index", name: "idx_llm_budget_reservations_state"},
		{kind: "index", name: "idx_llm_budget_reservations_operation"},
		{kind: "index", name: "idx_llm_budget_transitions_reservation"},
		{kind: "index", name: "idx_llm_budget_transitions_workspace"},
		{kind: "trigger", name: "trg_llm_provider_calls_dispatch_lineage"},
		{kind: "trigger", name: "trg_llm_attempt_reports_policy_lineage"},
		{kind: "trigger", name: "trg_llm_provider_calls_budget_join"},
		{kind: "trigger", name: "trg_llm_budget_reservations_state_transition"},
		{kind: "trigger", name: "trg_llm_budget_reservations_terminal_immutable"},
		{kind: "trigger", name: "trg_llm_budget_reservations_immutable_identity"},
		{kind: "trigger", name: "trg_llm_budget_reservations_dispatch_marker_immutable"},
		{kind: "trigger", name: "trg_llm_budget_reservations_no_delete"},
		{kind: "trigger", name: "trg_llm_budget_transitions_reservation_insert"},
		{kind: "trigger", name: "trg_llm_budget_transitions_no_update"},
		{kind: "trigger", name: "trg_llm_budget_transitions_no_delete"},
	} {
		var name string
		if err := rawDB.QueryRow(`
			SELECT name FROM sqlite_master WHERE type = ? AND name = ?
		`, object.kind, object.name).Scan(&name); err != nil {
			t.Fatalf("missing migrated %s %s: %v", object.kind, object.name, err)
		}
	}

	rows, err := rawDB.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("run foreign_key_check: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent string
		var rowID int64
		var foreignKeyID int
		if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			t.Fatalf("scan foreign-key violation: %v", err)
		}
		t.Fatalf("migration left foreign-key violation: table=%s row=%d parent=%s fk=%d",
			table, rowID, parent, foreignKeyID)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate foreign_key_check: %v", err)
	}
}

func TestMigration026EnforcesUniqueDispatchIdentity(t *testing.T) {
	s := newTestStore(t)
	mustInsertBudgetRow(t, s.db, validBudgetRow())

	duplicate := validBudgetRow()
	duplicate.ID = "reservation-2"
	err := insertBudgetRow(t, s.db, duplicate)
	if err == nil {
		t.Fatal("expected a duplicate dispatch identity to be rejected")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unique") {
		t.Fatalf("expected a uniqueness violation, got %v", err)
	}
}

func TestMigration026AllowsDistinctOrdinalsInOneAttempt(t *testing.T) {
	s := newTestStore(t)
	mustInsertBudgetRow(t, s.db, validBudgetRow())

	fallback := validBudgetRow()
	fallback.ID = "reservation-2"
	fallback.PhysicalOrdinal = 1
	fallback.RouteSlot = "fallback"
	fallback.Provider = "deepseek"
	mustInsertBudgetRow(t, s.db, fallback)
}

func TestMigration026RejectsInvalidReservations(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*budgetRow)
	}{
		{"third physical call", func(r *budgetRow) { r.PhysicalOrdinal = 2 }},
		{"zero logical attempt", func(r *budgetRow) { r.LogicalAttempt = 0 }},
		{"unknown route slot", func(r *budgetRow) { r.RouteSlot = "tertiary" }},
		{"unknown state", func(r *budgetRow) { r.State = "pending" }},
		{"malformed budget day", func(r *budgetRow) { r.BudgetDay = "2026-7-30" }},
		{"non positive reservation", func(r *budgetRow) { r.Reserved = 0 }},
		{"negative reservation", func(r *budgetRow) { r.Reserved = -1 }},
		{"negative settled", func(r *budgetRow) { r.Settled = -1 }},
		{"negative rate", func(r *budgetRow) { r.InputRate = -1 }},
		{"zero input cap", func(r *budgetRow) { r.MaxInputTokens = 0 }},
		{"zero output cap", func(r *budgetRow) { r.MaxOutputTokens = 0 }},
		{"empty provider", func(r *budgetRow) { r.Provider = "" }},
		{"empty model", func(r *budgetRow) { r.Model = "" }},
		{"empty fingerprint", func(r *budgetRow) { r.PolicyFingerprint = "" }},
		{"empty price revision", func(r *budgetRow) { r.PriceRevision = "" }},
		{"empty request hash", func(r *budgetRow) { r.RequestHash = "" }},
		{"primary slot with fallback ordinal", func(r *budgetRow) { r.PhysicalOrdinal = 1 }},
		{"fallback slot with primary ordinal", func(r *budgetRow) { r.RouteSlot = "fallback" }},
		{"reserved with settled amount", func(r *budgetRow) { r.Settled = 1 }},
		{"reserved with dispatch marker", func(r *budgetRow) {
			now := time.Now().UTC()
			r.DispatchedAt = &now
		}},
		{"reserved with terminal timestamp", func(r *budgetRow) {
			now := time.Now().UTC()
			r.TerminalAt = &now
		}},
		{"unknown workspace", func(r *budgetRow) { r.WorkspaceID = "missing-workspace" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			row := validBudgetRow()
			tc.mutate(&row)
			if err := insertBudgetRow(t, s.db, row); err == nil {
				t.Fatal("expected the invalid reservation to be rejected")
			}
		})
	}
}

func TestMigration026RequiresDispatchMarkerForBilledStates(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name  string
		state string
	}{
		{"possibly dispatched", "possibly_dispatched"},
		{"settled", "settled"},
		{"usage uncertain", "usage_uncertain"},
	}
	for _, tc := range cases {
		t.Run(tc.name+" without marker is rejected", func(t *testing.T) {
			s := newTestStore(t)
			row := validBudgetRow()
			row.State = tc.state
			if tc.state == "usage_uncertain" {
				row.Settled = row.Reserved
			}
			if tc.state != "possibly_dispatched" {
				row.TerminalAt = &now
			}
			if err := insertBudgetRow(t, s.db, row); err == nil {
				t.Fatalf("expected %s without a dispatch marker to be rejected", tc.state)
			}
		})
	}
}

func TestMigration026RequiresUncertainToSettleFullReservation(t *testing.T) {
	now := time.Now().UTC()
	s := newTestStore(t)
	row := validBudgetRow()
	row.State = "usage_uncertain"
	row.DispatchedAt = &now
	row.TerminalAt = &now
	row.Settled = row.Reserved - 1
	if err := insertBudgetRow(t, s.db, row); err == nil {
		t.Fatal("expected an uncertain entry settling less than its reservation to be rejected")
	}

	row.Settled = row.Reserved
	mustInsertBudgetRow(t, s.db, row)
}

func TestMigration026RejectsSettlementAboveReservation(t *testing.T) {
	now := time.Now().UTC()
	s := newTestStore(t)
	row := validBudgetRow()
	row.State = "settled"
	row.DispatchedAt = &now
	row.TerminalAt = &now
	row.Settled = row.Reserved + 1
	if err := insertBudgetRow(t, s.db, row); err == nil {
		t.Fatal("expected a settlement above the reservation to be rejected")
	}
}

func TestMigration026RejectsTrustedUsageBeyondPreparedBounds(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name   string
		mutate func(*budgetRow)
	}{
		{"input above cap", func(r *budgetRow) { r.UncachedInputTokens = r.MaxInputTokens + 1 }},
		{"input split above cap", func(r *budgetRow) {
			r.UncachedInputTokens = r.MaxInputTokens
			r.CachedInputTokens = 1
			r.CachedInputReported = 1
		}},
		{"output above cap", func(r *budgetRow) { r.OutputTokens = r.MaxOutputTokens + 1 }},
		{"cached tokens without category", func(r *budgetRow) {
			r.CachedInputTokens = 10
			r.CachedInputReported = 0
		}},
		{"negative usage", func(r *budgetRow) { r.OutputTokens = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			row := validBudgetRow()
			row.State = "settled"
			row.DispatchedAt = &now
			row.TerminalAt = &now
			row.Settled = 1_000
			tc.mutate(&row)
			if err := insertBudgetRow(t, s.db, row); err == nil {
				t.Fatal("expected out-of-bound trusted usage to be rejected")
			}
		})
	}
}

func TestMigration026RejectsTrustedUsageOnNonSettledStates(t *testing.T) {
	s := newTestStore(t)
	row := validBudgetRow()
	row.UncachedInputTokens = 10
	if err := insertBudgetRow(t, s.db, row); err == nil {
		t.Fatal("expected trusted usage on a reserved entry to be rejected")
	}
}

func TestMigration026EnforcesStateMachine(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name    string
		from    string
		to      string
		allowed bool
	}{
		{"reserved to possibly dispatched", "reserved", "possibly_dispatched", true},
		{"reserved to released", "reserved", "released", true},
		{"reserved to settled", "reserved", "settled", false},
		{"reserved to usage uncertain", "reserved", "usage_uncertain", false},
		{"possibly dispatched to settled", "possibly_dispatched", "settled", true},
		{"possibly dispatched to usage uncertain", "possibly_dispatched", "usage_uncertain", true},
		{"possibly dispatched to released", "possibly_dispatched", "released", false},
		{"possibly dispatched to reserved", "possibly_dispatched", "reserved", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			row := validBudgetRow()
			row.State = tc.from
			if tc.from == "possibly_dispatched" {
				row.DispatchedAt = &now
			}
			mustInsertBudgetRow(t, s.db, row)

			// Satisfy the row-level invariants for the destination state so the
			// test isolates the state-transition trigger itself.
			settled := int64(0)
			var dispatched, terminal *time.Time
			dispatched = row.DispatchedAt
			switch tc.to {
			case "possibly_dispatched":
				dispatched = &now
			case "settled":
				settled = 1_000
				terminal = &now
				if dispatched == nil {
					dispatched = &now
				}
			case "usage_uncertain":
				settled = row.Reserved
				terminal = &now
				if dispatched == nil {
					dispatched = &now
				}
			case "released":
				terminal = &now
			}

			_, err := s.db.Exec(`
				UPDATE llm_budget_reservations
				SET state = ?, settled_usd_nanos = ?, dispatched_at = ?, terminal_at = ?, updated_at = ?
				WHERE id = ?`,
				tc.to, settled, dispatched, terminal, now, row.ID)
			if tc.allowed && err != nil {
				t.Fatalf("expected %s -> %s to be allowed, got %v", tc.from, tc.to, err)
			}
			if !tc.allowed && err == nil {
				t.Fatalf("expected %s -> %s to be rejected", tc.from, tc.to)
			}
		})
	}
}

func TestMigration026MakesTerminalEntriesImmutable(t *testing.T) {
	now := time.Now().UTC()
	for _, state := range []string{"released", "settled", "usage_uncertain"} {
		t.Run(state, func(t *testing.T) {
			s := newTestStore(t)
			row := validBudgetRow()
			row.State = state
			row.TerminalAt = &now
			switch state {
			case "settled":
				row.DispatchedAt = &now
				row.Settled = 1_000
			case "usage_uncertain":
				row.DispatchedAt = &now
				row.Settled = row.Reserved
			}
			mustInsertBudgetRow(t, s.db, row)

			if _, err := s.db.Exec(
				`UPDATE llm_budget_reservations SET error_class = 'changed' WHERE id = ?`, row.ID,
			); err == nil {
				t.Fatalf("expected a terminal %s entry to be immutable", state)
			}
		})
	}
}

func TestMigration026MakesIdentityAndPriceSnapshotImmutable(t *testing.T) {
	cases := []struct {
		name   string
		update string
		arg    any
	}{
		{"operation kind", `UPDATE llm_budget_reservations SET operation_kind = ? WHERE id = 'reservation-1'`, "requirement"},
		{"operation id", `UPDATE llm_budget_reservations SET operation_id = ? WHERE id = 'reservation-1'`, "job-2"},
		{"logical attempt", `UPDATE llm_budget_reservations SET logical_attempt = ? WHERE id = 'reservation-1'`, 2},
		{"physical ordinal", `UPDATE llm_budget_reservations SET physical_ordinal = ? WHERE id = 'reservation-1'`, 1},
		{"budget day", `UPDATE llm_budget_reservations SET budget_day = ? WHERE id = 'reservation-1'`, "2026-07-31"},
		{"provider", `UPDATE llm_budget_reservations SET provider = ? WHERE id = 'reservation-1'`, "other"},
		{"model", `UPDATE llm_budget_reservations SET model = ? WHERE id = 'reservation-1'`, "other-model"},
		{"endpoint", `UPDATE llm_budget_reservations SET endpoint = ? WHERE id = 'reservation-1'`, "https://other.invalid"},
		{"fingerprint", `UPDATE llm_budget_reservations SET policy_fingerprint = ? WHERE id = 'reservation-1'`, "other"},
		{"price revision", `UPDATE llm_budget_reservations SET price_revision = ? WHERE id = 'reservation-1'`, "other"},
		{"request hash", `UPDATE llm_budget_reservations SET request_hash = ? WHERE id = 'reservation-1'`, "other"},
		{"input rate", `UPDATE llm_budget_reservations SET input_usd_per_million = ? WHERE id = 'reservation-1'`, 1},
		{"output rate", `UPDATE llm_budget_reservations SET output_usd_per_million = ? WHERE id = 'reservation-1'`, 1},
		{"input cap", `UPDATE llm_budget_reservations SET max_input_tokens = ? WHERE id = 'reservation-1'`, 1},
		{"output cap", `UPDATE llm_budget_reservations SET max_output_tokens = ? WHERE id = 'reservation-1'`, 1},
		{"reserved amount", `UPDATE llm_budget_reservations SET reserved_usd_nanos = ? WHERE id = 'reservation-1'`, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			mustInsertBudgetRow(t, s.db, validBudgetRow())
			if _, err := s.db.Exec(tc.update, tc.arg); err == nil {
				t.Fatalf("expected %s to be immutable", tc.name)
			}
		})
	}
}

func TestMigration026MakesWorkspaceImmutable(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	// Create a second real workspace so the foreign key would accept the move
	// and only the immutability trigger can reject it.
	if err := s.CreateWorkspace(ctx, &models.Workspace{ID: "workspace-2", Name: "workspace-2"}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	mustInsertBudgetRow(t, s.db, validBudgetRow())

	if _, err := s.db.Exec(
		`UPDATE llm_budget_reservations SET workspace_id = ? WHERE id = 'reservation-1'`, "workspace-2",
	); err == nil {
		t.Fatal("expected the reservation workspace to be immutable")
	}
}

func TestMigration026MakesDispatchMarkerImmutable(t *testing.T) {
	now := time.Now().UTC()
	s := newTestStore(t)
	row := validBudgetRow()
	row.State = "possibly_dispatched"
	row.DispatchedAt = &now
	mustInsertBudgetRow(t, s.db, row)

	later := now.Add(time.Minute)
	if _, err := s.db.Exec(
		`UPDATE llm_budget_reservations SET dispatched_at = ? WHERE id = ?`, later, row.ID,
	); err == nil {
		t.Fatal("expected an existing dispatch marker to be immutable")
	}
}

func TestMigration026ForbidsReservationDeletes(t *testing.T) {
	s := newTestStore(t)
	mustInsertBudgetRow(t, s.db, validBudgetRow())
	if _, err := s.db.Exec(`DELETE FROM llm_budget_reservations WHERE id = 'reservation-1'`); err == nil {
		t.Fatal("expected reservation deletes to be forbidden")
	}
}

func TestMigration026KeepsTransitionsAppendOnly(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	mustInsertBudgetRow(t, s.db, validBudgetRow())

	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO llm_budget_transitions (
			id, reservation_id, workspace_id, event, from_state, to_state,
			amount_usd_nanos, reason, policy_fingerprint, created_at
		) VALUES ('transition-1','reservation-1','default','reserve',NULL,'reserved',68000000,'','fingerprint',?)`,
		time.Now().UTC()); err != nil {
		t.Fatalf("insert transition: %v", err)
	}

	if _, err := s.db.Exec(`UPDATE llm_budget_transitions SET reason = 'changed' WHERE id = 'transition-1'`); err == nil {
		t.Fatal("expected transition updates to be forbidden")
	}
	if _, err := s.db.Exec(`DELETE FROM llm_budget_transitions WHERE id = 'transition-1'`); err == nil {
		t.Fatal("expected transition deletes to be forbidden")
	}
}

func TestMigration026RejectsTransitionForUnknownReservation(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`
		INSERT INTO llm_budget_transitions (
			id, reservation_id, workspace_id, event, from_state, to_state,
			amount_usd_nanos, reason, policy_fingerprint, created_at
		) VALUES ('transition-1','missing','default','reserve',NULL,'reserved',0,'','fingerprint',?)`,
		time.Now().UTC()); err == nil {
		t.Fatal("expected a transition for an unknown reservation to be rejected")
	}
}

func TestMigration026RejectsUnknownTransitionEvent(t *testing.T) {
	s := newTestStore(t)
	mustInsertBudgetRow(t, s.db, validBudgetRow())
	if _, err := s.db.Exec(`
		INSERT INTO llm_budget_transitions (
			id, reservation_id, workspace_id, event, from_state, to_state,
			amount_usd_nanos, reason, policy_fingerprint, created_at
		) VALUES ('transition-1','reservation-1','default','refund',NULL,'reserved',0,'','fingerprint',?)`,
		time.Now().UTC()); err == nil {
		t.Fatal("expected an unknown transition event to be rejected")
	}
}
