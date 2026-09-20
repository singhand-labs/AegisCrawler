package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/crypto"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	_ "modernc.org/sqlite"
)

var (
	ErrTaskNotFound       = errors.New("task not found")
	ErrRuleNotFound       = errors.New("rule not found")
	ErrLeaseConflict      = errors.New("task already leased by another worker")
	ErrNoTaskAvailable    = errors.New("no task available")
	ErrTaskNotCancellable = errors.New("task cannot be cancelled in its current state")
	ErrTaskNotRetryable   = errors.New("task cannot be retried in its current state")
	ErrWorkspaceConflict  = errors.New("resource id is unavailable in this workspace")
	ErrMCPTokenNotFound   = errors.New("mcp token not found")
)

// Store abstracts persistence.
type Store struct {
	db            *sql.DB
	encryptionKey string
}

// New opens a SQLite database and runs migrations.
// It defaults to WAL journal mode; use NewWithConfig to override.
func New(dbPath string, encryptionKey ...string) (*Store, error) {
	return NewWithConfig(&config.Config{SQLiteJournalMode: "WAL"}, dbPath, encryptionKey...)
}

// NewWithConfig opens a SQLite database using the provided configuration and runs migrations.
func NewWithConfig(cfg *config.Config, dbPath string, encryptionKey ...string) (*Store, error) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	journalMode := strings.ToUpper(cfg.SQLiteJournalMode)
	if journalMode == "" {
		journalMode = "WAL"
	}

	dsn := fmt.Sprintf("%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(%s)", dbPath, journalMode)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(time.Hour)

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	key := ""
	if len(encryptionKey) > 0 {
		key = encryptionKey[0]
	}
	return &Store{db: db, encryptionKey: key}, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying database handle for testing.
func (s *Store) DB() *sql.DB {
	return s.db
}

// Ping verifies the database is reachable by executing a simple query.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.QueryRowContext(ctx, `SELECT 1`).Scan(new(int))
}

// WithTx executes the provided function inside a database transaction.
func (s *Store) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func (s *Store) encryptVariables(v models.JSON) (models.JSON, error) {
	if s.encryptionKey == "" || len(v) == 0 {
		return v, nil
	}
	var vars map[string]any
	if err := json.Unmarshal(v, &vars); err != nil {
		return nil, fmt.Errorf("unmarshal variables for encryption: %w", err)
	}
	encrypted, err := crypto.EncryptVariables(s.encryptionKey, vars)
	if err != nil {
		return nil, fmt.Errorf("encrypt variables: %w", err)
	}
	return models.JSON(encrypted), nil
}

func (s *Store) decryptVariables(v models.JSON) (models.JSON, error) {
	if s.encryptionKey == "" || len(v) == 0 {
		return v, nil
	}
	vars, err := crypto.DecryptVariables(s.encryptionKey, string(v))
	if err != nil {
		return nil, fmt.Errorf("decrypt variables: %w", err)
	}
	plain, err := json.Marshal(vars)
	if err != nil {
		return nil, fmt.Errorf("marshal decrypted variables: %w", err)
	}
	return models.JSON(plain), nil
}

type migration struct {
	version int
	name    string
	up      func(*sql.Tx) error
}

var migrations = []migration{
	{1, "initial schema", migration001},
	{2, "add approval_status", migration002},
	{3, "retention indexes", migration003},
	{4, "audit_logs", migration004},
	{5, "schedules", migration005},
	{6, "scheduler locks and cancel flag", migration006},
	{7, "rule_enhancements", migration007},
	{8, "llm_jobs", migration008},
	{9, "llm_cache", migration009},
	{10, "rule_source", migration010},
	{11, "workspace scope", migration011},
	{12, "encrypted recordings", migration012},
	{13, "immutable rule versions", migration013},
	{14, "durable collection requirements", migration014},
	{15, "durable dsl replay workflows", migration015},
	{16, "versioned task execution and results", migration016},
	{17, "workspace mcp tokens", migration017},
	{18, "resumable human interventions", migration018},
	{19, "llm job chunk progress", migration019},
	{20, "manual dsl workflow correction", migration020},
	{21, "durable llm attempt artifacts", migration021},
	{22, "harden llm capture and retry provenance", migration022},
	{23, "fence dsl provider artifacts to current leases", migration023},
	{24, "durable one-shot dsl selector repair", migration024},
	{25, "admin reviewed dsl attempt adoption", migration025},
	{26, "persistent transactional llm budget ledger", migration026},
	{27, "dsl replay attempt expiry", migration027},
	{28, "dsl workflow and rule version safety flags", migration028},
	{29, "dsl workflow budget envelope", migration029},
	{30, "dsl approval and rule version provenance", migration030},
	{31, "idempotency cache", migration031},
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	for _, m := range migrations {
		var exists bool
		if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = ?)`, m.version).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %03d: %w", m.version, err)
		}
		if exists {
			continue
		}

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %03d: %w", m.version, err)
		}
		if err := m.up(tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %03d %s: %w", m.version, m.name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, m.version); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %03d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %03d: %w", m.version, err)
		}
	}
	return nil
}

const migration001Schema = `
CREATE TABLE IF NOT EXISTS rules (
    id TEXT PRIMARY KEY,
    version TEXT NOT NULL,
    name TEXT NOT NULL,
    domain TEXT NOT NULL,
    url_pattern TEXT,
    enabled BOOLEAN NOT NULL DEFAULT 1,
    priority TEXT NOT NULL DEFAULT 'normal',
    entry TEXT,
    variables TEXT,
    selectors TEXT,
    humanize TEXT,
    steps TEXT NOT NULL,
    output TEXT,
    send_policy TEXT,
    hooks TEXT,
    tags TEXT,
    owner TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS tasks (
    id TEXT PRIMARY KEY,
    rule_id TEXT NOT NULL REFERENCES rules(id),
    rule_version TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    priority TEXT NOT NULL DEFAULT 'normal',
    variables TEXT,
    worker_id TEXT,
    lease_until DATETIME,
    retry_count INTEGER NOT NULL DEFAULT 0,
    max_retries INTEGER NOT NULL DEFAULT 3,
    output_schema TEXT,
    send_policy TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    scheduled_at DATETIME,
    completed_at DATETIME,
    error_type TEXT,
    error_message TEXT,
    schedule_id TEXT REFERENCES schedules(id)
);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_lease ON tasks(lease_until);
CREATE INDEX IF NOT EXISTS idx_tasks_worker ON tasks(worker_id);
CREATE INDEX IF NOT EXISTS idx_tasks_schedule_id ON tasks(schedule_id);

CREATE TABLE IF NOT EXISTS schedules (
    id TEXT PRIMARY KEY,
    rule_id TEXT NOT NULL REFERENCES rules(id),
    rule_version TEXT NOT NULL,
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    expression TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT 1,
    next_run_at DATETIME,
    last_run_at DATETIME,
    variables TEXT,
    priority TEXT NOT NULL DEFAULT 'normal',
    max_retries INTEGER NOT NULL DEFAULT 3,
    catchup TEXT NOT NULL DEFAULT 'skip',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_schedules_next_run ON schedules(next_run_at);
CREATE INDEX IF NOT EXISTS idx_schedules_rule_id ON schedules(rule_id);
CREATE INDEX IF NOT EXISTS idx_schedules_enabled ON schedules(enabled);

CREATE TABLE IF NOT EXISTS results (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id),
    worker_id TEXT NOT NULL,
    payload TEXT NOT NULL,
    immediate BOOLEAN NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_results_task ON results(task_id);

CREATE TABLE IF NOT EXISTS logs (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id),
    worker_id TEXT NOT NULL,
    level TEXT NOT NULL,
    message TEXT NOT NULL,
    extra TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_logs_task ON logs(task_id);

CREATE TABLE IF NOT EXISTS snapshots (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id),
    worker_id TEXT NOT NULL,
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    data TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_snapshots_task ON snapshots(task_id);

CREATE TABLE IF NOT EXISTS heartbeats (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id),
    worker_id TEXT NOT NULL,
    payload TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_heartbeats_task ON heartbeats(task_id);

CREATE TABLE IF NOT EXISTS task_status_updates (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id),
    worker_id TEXT NOT NULL,
    status TEXT NOT NULL,
    message TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_status_task ON task_status_updates(task_id);

CREATE TABLE IF NOT EXISTS checkpoints (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id),
    worker_id TEXT NOT NULL,
    name TEXT NOT NULL,
    payload TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_checkpoints_task ON checkpoints(task_id);
`

func migration001(tx *sql.Tx) error {
	_, err := tx.Exec(migration001Schema)
	return err
}

func migration002(tx *sql.Tx) error {
	var colCount int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('rules') WHERE name = 'approval_status'`).Scan(&colCount); err != nil {
		return err
	}
	if colCount == 0 {
		if _, err := tx.Exec(`ALTER TABLE rules ADD COLUMN approval_status TEXT NOT NULL DEFAULT 'approved'`); err != nil {
			return fmt.Errorf("add approval_status column: %w", err)
		}
	}
	return nil
}

const migration003Schema = `
CREATE INDEX IF NOT EXISTS idx_results_created_at ON results(created_at);
CREATE INDEX IF NOT EXISTS idx_logs_created_at ON logs(created_at);
CREATE INDEX IF NOT EXISTS idx_snapshots_created_at ON snapshots(created_at);
CREATE INDEX IF NOT EXISTS idx_heartbeats_created_at ON heartbeats(created_at);
CREATE INDEX IF NOT EXISTS idx_status_updates_created_at ON task_status_updates(created_at);
CREATE INDEX IF NOT EXISTS idx_checkpoints_created_at ON checkpoints(created_at);
CREATE INDEX IF NOT EXISTS idx_tasks_completed_at ON tasks(completed_at);
`

func migration003(tx *sql.Tx) error {
	_, err := tx.Exec(migration003Schema)
	return err
}

const migration004Schema = `
CREATE TABLE IF NOT EXISTS audit_logs (
    id TEXT PRIMARY KEY,
    actor TEXT NOT NULL,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    payload TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_audit_logs_actor ON audit_logs(actor);
CREATE INDEX IF NOT EXISTS idx_audit_logs_action ON audit_logs(action);
CREATE INDEX IF NOT EXISTS idx_audit_logs_resource ON audit_logs(resource_type, resource_id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs(created_at);
`

func migration004(tx *sql.Tx) error {
	_, err := tx.Exec(migration004Schema)
	return err
}

const migration005Schema = `
CREATE TABLE IF NOT EXISTS schedules (
    id TEXT PRIMARY KEY,
    rule_id TEXT NOT NULL REFERENCES rules(id),
    rule_version TEXT NOT NULL,
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    expression TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT 1,
    next_run_at DATETIME,
    last_run_at DATETIME,
    variables TEXT,
    priority TEXT NOT NULL DEFAULT 'normal',
    max_retries INTEGER NOT NULL DEFAULT 3,
    catchup TEXT NOT NULL DEFAULT 'skip',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_schedules_next_run ON schedules(next_run_at);
CREATE INDEX IF NOT EXISTS idx_schedules_rule_id ON schedules(rule_id);
CREATE INDEX IF NOT EXISTS idx_schedules_enabled ON schedules(enabled);
`

func migration005(tx *sql.Tx) error {
	var colCount int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('tasks') WHERE name = 'schedule_id'`).Scan(&colCount); err != nil {
		return err
	}
	if colCount == 0 {
		if _, err := tx.Exec(`ALTER TABLE tasks ADD COLUMN schedule_id TEXT`); err != nil {
			return fmt.Errorf("add schedule_id column: %w", err)
		}
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_schedule_id ON tasks(schedule_id)`); err != nil {
		return fmt.Errorf("create idx_tasks_schedule_id: %w", err)
	}
	_, err := tx.Exec(migration005Schema)
	return err
}

const migration006Schema = `
CREATE TABLE IF NOT EXISTS scheduler_locks (
    name TEXT PRIMARY KEY,
    owner TEXT,
    expires_at DATETIME,
    acquired_at DATETIME
);
`

func migration006(tx *sql.Tx) error {
	var colCount int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('tasks') WHERE name = 'cancel_requested'`).Scan(&colCount); err != nil {
		return err
	}
	if colCount == 0 {
		if _, err := tx.Exec(`ALTER TABLE tasks ADD COLUMN cancel_requested BOOLEAN NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add cancel_requested column: %w", err)
		}
	}
	_, err := tx.Exec(migration006Schema)
	return err
}

const migration007Schema = `
CREATE TABLE IF NOT EXISTS rule_enhancements (
    id TEXT PRIMARY KEY,
    rule_id TEXT REFERENCES rules(id) ON DELETE SET NULL,
    baseline TEXT NOT NULL,
    enhanced TEXT NOT NULL,
    patch TEXT NOT NULL,
    user_hint TEXT,
    provider TEXT,
    model TEXT,
    input_tokens INTEGER,
    output_tokens INTEGER,
    suggestions TEXT,
    safety_flags TEXT,
    status TEXT NOT NULL DEFAULT 'pending',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_rule_enhancements_rule_id ON rule_enhancements(rule_id);
CREATE INDEX IF NOT EXISTS idx_rule_enhancements_status ON rule_enhancements(status);
`

func migration007(tx *sql.Tx) error {
	_, err := tx.Exec(migration007Schema)
	return err
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func createRuleWithExec(ctx context.Context, ex execer, r *models.Rule) error {
	r.WorkspaceID = workspaceID(ctx)
	if r.ApprovalStatus == "" {
		r.ApprovalStatus = string(models.RuleApprovalApproved)
	}
	if r.Source == "" {
		r.Source = "pageagent"
	}
	const q = `
		INSERT INTO rules (id, workspace_id, version, name, domain, url_pattern, enabled, priority, entry, variables, selectors, humanize, steps, output, send_policy, hooks, tags, owner, approval_status, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			version=excluded.version, name=excluded.name, domain=excluded.domain, url_pattern=excluded.url_pattern,
			enabled=excluded.enabled, priority=excluded.priority, entry=excluded.entry, variables=excluded.variables,
			selectors=excluded.selectors, humanize=excluded.humanize, steps=excluded.steps, output=excluded.output,
			send_policy=excluded.send_policy, hooks=excluded.hooks, tags=excluded.tags, owner=excluded.owner,
			approval_status=excluded.approval_status, source=excluded.source, updated_at=excluded.updated_at
		WHERE rules.workspace_id = excluded.workspace_id
	`
	result, err := ex.ExecContext(ctx, q,
		r.ID, r.WorkspaceID, r.Version, r.Name, r.Domain, r.URLPattern, r.Enabled, r.Priority, r.Entry,
		r.Variables, r.Selectors, r.Humanize, r.Steps, r.Output, r.SendPolicy, r.Hooks, r.Tags,
		r.Owner, r.ApprovalStatus, r.Source, r.CreatedAt, r.UpdatedAt,
	)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrWorkspaceConflict
	}
	return nil
}

// CreateRule inserts or replaces a rule.
func (s *Store) CreateRule(ctx context.Context, r *models.Rule) error {
	return createRuleWithExec(ctx, s.db, r)
}

// CreateRuleTx inserts or replaces a rule within an existing transaction.
func (s *Store) CreateRuleTx(ctx context.Context, tx *sql.Tx, r *models.Rule) error {
	return createRuleWithExec(ctx, tx, r)
}

// ListRulesFilter holds pagination and filter criteria for ListRules.
type ListRulesFilter struct {
	Enabled        *bool
	ApprovalStatus string
	Domain         string
	Owner          string
	Limit          int
	Offset         int
}

// ListTasksFilter holds pagination and filter criteria for ListTasks.
type ListTasksFilter struct {
	Status        string
	RuleID        string
	WorkerID      string
	Priority      string
	CreatedAfter  time.Time
	CreatedBefore time.Time
	Limit         int
	Offset        int
}

// ListAuditLogsFilter holds pagination and filter criteria for ListAuditLogs.
type ListAuditLogsFilter struct {
	Actor         string
	Action        string
	ResourceType  string
	ResourceID    string
	CreatedAfter  time.Time
	CreatedBefore time.Time
	Limit         int
	Offset        int
}

// ListSchedulesFilter holds pagination and filter criteria for ListSchedules.
type ListSchedulesFilter struct {
	RuleID  string
	Type    string
	Enabled *bool
	Limit   int
	Offset  int
}

// GetRuleByID returns a rule by id.
func (s *Store) GetRuleByID(ctx context.Context, id string) (*models.Rule, error) {
	const q = `SELECT id, workspace_id, version, name, domain, url_pattern, enabled, priority, entry, variables, selectors, humanize, steps, output, send_policy, hooks, tags, owner, approval_status, source, created_at, updated_at FROM rules WHERE id = ? AND workspace_id = ?`
	row := s.db.QueryRowContext(ctx, q, id, workspaceID(ctx))
	r := &models.Rule{}
	err := row.Scan(&r.ID, &r.WorkspaceID, &r.Version, &r.Name, &r.Domain, &r.URLPattern, &r.Enabled, &r.Priority, &r.Entry,
		&r.Variables, &r.Selectors, &r.Humanize, &r.Steps, &r.Output, &r.SendPolicy, &r.Hooks, &r.Tags,
		&r.Owner, &r.ApprovalStatus, &r.Source, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRuleNotFound
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// ListRules returns rules matching the provided filter, plus the total matching count.
func (s *Store) ListRules(ctx context.Context, filter ListRulesFilter) ([]*models.Rule, int, error) {
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}

	where := []string{"workspace_id = ?"}
	args := []any{workspaceID(ctx)}
	if filter.Enabled != nil {
		where = append(where, "enabled = ?")
		args = append(args, *filter.Enabled)
	}
	if filter.ApprovalStatus != "" {
		where = append(where, "approval_status = ?")
		args = append(args, filter.ApprovalStatus)
	}
	if filter.Domain != "" {
		where = append(where, "domain = ?")
		args = append(args, filter.Domain)
	}
	if filter.Owner != "" {
		where = append(where, "owner = ?")
		args = append(args, filter.Owner)
	}

	whereClause := "WHERE " + strings.Join(where, " AND ")

	const selectCols = `id, workspace_id, version, name, domain, url_pattern, enabled, priority, entry, variables, selectors, humanize, steps, output, send_policy, hooks, tags, owner, approval_status, source, created_at, updated_at`

	var total int
	countQuery := "SELECT COUNT(*) FROM rules " + whereClause
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count rules: %w", err)
	}

	query := fmt.Sprintf("SELECT %s FROM rules %s ORDER BY created_at DESC LIMIT ? OFFSET ?", selectCols, whereClause)
	queryArgs := append(args, filter.Limit, filter.Offset)
	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list rules: %w", err)
	}
	defer rows.Close()

	var rules []*models.Rule
	for rows.Next() {
		r := &models.Rule{}
		if err := rows.Scan(&r.ID, &r.WorkspaceID, &r.Version, &r.Name, &r.Domain, &r.URLPattern, &r.Enabled, &r.Priority, &r.Entry,
			&r.Variables, &r.Selectors, &r.Humanize, &r.Steps, &r.Output, &r.SendPolicy, &r.Hooks, &r.Tags,
			&r.Owner, &r.ApprovalStatus, &r.Source, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan rule: %w", err)
		}
		rules = append(rules, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate rules: %w", err)
	}
	return rules, total, nil
}

func deleteRuleWithExec(ctx context.Context, ex execer, id string) error {
	workspace := workspaceID(ctx)
	childTables := []string{"results", "logs", "snapshots", "heartbeats", "task_status_updates", "human_interventions", "checkpoints"}
	for _, table := range childTables {
		if _, err := ex.ExecContext(ctx,
			fmt.Sprintf("DELETE FROM %s WHERE workspace_id = ? AND task_id IN (SELECT id FROM tasks WHERE rule_id = ? AND workspace_id = ?)", table),
			workspace, id, workspace); err != nil {
			return fmt.Errorf("delete %s for rule: %w", table, err)
		}
	}
	if _, err := ex.ExecContext(ctx,
		`DELETE FROM execution_attempts WHERE workspace_id = ? AND task_id IN (SELECT id FROM tasks WHERE rule_id = ? AND workspace_id = ?)`,
		workspace, id, workspace); err != nil {
		return fmt.Errorf("delete execution attempts for rule: %w", err)
	}

	if _, err := ex.ExecContext(ctx, `DELETE FROM tasks WHERE rule_id = ? AND workspace_id = ?`, id, workspace); err != nil {
		return fmt.Errorf("delete tasks for rule: %w", err)
	}

	res, err := ex.ExecContext(ctx, `DELETE FROM rules WHERE id = ? AND workspace_id = ?`, id, workspace)
	if err != nil {
		return fmt.Errorf("delete rule: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrRuleNotFound
	}
	return nil
}

// DeleteRule deletes a rule and all of its tasks and child rows.
func (s *Store) DeleteRule(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete transaction: %w", err)
	}
	defer tx.Rollback()

	if err := deleteRuleWithExec(ctx, tx, id); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete rule: %w", err)
	}
	return nil
}

// DeleteRuleTx deletes a rule and all of its tasks and child rows within an existing transaction.
func (s *Store) DeleteRuleTx(ctx context.Context, tx *sql.Tx, id string) error {
	return deleteRuleWithExec(ctx, tx, id)
}

// ListEnabledRules returns all enabled and approved rules.
func (s *Store) ListEnabledRules(ctx context.Context) ([]*models.Rule, error) {
	const q = `SELECT id, workspace_id, version, name, domain, url_pattern, enabled, priority, entry, variables, selectors, humanize, steps, output, send_policy, hooks, tags, owner, approval_status, source, created_at, updated_at FROM rules WHERE workspace_id = ? AND enabled = 1 AND approval_status = 'approved'`
	rows, err := s.db.QueryContext(ctx, q, workspaceID(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []*models.Rule
	for rows.Next() {
		r := &models.Rule{}
		if err := rows.Scan(&r.ID, &r.WorkspaceID, &r.Version, &r.Name, &r.Domain, &r.URLPattern, &r.Enabled, &r.Priority, &r.Entry,
			&r.Variables, &r.Selectors, &r.Humanize, &r.Steps, &r.Output, &r.SendPolicy, &r.Hooks, &r.Tags,
			&r.Owner, &r.ApprovalStatus, &r.Source, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, rows.Err()
}

// UpdateRuleApprovalStatus updates the approval_status of a rule.
func (s *Store) UpdateRuleApprovalStatus(ctx context.Context, id, status string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE rules SET approval_status = ?, updated_at = ? WHERE id = ? AND workspace_id = ?`,
		status, time.Now().UTC(), id, workspaceID(ctx))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrRuleNotFound
	}
	return nil
}

// CreateTask inserts a new task.
func (s *Store) CreateTask(ctx context.Context, t *models.Task) error {
	t.WorkspaceID = workspaceID(ctx)
	if t.RuleVersionNumber > 0 {
		if err := s.BindTaskToRuleVersion(ctx, t, t.RuleVersionNumber); err != nil {
			return err
		}
	} else {
		t.RuleVersionNumber = 1
		if len(t.InputSchema) == 0 {
			t.InputSchema = models.JSON("{}")
		}
	}
	var ruleExists bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM rules WHERE id = ? AND workspace_id = ?)`,
		t.RuleID, t.WorkspaceID).Scan(&ruleExists); err != nil {
		return err
	}
	if !ruleExists {
		return ErrRuleNotFound
	}
	variables, err := s.encryptVariables(t.Variables)
	if err != nil {
		return err
	}
	const q = `
		INSERT INTO tasks (id, workspace_id, rule_id, rule_version, rule_version_number,
			status, priority, variables, input_schema, worker_id, lease_until,
			retry_count, max_retries, output_schema, send_policy, created_at, updated_at,
			scheduled_at, schedule_id, current_attempt_id, browser_profile_id, cancel_requested)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err = s.db.ExecContext(ctx, q,
		t.ID, t.WorkspaceID, t.RuleID, t.RuleVersion, t.RuleVersionNumber,
		t.Status, t.Priority, variables, t.InputSchema, t.WorkerID, t.LeaseUntil,
		t.RetryCount, t.MaxRetries, t.OutputSchema, t.SendPolicy, t.CreatedAt, t.UpdatedAt,
		t.ScheduledAt, t.ScheduleID, t.CurrentAttemptID, t.BrowserProfileID, t.CancelRequested,
	)
	return err
}

const taskSelectColumns = `id, workspace_id, rule_id, rule_version, rule_version_number,
	status, priority, variables, input_schema, worker_id, lease_until, retry_count,
	max_retries, output_schema, send_policy, created_at, updated_at, scheduled_at,
	completed_at, error_type, error_message, schedule_id, current_attempt_id,
	browser_profile_id, cancel_requested`

// GetTaskByID returns a task by id.
func (s *Store) GetTaskByID(ctx context.Context, id string) (*models.Task, error) {
	return s.getTaskByID(ctx, s.db, id, workspaceID(ctx))
}

func (s *Store) getTaskByID(ctx context.Context, ex ruleVersionExecer, id, workspace string) (*models.Task, error) {
	row := ex.QueryRowContext(ctx, `SELECT `+taskSelectColumns+`
		FROM tasks WHERE id = ? AND workspace_id = ?`, id, workspace)
	t := &models.Task{}
	err := row.Scan(&t.ID, &t.WorkspaceID, &t.RuleID, &t.RuleVersion, &t.RuleVersionNumber,
		&t.Status, &t.Priority, &t.Variables, &t.InputSchema, &t.WorkerID, &t.LeaseUntil,
		&t.RetryCount, &t.MaxRetries, &t.OutputSchema, &t.SendPolicy, &t.CreatedAt,
		&t.UpdatedAt, &t.ScheduledAt, &t.CompletedAt, &t.ErrorType, &t.ErrorMessage,
		&t.ScheduleID, &t.CurrentAttemptID, &t.BrowserProfileID, &t.CancelRequested)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	t.Variables, err = s.decryptVariables(t.Variables)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// ListTasks returns tasks matching the provided filter, plus the total matching count.
func (s *Store) ListTasks(ctx context.Context, filter ListTasksFilter) ([]*models.Task, int, error) {
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}

	where := []string{"workspace_id = ?"}
	args := []any{workspaceID(ctx)}
	if filter.Status != "" {
		where = append(where, "status = ?")
		args = append(args, filter.Status)
	}
	if filter.RuleID != "" {
		where = append(where, "rule_id = ?")
		args = append(args, filter.RuleID)
	}
	if filter.WorkerID != "" {
		where = append(where, "worker_id = ?")
		args = append(args, filter.WorkerID)
	}
	if filter.Priority != "" {
		where = append(where, "priority = ?")
		args = append(args, filter.Priority)
	}
	if !filter.CreatedAfter.IsZero() {
		where = append(where, "created_at >= ?")
		args = append(args, filter.CreatedAfter.UTC())
	}
	if !filter.CreatedBefore.IsZero() {
		where = append(where, "created_at <= ?")
		args = append(args, filter.CreatedBefore.UTC())
	}

	whereClause := "WHERE " + strings.Join(where, " AND ")

	var total int
	countQuery := "SELECT COUNT(*) FROM tasks " + whereClause
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count tasks: %w", err)
	}

	query := fmt.Sprintf("SELECT %s FROM tasks %s ORDER BY created_at DESC LIMIT ? OFFSET ?", taskSelectColumns, whereClause)
	queryArgs := append(args, filter.Limit, filter.Offset)
	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	var tasks []*models.Task
	for rows.Next() {
		t := &models.Task{}
		if err := rows.Scan(&t.ID, &t.WorkspaceID, &t.RuleID, &t.RuleVersion,
			&t.RuleVersionNumber, &t.Status, &t.Priority, &t.Variables, &t.InputSchema,
			&t.WorkerID, &t.LeaseUntil, &t.RetryCount, &t.MaxRetries, &t.OutputSchema,
			&t.SendPolicy, &t.CreatedAt, &t.UpdatedAt, &t.ScheduledAt, &t.CompletedAt,
			&t.ErrorType, &t.ErrorMessage, &t.ScheduleID, &t.CurrentAttemptID,
			&t.BrowserProfileID, &t.CancelRequested); err != nil {
			return nil, 0, fmt.Errorf("scan task: %w", err)
		}
		t.Variables, err = s.decryptVariables(t.Variables)
		if err != nil {
			return nil, 0, fmt.Errorf("decrypt task variables: %w", err)
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate tasks: %w", err)
	}
	return tasks, total, nil
}

// CancelTask cancels a pending, leased, or human-waiting task, or requests cancellation of a running task
// by setting cancel_requested while preserving the running status.
func (s *Store) CancelTask(ctx context.Context, id string) error {
	now := time.Now().UTC()
	workspace := workspaceID(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Fast path: pending/leased/human-waiting tasks transition directly to cancelled.
	res, err := tx.ExecContext(ctx,
		`UPDATE tasks SET status = 'cancelled', updated_at = ?, completed_at = ?
		 WHERE id = ? AND workspace_id = ? AND status IN ('pending','leased','waiting_for_human')`,
		now, now, id, workspace)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE execution_attempts
			SET status = ?, completed_at = ?, lease_until = NULL
			WHERE id = (SELECT current_attempt_id FROM tasks WHERE id = ? AND workspace_id = ?)
			  AND workspace_id = ? AND status IN (?, ?)`, models.ExecutionAttemptCancelled,
			now, id, workspace, workspace, models.ExecutionAttemptLeased, models.ExecutionAttemptWaitingHuman); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE human_interventions
			SET status = 'cancelled', decided_at = ?, decided_by = 'system:task_cancelled',
			    decision_note = 'task cancelled while waiting for operator'
			WHERE workspace_id = ? AND task_id = ? AND status = 'pending'`, now, workspace, id); err != nil {
			return err
		}
		return tx.Commit()
	}

	// Running tasks get a cancellation request flag instead of forced status change.
	res, err = tx.ExecContext(ctx,
		`UPDATE tasks SET cancel_requested = 1, updated_at = ?
		 WHERE id = ? AND workspace_id = ? AND status = 'running'`,
		now, id, workspace)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return tx.Commit()
	}

	// Distinguish 404 vs 409 by checking existence.
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM tasks WHERE id = ? AND workspace_id = ?`, id, workspace).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTaskNotFound
		}
		return err
	}
	return ErrTaskNotCancellable
}

// AcquireSchedulerLock attempts to acquire a scheduler lock with the given lease.
// Returns true if the lock was acquired, renewed by the same owner, or stolen
// from an expired owner.
func (s *Store) AcquireSchedulerLock(ctx context.Context, name, owner string, lease time.Duration) (bool, error) {
	workspace := workspaceID(ctx)
	now := time.Now().UTC()
	expiresAt := now.Add(lease)

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO scheduler_locks (workspace_id, name, owner, expires_at, acquired_at) VALUES (?, ?, ?, ?, ?)`,
		workspace, name, owner, expiresAt, now)
	if err == nil {
		return true, nil
	}

	// Lock already exists. The current owner may renew it through the acquire
	// path, while another owner may only steal an expired lock.
	res, err := s.db.ExecContext(ctx,
		`UPDATE scheduler_locks SET owner = ?, expires_at = ?, acquired_at = ?
		 WHERE workspace_id = ? AND name = ? AND (owner = ? OR expires_at <= ?)`,
		owner, expiresAt, now, workspace, name, owner, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// RenewSchedulerLock extends the lease for a lock currently held by owner.
func (s *Store) RenewSchedulerLock(ctx context.Context, name, owner string, lease time.Duration) (bool, error) {
	now := time.Now().UTC()
	expiresAt := now.Add(lease)
	res, err := s.db.ExecContext(ctx,
		`UPDATE scheduler_locks SET expires_at = ?, acquired_at = ? WHERE workspace_id = ? AND name = ? AND owner = ?`,
		expiresAt, now, workspaceID(ctx), name, owner)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ReleaseSchedulerLock releases a scheduler lock held by owner.
func (s *Store) ReleaseSchedulerLock(ctx context.Context, name, owner string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM scheduler_locks WHERE workspace_id = ? AND name = ? AND owner = ?`,
		workspaceID(ctx), name, owner)
	return err
}

// RetryTask transitions a failed, dead_letter, or cancelled task back to pending
// and resets retry bookkeeping.
func (s *Store) RetryTask(ctx context.Context, id string) error {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET status = 'pending', retry_count = 0, worker_id = NULL,
		   lease_until = NULL, completed_at = NULL, error_type = NULL,
		   error_message = NULL, updated_at = ?
		 WHERE id = ? AND workspace_id = ? AND status IN ('failed','dead_letter','cancelled')`,
		now, id, workspaceID(ctx))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM tasks WHERE id = ? AND workspace_id = ?`, id, workspaceID(ctx)).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrTaskNotFound
			}
			return err
		}
		return ErrTaskNotRetryable
	}
	return nil
}

// ClaimTask leases any available task to a worker. Worker-facing callers should
// use ClaimTaskForBrowserProfile so profile-bound tasks cannot be delivered to
// an incompatible browser session. This legacy entry point remains for
// internal callers that intentionally do not apply profile affinity.
func (s *Store) ClaimTask(ctx context.Context, workerID string, leaseDuration time.Duration, maxWorkerTasks int) (*models.Task, error) {
	claim, err := s.claimTask(ctx, workerID, nil, leaseDuration, maxWorkerTasks)
	if err != nil {
		return nil, err
	}
	return claim.Task, nil
}

// TaskClaim is the task lease plus its immutable execution lineage. RuleVersion
// and Contract are nil together only for a true legacy task.
type TaskClaim struct {
	Task        *models.Task
	RuleVersion *models.RuleVersion
	Contract    *models.RuleVersionContract
}

// ClaimTaskForBrowserProfile leases work compatible with a worker's current
// named browser profile. Workers without a profile may claim only unbound work;
// profiled workers may also claim unbound work, but exact profile matches win
// over unbound tasks at the same priority.
func (s *Store) ClaimTaskForBrowserProfile(ctx context.Context, workerID, browserProfileID string, leaseDuration time.Duration, maxWorkerTasks int) (*models.Task, error) {
	claim, err := s.claimTask(ctx, workerID, &browserProfileID, leaseDuration, maxWorkerTasks)
	if err != nil {
		return nil, err
	}
	return claim.Task, nil
}

// ClaimTaskForBrowserProfileWithContract resolves a versioned task's immutable
// rule and contract in the same transaction that creates its lease.
func (s *Store) ClaimTaskForBrowserProfileWithContract(ctx context.Context, workerID, browserProfileID string, leaseDuration time.Duration, maxWorkerTasks int) (*TaskClaim, error) {
	return s.claimTask(ctx, workerID, &browserProfileID, leaseDuration, maxWorkerTasks)
}

func (s *Store) claimTask(ctx context.Context, workerID string, browserProfileID *string, leaseDuration time.Duration, maxWorkerTasks int) (*TaskClaim, error) {
	workspace := workspaceID(ctx)
	now := time.Now().UTC()
	leaseUntil := now.Add(leaseDuration)

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Enforce per-worker concurrency limit.
	var activeCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE workspace_id = ? AND worker_id = ? AND status IN ('leased','running')`, workspace, workerID).Scan(&activeCount); err != nil {
		return nil, err
	}
	if activeCount >= maxWorkerTasks {
		return nil, ErrNoTaskAvailable
	}

	// Find the highest priority pending task whose lease has expired and whose rule is approved.
	profileFilter := ""
	profileOrder := ""
	queryArgs := []any{workspace, now, now}
	if browserProfileID != nil {
		if *browserProfileID == "" {
			profileFilter = "AND t.browser_profile_id = ''"
		} else {
			profileFilter = "AND t.browser_profile_id IN ('', ?)"
			profileOrder = "CASE WHEN t.browser_profile_id = ? THEN 0 ELSE 1 END,"
			queryArgs = append(queryArgs, *browserProfileID)
		}
	}
	q := fmt.Sprintf(`
		SELECT t.id FROM tasks t
		JOIN rules r ON r.id = t.rule_id
		WHERE t.workspace_id = ?
		  AND r.workspace_id = t.workspace_id
		  AND t.status = 'pending'
		  AND r.approval_status = 'approved'
		  AND (t.scheduled_at IS NULL OR t.scheduled_at <= ?)
		  AND (t.lease_until IS NULL OR t.lease_until <= ?)
		  %s
		ORDER BY CASE t.priority WHEN 'high' THEN 0 WHEN 'normal' THEN 1 ELSE 2 END,
		         %s
		         t.created_at ASC
		LIMIT 1
	`, profileFilter, profileOrder)
	if browserProfileID != nil && *browserProfileID != "" {
		queryArgs = append(queryArgs, *browserProfileID)
	}
	var taskID string
	if err := tx.QueryRowContext(ctx, q, queryArgs...).Scan(&taskID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoTaskAvailable
		}
		return nil, err
	}

	var attemptNumber int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_number), 0) + 1
		FROM execution_attempts WHERE workspace_id = ? AND task_id = ?`, workspace, taskID).Scan(&attemptNumber); err != nil {
		return nil, err
	}
	attemptID := NewID()
	if _, err := tx.ExecContext(ctx, `INSERT INTO execution_attempts (
		id, workspace_id, task_id, attempt_number, worker_id, status,
		lease_until, started_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, attemptID, workspace, taskID,
		attemptNumber, workerID, models.ExecutionAttemptLeased, leaseUntil, now); err != nil {
		return nil, err
	}
	const update = `UPDATE tasks SET status = 'leased', worker_id = ?, lease_until = ?,
		current_attempt_id = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND status = 'pending'`
	res, err := tx.ExecContext(ctx, update, workerID, leaseUntil, attemptID, now, taskID, workspace)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNoTaskAvailable
	}

	task, err := s.getTaskByID(ctx, tx, taskID, workspace)
	if err != nil {
		return nil, err
	}
	version, contract, err := resolveTaskRuleVersionContract(ctx, tx, workspace, task)
	if err != nil {
		return nil, err
	}
	if version != nil && version.Status != models.RuleApprovalApproved {
		return nil, ErrRuleVersionNotApproved
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &TaskClaim{Task: task, RuleVersion: version, Contract: contract}, nil
}

// RenewLease extends a task lease for an active (leased or running) task.
func (s *Store) RenewLease(ctx context.Context, taskID, workerID string, leaseDuration time.Duration) error {
	now := time.Now().UTC()
	leaseUntil := now.Add(leaseDuration)
	workspace := workspaceID(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE tasks SET lease_until = ?, updated_at = ? WHERE id = ? AND workspace_id = ? AND worker_id = ? AND status IN ('leased','running','waiting_for_human')`,
		leaseUntil, now, taskID, workspace, workerID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrLeaseConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE execution_attempts SET lease_until = ?
		WHERE id = (SELECT current_attempt_id FROM tasks WHERE id = ? AND workspace_id = ?)
		  AND workspace_id = ? AND worker_id = ? AND status IN ('leased','running','waiting_for_human')`,
		leaseUntil, taskID, workspace, workspace, workerID); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateTaskStatus updates task status and optional error info.
func (s *Store) UpdateTaskStatus(ctx context.Context, taskID, workerID, status, message, errorType string) error {
	now := time.Now().UTC()
	workspace := workspaceID(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var completedAt interface{}
	terminal := status == string(models.TaskStatusDone) || status == string(models.TaskStatusFailed) ||
		status == string(models.TaskStatusCancelled) || status == string(models.TaskStatusDeadLetter)
	mayCloseHumanWait := status == string(models.TaskStatusFailed) ||
		status == string(models.TaskStatusCancelled) || status == string(models.TaskStatusDeadLetter)
	releaseLease := terminal || status == string(models.TaskStatusWaitingHuman)
	if terminal {
		completedAt = now
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE tasks SET status = ?, updated_at = ?, completed_at = COALESCE(?, completed_at),
		 error_type = COALESCE(NULLIF(?, ''), error_type), error_message = COALESCE(NULLIF(?, ''), error_message),
		 lease_until = CASE WHEN ? THEN NULL ELSE lease_until END
		 WHERE id = ? AND workspace_id = ? AND worker_id = ?
		   AND (status IN ('leased','running') OR (status = 'waiting_for_human' AND ?))`,
		status, now, completedAt, errorType, message, releaseLease, taskID, workspace, workerID, mayCloseHumanWait)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrLeaseConflict
	}
	if mayCloseHumanWait {
		if _, err := tx.ExecContext(ctx, `UPDATE human_interventions
			SET status = 'cancelled', decided_at = ?, decided_by = 'system:worker_terminal',
			    decision_note = 'worker ended the attempt while waiting for operator'
			WHERE workspace_id = ? AND task_id = ? AND attempt_id = (
				SELECT current_attempt_id FROM tasks WHERE id = ? AND workspace_id = ?
			) AND status = 'pending'`, now, workspace, taskID, taskID, workspace); err != nil {
			return err
		}
	}
	attemptStatus := models.ExecutionAttemptRunning
	switch models.TaskStatus(status) {
	case models.TaskStatusWaitingHuman:
		attemptStatus = models.ExecutionAttemptWaitingHuman
	case models.TaskStatusDone:
		attemptStatus = models.ExecutionAttemptSucceeded
	case models.TaskStatusFailed:
		attemptStatus = models.ExecutionAttemptFailed
	case models.TaskStatusCancelled:
		attemptStatus = models.ExecutionAttemptCancelled
	case models.TaskStatusDeadLetter:
		attemptStatus = models.ExecutionAttemptDeadLetter
	}
	_, err = tx.ExecContext(ctx, `UPDATE execution_attempts
		SET status = ?, completed_at = CASE WHEN ? THEN ? ELSE completed_at END,
		    error_type = CASE WHEN ? THEN ? ELSE error_type END,
		    error_message = CASE WHEN ? THEN ? ELSE error_message END,
		    lease_until = CASE WHEN ? THEN NULL ELSE lease_until END
		WHERE id = (SELECT current_attempt_id FROM tasks WHERE id = ? AND workspace_id = ?)
		  AND workspace_id = ? AND worker_id = ?`, attemptStatus, terminal, now,
		terminal, errorType, terminal, message, releaseLease, taskID, workspace,
		workspace, workerID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ReleaseTask makes a task available again (e.g. lease expired).
func (s *Store) ReleaseTask(ctx context.Context, taskID string) error {
	now := time.Now().UTC()
	workspace := workspaceID(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE execution_attempts
		SET status = ?, completed_at = ?, error_type = 'LeaseExpired', error_message = 'task lease expired', lease_until = NULL
		WHERE id = (SELECT current_attempt_id FROM tasks WHERE id = ? AND workspace_id = ?)
		  AND workspace_id = ? AND status IN ('leased','running')`,
		models.ExecutionAttemptLeaseExpired, now, taskID, workspace, workspace); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE tasks SET status = 'pending', worker_id = NULL, lease_until = NULL, updated_at = ?, retry_count = retry_count + 1
		 WHERE id = ? AND workspace_id = ? AND status IN ('leased','running')`,
		now, taskID, workspace)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrLeaseConflict
	}
	return tx.Commit()
}

// ListExpiredLeases returns tasks with expired leases.
func (s *Store) ListExpiredLeases(ctx context.Context, before time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM tasks WHERE workspace_id = ? AND status IN ('leased','running') AND lease_until <= ?`, workspaceID(ctx), before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// InsertResult stores a result payload.
func (s *Store) InsertResult(ctx context.Context, r *models.Result) error {
	if err := s.ensureTaskInWorkspace(ctx, r.TaskID); err != nil {
		return err
	}
	r.WorkspaceID = workspaceID(ctx)
	if r.AttemptID == "" {
		var current sql.NullString
		if err := s.db.QueryRowContext(ctx, `SELECT current_attempt_id FROM tasks
			WHERE workspace_id = ? AND id = ?`, r.WorkspaceID, r.TaskID).Scan(&current); err != nil {
			return err
		}
		if current.Valid && current.String != "" {
			r.AttemptID = current.String
		} else {
			// Direct legacy submissions can predate a lease. Give them deterministic
			// attempt lineage so the versioned result view does not hide the row.
			r.AttemptID = "legacy-direct-" + r.TaskID
			startedAt := r.CreatedAt
			if startedAt.IsZero() {
				startedAt = time.Now().UTC()
			}
			_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO execution_attempts (
				id, workspace_id, task_id, attempt_number, worker_id, status,
				started_at, completed_at, error_type, error_message
			) VALUES (?, ?, ?, (SELECT COALESCE(MAX(attempt_number), 0) + 1 FROM execution_attempts
				WHERE workspace_id = ? AND task_id = ?), ?, ?, ?, ?, '', '')`,
				r.AttemptID, r.WorkspaceID, r.TaskID, r.WorkspaceID, r.TaskID,
				r.WorkerID, models.ExecutionAttemptLeaseExpired, startedAt, startedAt)
			if err != nil {
				return err
			}
		}
	}
	if r.IdempotencyKey == "" {
		r.IdempotencyKey = "legacy-" + r.ID
	}
	if r.Sequence <= 0 {
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) + 1 FROM results
			WHERE workspace_id = ? AND task_id = ? AND attempt_id = ? AND kind = 'batch'`,
			r.WorkspaceID, r.TaskID, r.AttemptID).Scan(&r.Sequence); err != nil {
			return err
		}
	}
	if r.Kind == "" {
		r.Kind = models.ResultKindBatch
	}
	r.Valid = true
	digest := sha256.Sum256(r.Payload)
	r.PayloadHash = fmt.Sprintf("%x", digest)
	const q = `INSERT INTO results (
		id, workspace_id, task_id, worker_id, attempt_id, idempotency_key,
		sequence, kind, payload, payload_hash, valid, validation_error, immediate, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, q, r.ID, r.WorkspaceID, r.TaskID, r.WorkerID,
		r.AttemptID, r.IdempotencyKey, r.Sequence, r.Kind, r.Payload, r.PayloadHash,
		r.Valid, r.ValidationError, r.Immediate, r.CreatedAt)
	return err
}

// InsertLog stores a log entry.
func (s *Store) InsertLog(ctx context.Context, l *models.LogEntry) error {
	if err := s.ensureTaskInWorkspace(ctx, l.TaskID); err != nil {
		return err
	}
	l.WorkspaceID = workspaceID(ctx)
	const q = `INSERT INTO logs (id, workspace_id, task_id, worker_id, level, message, extra, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, q, l.ID, l.WorkspaceID, l.TaskID, l.WorkerID, l.Level, l.Message, l.Extra, l.CreatedAt)
	return err
}

// InsertSnapshot stores a snapshot.
func (s *Store) InsertSnapshot(ctx context.Context, snap *models.Snapshot) error {
	if err := s.ensureTaskInWorkspace(ctx, snap.TaskID); err != nil {
		return err
	}
	snap.WorkspaceID = workspaceID(ctx)
	const q = `INSERT INTO snapshots (id, workspace_id, task_id, worker_id, name, type, data, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, q, snap.ID, snap.WorkspaceID, snap.TaskID, snap.WorkerID, snap.Name, snap.Type, snap.Data, snap.CreatedAt)
	return err
}

// InsertHeartbeat stores a heartbeat.
func (s *Store) InsertHeartbeat(ctx context.Context, h *models.Heartbeat) error {
	if err := s.ensureTaskInWorkspace(ctx, h.TaskID); err != nil {
		return err
	}
	h.WorkspaceID = workspaceID(ctx)
	const q = `INSERT INTO heartbeats (id, workspace_id, task_id, worker_id, payload, created_at) VALUES (?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, q, h.ID, h.WorkspaceID, h.TaskID, h.WorkerID, h.Payload, h.CreatedAt)
	return err
}

// InsertStatusUpdate stores a status update.
func (s *Store) InsertStatusUpdate(ctx context.Context, u *models.TaskStatusUpdate) error {
	if err := s.ensureTaskInWorkspace(ctx, u.TaskID); err != nil {
		return err
	}
	u.WorkspaceID = workspaceID(ctx)
	const q = `INSERT INTO task_status_updates (id, workspace_id, task_id, worker_id, status, message, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, q, u.ID, u.WorkspaceID, u.TaskID, u.WorkerID, u.Status, u.Message, u.CreatedAt)
	return err
}

// InsertCheckpoint stores a checkpoint.
func (s *Store) InsertCheckpoint(ctx context.Context, c *models.Checkpoint) error {
	if err := s.ensureTaskInWorkspace(ctx, c.TaskID); err != nil {
		return err
	}
	c.WorkspaceID = workspaceID(ctx)
	const q = `INSERT INTO checkpoints (id, workspace_id, task_id, worker_id, name, payload, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, q, c.ID, c.WorkspaceID, c.TaskID, c.WorkerID, c.Name, c.Payload, c.CreatedAt)
	return err
}

// GetLatestCheckpoint returns the most recent checkpoint for a task.
func (s *Store) GetLatestCheckpoint(ctx context.Context, taskID string) (*models.Checkpoint, error) {
	const q = `SELECT id, workspace_id, task_id, worker_id, name, payload, created_at FROM checkpoints WHERE task_id = ? AND workspace_id = ? ORDER BY created_at DESC LIMIT 1`
	row := s.db.QueryRowContext(ctx, q, taskID, workspaceID(ctx))
	c := &models.Checkpoint{}
	err := row.Scan(&c.ID, &c.WorkspaceID, &c.TaskID, &c.WorkerID, &c.Name, &c.Payload, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ListResults returns all result payloads for a task.
func (s *Store) ListResults(ctx context.Context, taskID string) ([]*models.Result, error) {
	// M-3: cap the result set to prevent OOM when a task has millions of
	// results. Callers needing pagination should add a dedicated handler.
	const q = `SELECT id, workspace_id, task_id, worker_id, attempt_id,
		idempotency_key, sequence, kind, payload, payload_hash, valid,
		validation_error, immediate, created_at
		FROM results WHERE task_id = ? AND workspace_id = ? ORDER BY created_at ASC LIMIT 1000`
	rows, err := s.db.QueryContext(ctx, q, taskID, workspaceID(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*models.Result
	for rows.Next() {
		r := &models.Result{}
		if err := rows.Scan(&r.ID, &r.WorkspaceID, &r.TaskID, &r.WorkerID,
			&r.AttemptID, &r.IdempotencyKey, &r.Sequence, &r.Kind, &r.Payload,
			&r.PayloadHash, &r.Valid, &r.ValidationError, &r.Immediate,
			&r.CreatedAt); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// ListLogs returns log entries for a task, newest first. Capped at 1000 rows
// (M-3) to prevent OOM on verbose tasks.
func (s *Store) ListLogs(ctx context.Context, taskID string) ([]*models.LogEntry, error) {
	const q = `SELECT id, workspace_id, task_id, worker_id, level, message, extra, created_at FROM logs WHERE task_id = ? AND workspace_id = ? ORDER BY created_at DESC LIMIT 1000`
	rows, err := s.db.QueryContext(ctx, q, taskID, workspaceID(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []*models.LogEntry
	for rows.Next() {
		l := &models.LogEntry{}
		if err := rows.Scan(&l.ID, &l.WorkspaceID, &l.TaskID, &l.WorkerID, &l.Level, &l.Message, &l.Extra, &l.CreatedAt); err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

// InsertAuditLog stores an audit log entry.
func (s *Store) InsertAuditLog(ctx context.Context, a *models.AuditLog) error {
	a.WorkspaceID = workspaceID(ctx)
	const q = `INSERT INTO audit_logs (id, workspace_id, actor, action, resource_type, resource_id, payload, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, q, a.ID, a.WorkspaceID, a.Actor, a.Action, a.ResourceType, a.ResourceID, a.Payload, a.CreatedAt)
	return err
}

// ListAuditLogs returns audit log entries matching the provided filter, plus the total matching count.
func (s *Store) ListAuditLogs(ctx context.Context, filter ListAuditLogsFilter) ([]*models.AuditLog, int, error) {
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}

	where := []string{"workspace_id = ?"}
	args := []any{workspaceID(ctx)}
	if filter.Actor != "" {
		where = append(where, "actor = ?")
		args = append(args, filter.Actor)
	}
	if filter.Action != "" {
		where = append(where, "action = ?")
		args = append(args, filter.Action)
	}
	if filter.ResourceType != "" {
		where = append(where, "resource_type = ?")
		args = append(args, filter.ResourceType)
	}
	if filter.ResourceID != "" {
		where = append(where, "resource_id = ?")
		args = append(args, filter.ResourceID)
	}
	if !filter.CreatedAfter.IsZero() {
		where = append(where, "created_at >= ?")
		args = append(args, filter.CreatedAfter.UTC())
	}
	if !filter.CreatedBefore.IsZero() {
		where = append(where, "created_at <= ?")
		args = append(args, filter.CreatedBefore.UTC())
	}

	whereClause := "WHERE " + strings.Join(where, " AND ")

	const selectCols = `id, workspace_id, actor, action, resource_type, resource_id, payload, created_at`

	var total int
	countQuery := "SELECT COUNT(*) FROM audit_logs " + whereClause
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count audit logs: %w", err)
	}

	query := fmt.Sprintf("SELECT %s FROM audit_logs %s ORDER BY created_at DESC LIMIT ? OFFSET ?", selectCols, whereClause)
	queryArgs := append(args, filter.Limit, filter.Offset)
	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list audit logs: %w", err)
	}
	defer rows.Close()

	var logs []*models.AuditLog
	for rows.Next() {
		a := &models.AuditLog{}
		if err := rows.Scan(&a.ID, &a.WorkspaceID, &a.Actor, &a.Action, &a.ResourceType, &a.ResourceID, &a.Payload, &a.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan audit log: %w", err)
		}
		logs = append(logs, a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate audit logs: %w", err)
	}
	return logs, total, nil
}

// CreateSchedule inserts a new schedule.
func (s *Store) CreateSchedule(ctx context.Context, sch *models.Schedule) error {
	sch.WorkspaceID = workspaceID(ctx)
	if sch.RuleVersionNumber > 0 {
		binding := &models.Task{RuleID: sch.RuleID, RuleVersion: sch.RuleVersion,
			RuleVersionNumber: sch.RuleVersionNumber, Variables: sch.Variables,
			BrowserProfileID: sch.BrowserProfileID}
		if err := s.BindTaskToRuleVersion(ctx, binding, sch.RuleVersionNumber); err != nil {
			return err
		}
		sch.RuleVersion = binding.RuleVersion
		sch.RuleVersionNumber = binding.RuleVersionNumber
		sch.Variables = binding.Variables
		sch.InputSchema = binding.InputSchema
		sch.BrowserProfileID = binding.BrowserProfileID
	} else {
		sch.RuleVersionNumber = 1
		if len(sch.InputSchema) == 0 {
			sch.InputSchema = models.JSON("{}")
		}
	}
	if sch.Timezone == "" {
		sch.Timezone = "UTC"
	}
	var ruleExists bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM rules WHERE id = ? AND workspace_id = ?)`,
		sch.RuleID, sch.WorkspaceID).Scan(&ruleExists); err != nil {
		return err
	}
	if !ruleExists {
		return ErrRuleNotFound
	}
	variables, err := s.encryptVariables(sch.Variables)
	if err != nil {
		return err
	}
	const q = `
		INSERT INTO schedules (id, workspace_id, rule_id, rule_version,
			rule_version_number, name, type, expression, enabled, next_run_at,
			last_run_at, variables, input_schema, browser_profile_id, timezone,
			priority, max_retries, catchup, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err = s.db.ExecContext(ctx, q,
		sch.ID, sch.WorkspaceID, sch.RuleID, sch.RuleVersion, sch.RuleVersionNumber,
		sch.Name, sch.Type, sch.Expression, sch.Enabled, sch.NextRunAt,
		sch.LastRunAt, variables, sch.InputSchema, sch.BrowserProfileID, sch.Timezone,
		sch.Priority, sch.MaxRetries, sch.Catchup, sch.CreatedAt, sch.UpdatedAt,
	)
	return err
}

// GetScheduleByID returns a schedule by id.
func (s *Store) GetScheduleByID(ctx context.Context, id string) (*models.Schedule, error) {
	const q = `SELECT id, workspace_id, rule_id, rule_version, rule_version_number,
		name, type, expression, enabled, next_run_at, last_run_at, variables,
		input_schema, browser_profile_id, timezone, priority, max_retries, catchup,
		created_at, updated_at FROM schedules WHERE id = ? AND workspace_id = ?`
	row := s.db.QueryRowContext(ctx, q, id, workspaceID(ctx))
	sch := &models.Schedule{}
	err := row.Scan(&sch.ID, &sch.WorkspaceID, &sch.RuleID, &sch.RuleVersion,
		&sch.RuleVersionNumber, &sch.Name, &sch.Type, &sch.Expression, &sch.Enabled,
		&sch.NextRunAt, &sch.LastRunAt, &sch.Variables, &sch.InputSchema,
		&sch.BrowserProfileID, &sch.Timezone, &sch.Priority, &sch.MaxRetries,
		&sch.Catchup, &sch.CreatedAt, &sch.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRuleNotFound
	}
	if err != nil {
		return nil, err
	}
	sch.Variables, err = s.decryptVariables(sch.Variables)
	if err != nil {
		return nil, err
	}
	return sch, nil
}

// UpdateSchedule updates an existing schedule.
func (s *Store) UpdateSchedule(ctx context.Context, sch *models.Schedule) error {
	sch.WorkspaceID = workspaceID(ctx)
	var ruleExists bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM rules WHERE id = ? AND workspace_id = ?)`,
		sch.RuleID, sch.WorkspaceID).Scan(&ruleExists); err != nil {
		return err
	}
	if !ruleExists {
		return ErrRuleNotFound
	}
	variables, err := s.encryptVariables(sch.Variables)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE schedules SET rule_id = ?, rule_version = ?, rule_version_number = ?,
		name = ?, type = ?, expression = ?, enabled = ?, next_run_at = ?,
		last_run_at = ?, variables = ?, input_schema = ?, browser_profile_id = ?,
		timezone = ?, priority = ?, max_retries = ?, catchup = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ?`,
		sch.RuleID, sch.RuleVersion, sch.RuleVersionNumber, sch.Name, sch.Type,
		sch.Expression, sch.Enabled, sch.NextRunAt, sch.LastRunAt, variables,
		sch.InputSchema, sch.BrowserProfileID, sch.Timezone, sch.Priority,
		sch.MaxRetries, sch.Catchup, sch.UpdatedAt, sch.ID, sch.WorkspaceID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrRuleNotFound
	}
	return nil
}

// DeleteSchedule deletes a schedule by id.
func (s *Store) DeleteSchedule(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM schedules WHERE id = ? AND workspace_id = ?`, id, workspaceID(ctx))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrRuleNotFound
	}
	return nil
}

// ListSchedules returns schedules matching the provided filter, plus the total matching count.
func (s *Store) ListSchedules(ctx context.Context, filter ListSchedulesFilter) ([]*models.Schedule, int, error) {
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}

	where := []string{"workspace_id = ?"}
	args := []any{workspaceID(ctx)}
	if filter.RuleID != "" {
		where = append(where, "rule_id = ?")
		args = append(args, filter.RuleID)
	}
	if filter.Type != "" {
		where = append(where, "type = ?")
		args = append(args, filter.Type)
	}
	if filter.Enabled != nil {
		where = append(where, "enabled = ?")
		args = append(args, *filter.Enabled)
	}

	whereClause := "WHERE " + strings.Join(where, " AND ")

	const selectCols = `id, workspace_id, rule_id, rule_version, rule_version_number,
		name, type, expression, enabled, next_run_at, last_run_at, variables,
		input_schema, browser_profile_id, timezone, priority, max_retries, catchup,
		created_at, updated_at`

	var total int
	countQuery := "SELECT COUNT(*) FROM schedules " + whereClause
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count schedules: %w", err)
	}

	query := fmt.Sprintf("SELECT %s FROM schedules %s ORDER BY created_at DESC LIMIT ? OFFSET ?", selectCols, whereClause)
	queryArgs := append(args, filter.Limit, filter.Offset)
	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list schedules: %w", err)
	}
	defer rows.Close()

	var schedules []*models.Schedule
	for rows.Next() {
		sch := &models.Schedule{}
		if err := rows.Scan(&sch.ID, &sch.WorkspaceID, &sch.RuleID, &sch.RuleVersion,
			&sch.RuleVersionNumber, &sch.Name, &sch.Type, &sch.Expression, &sch.Enabled,
			&sch.NextRunAt, &sch.LastRunAt, &sch.Variables, &sch.InputSchema,
			&sch.BrowserProfileID, &sch.Timezone, &sch.Priority, &sch.MaxRetries,
			&sch.Catchup, &sch.CreatedAt, &sch.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan schedule: %w", err)
		}
		sch.Variables, err = s.decryptVariables(sch.Variables)
		if err != nil {
			return nil, 0, fmt.Errorf("decrypt schedule variables: %w", err)
		}
		schedules = append(schedules, sch)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate schedules: %w", err)
	}
	return schedules, total, nil
}

// ListDueSchedules returns enabled schedules with NextRunAt <= before.
func (s *Store) ListDueSchedules(ctx context.Context, before time.Time) ([]*models.Schedule, error) {
	const q = `SELECT id, workspace_id, rule_id, rule_version, rule_version_number,
		name, type, expression, enabled, next_run_at, last_run_at, variables,
		input_schema, browser_profile_id, timezone, priority, max_retries, catchup,
		created_at, updated_at FROM schedules WHERE workspace_id = ? AND enabled = 1
		AND next_run_at IS NOT NULL AND next_run_at <= ? ORDER BY next_run_at ASC`
	rows, err := s.db.QueryContext(ctx, q, workspaceID(ctx), before.UTC())
	if err != nil {
		return nil, fmt.Errorf("list due schedules: %w", err)
	}
	defer rows.Close()

	var schedules []*models.Schedule
	for rows.Next() {
		sch := &models.Schedule{}
		if err := rows.Scan(&sch.ID, &sch.WorkspaceID, &sch.RuleID, &sch.RuleVersion,
			&sch.RuleVersionNumber, &sch.Name, &sch.Type, &sch.Expression, &sch.Enabled,
			&sch.NextRunAt, &sch.LastRunAt, &sch.Variables, &sch.InputSchema,
			&sch.BrowserProfileID, &sch.Timezone, &sch.Priority, &sch.MaxRetries,
			&sch.Catchup, &sch.CreatedAt, &sch.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan due schedule: %w", err)
		}
		sch.Variables, err = s.decryptVariables(sch.Variables)
		if err != nil {
			return nil, fmt.Errorf("decrypt due schedule variables: %w", err)
		}
		schedules = append(schedules, sch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due schedules: %w", err)
	}
	return schedules, nil
}

// NewID returns a unique ID.
func NewID() string {
	return uuid.NewString()
}

// JSON is a helper to marshal to models.JSON.
func JSON(v any) models.JSON {
	b, _ := json.Marshal(v)
	return models.JSON(b)
}

// PurgeReport maps entity name to rows deleted.
type PurgeReport map[string]int64

func (s *Store) purgeTable(ctx context.Context, table string, cutoff time.Time, batch int) (int64, error) {
	var total int64
	workspace := workspaceID(ctx)
	for {
		res, err := s.db.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM %s WHERE id IN (
				SELECT id FROM %s WHERE workspace_id = ? AND created_at < ? ORDER BY created_at LIMIT ?
			)`, table, table),
			workspace, cutoff, batch)
		if err != nil {
			return total, fmt.Errorf("purge %s: %w", table, err)
		}
		n, _ := res.RowsAffected()
		total += n
		if n == 0 {
			break
		}
	}
	return total, nil
}

// PurgeOldData deletes data older than the configured retention periods.
// Child tables are purged first by age. Completed tasks are purged in batches;
// for each batch of task IDs, remaining child rows for those task IDs are deleted
// before the tasks themselves to avoid foreign-key violations.
func (s *Store) PurgeOldData(ctx context.Context, cfg *config.Config) (PurgeReport, error) {
	now := time.Now().UTC()

	report := make(PurgeReport)
	expiredRecordings, err := s.DeleteExpiredRecordings(ctx, now, cfg.RetentionBatchSize)
	if err != nil {
		return report, fmt.Errorf("expire recordings: %w", err)
	}
	report["recordings"] = expiredRecordings

	childTables := []struct {
		name      string
		retention time.Duration
		reportKey string
	}{
		{"results", cfg.ResultRetention, "results"},
		{"logs", cfg.LogRetention, "logs"},
		{"snapshots", cfg.SnapshotRetention, "snapshots"},
		{"heartbeats", cfg.HeartbeatRetention, "heartbeats"},
		{"task_status_updates", cfg.StatusUpdateRetention, "status_updates"},
		{"checkpoints", cfg.CheckpointRetention, "checkpoints"},
	}

	for _, ct := range childTables {
		n, err := s.purgeTable(ctx, ct.name, now.Add(-ct.retention), cfg.RetentionBatchSize)
		if err != nil {
			return report, err
		}
		report[ct.reportKey] = n
	}

	batchSize := cfg.RetentionBatchSize
	if batchSize <= 0 {
		batchSize = 1000
	}

	const completedTaskQuery = `
		SELECT id FROM tasks
		WHERE workspace_id = ?
		  AND status IN ('done','failed','cancelled','dead_letter')
		  AND completed_at < ?
		ORDER BY completed_at
		LIMIT ?
	`

	cutoff := now.Add(-cfg.CompletedTaskRetention)
	childTablesForTask := []string{"results", "logs", "snapshots", "heartbeats", "task_status_updates", "human_interventions", "checkpoints", "execution_attempts"}

	for {
		workspace := workspaceID(ctx)
		rows, err := s.db.QueryContext(ctx, completedTaskQuery, workspace, cutoff, batchSize)
		if err != nil {
			return report, fmt.Errorf("select completed tasks: %w", err)
		}

		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return report, fmt.Errorf("scan completed task id: %w", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return report, fmt.Errorf("iterate completed task ids: %w", err)
		}
		_ = rows.Close()

		if len(ids) == 0 {
			break
		}

		placeholders := strings.Repeat("?,", len(ids))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, 0, len(ids)+1)
		args = append(args, workspace)
		for _, id := range ids {
			args = append(args, id)
		}

		for _, table := range childTablesForTask {
			q := fmt.Sprintf("DELETE FROM %s WHERE workspace_id = ? AND task_id IN (%s)", table, placeholders)
			if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
				return report, fmt.Errorf("purge %s for completed tasks: %w", table, err)
			}
		}

		taskQuery := fmt.Sprintf("DELETE FROM tasks WHERE workspace_id = ? AND id IN (%s)", placeholders)
		res, err := s.db.ExecContext(ctx, taskQuery, args...)
		if err != nil {
			return report, fmt.Errorf("purge completed tasks: %w", err)
		}
		n, _ := res.RowsAffected()
		report["completed_tasks"] += n
	}

	return report, nil
}

const migration008Schema = `
CREATE TABLE IF NOT EXISTS llm_jobs (
    id TEXT PRIMARY KEY,
    rule_id TEXT NOT NULL,
    baseline TEXT NOT NULL,
    recording TEXT NOT NULL,
    user_hint TEXT NOT NULL,
    status TEXT NOT NULL,
    result_rule TEXT NOT NULL DEFAULT '{}',
    result_patch TEXT NOT NULL DEFAULT '{}',
    result_error TEXT NOT NULL DEFAULT '',
    provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    safety_flags TEXT NOT NULL DEFAULT '[]',
    suggestions TEXT NOT NULL DEFAULT '[]',
    created_at DATETIME NOT NULL,
    started_at DATETIME,
    completed_at DATETIME,
    attempt_count INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_llm_jobs_rule_id ON llm_jobs(rule_id);
CREATE INDEX IF NOT EXISTS idx_llm_jobs_status_created ON llm_jobs(status, created_at);
`

func migration008(tx *sql.Tx) error {
	_, err := tx.Exec(migration008Schema)
	return err
}

const migration009Schema = `
CREATE TABLE IF NOT EXISTS llm_cache (
    cache_key TEXT PRIMARY KEY,
    content TEXT NOT NULL,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    expires_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_llm_cache_expires ON llm_cache(expires_at);
`

func migration009(tx *sql.Tx) error {
	_, err := tx.Exec(migration009Schema)
	return err
}

func migration010(tx *sql.Tx) error {
	var colCount int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('rules') WHERE name = 'source'`).Scan(&colCount); err != nil {
		return err
	}
	if colCount == 0 {
		if _, err := tx.Exec(`ALTER TABLE rules ADD COLUMN source TEXT NOT NULL DEFAULT 'pageagent'`); err != nil {
			return fmt.Errorf("add source column: %w", err)
		}
	}
	return nil
}

func migration011(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS workspaces (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		INSERT OR IGNORE INTO workspaces (id, name) VALUES ('default', 'default');
	`); err != nil {
		return fmt.Errorf("create default workspace: %w", err)
	}
	// Repair legacy fixtures/databases that recorded migration 004 without the
	// corresponding table before adding workspace scope.
	if _, err := tx.Exec(migration004Schema); err != nil {
		return fmt.Errorf("ensure audit log table: %w", err)
	}

	tables := []string{
		"rules", "tasks", "schedules", "results", "logs", "snapshots",
		"heartbeats", "task_status_updates", "checkpoints", "audit_logs",
		"rule_enhancements", "llm_jobs",
	}
	for _, table := range tables {
		if err := addWorkspaceScope(tx, table); err != nil {
			return err
		}
	}

	if err := rebuildWorkspaceCache(tx); err != nil {
		return err
	}
	if err := rebuildWorkspaceSchedulerLocks(tx); err != nil {
		return err
	}

	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_rules_workspace_created ON rules(workspace_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_workspace_status ON tasks(workspace_id, status, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_workspace_rule ON tasks(workspace_id, rule_id)`,
		`CREATE INDEX IF NOT EXISTS idx_schedules_workspace_due ON schedules(workspace_id, enabled, next_run_at)`,
		`CREATE INDEX IF NOT EXISTS idx_schedules_workspace_rule ON schedules(workspace_id, rule_id)`,
		`CREATE INDEX IF NOT EXISTS idx_results_workspace_task ON results(workspace_id, task_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_workspace_task ON logs(workspace_id, task_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_snapshots_workspace_task ON snapshots(workspace_id, task_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_heartbeats_workspace_task ON heartbeats(workspace_id, task_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_status_workspace_task ON task_status_updates(workspace_id, task_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_checkpoints_workspace_task ON checkpoints(workspace_id, task_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_workspace_created ON audit_logs(workspace_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_enhancements_workspace_rule ON rule_enhancements(workspace_id, rule_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_llm_jobs_workspace_status ON llm_jobs(workspace_id, status, created_at)`,
	}
	for _, statement := range indexes {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("create workspace index: %w", err)
		}
	}
	return nil
}

func addWorkspaceScope(tx *sql.Tx, table string) error {
	var tableExists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&tableExists); err != nil {
		return fmt.Errorf("inspect %s table: %w", table, err)
	}
	if tableExists == 0 {
		return nil
	}
	var count int
	if err := tx.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name = 'workspace_id'", table)).Scan(&count); err != nil {
		return fmt.Errorf("inspect %s workspace column: %w", table, err)
	}
	if count == 0 {
		if _, err := tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN workspace_id TEXT NOT NULL DEFAULT 'default'", table)); err != nil {
			return fmt.Errorf("add %s workspace column: %w", table, err)
		}
	}

	insertTrigger := fmt.Sprintf(`
		CREATE TRIGGER IF NOT EXISTS trg_%s_workspace_insert
		BEFORE INSERT ON %s
		WHEN NOT EXISTS (SELECT 1 FROM workspaces WHERE id = NEW.workspace_id)
		BEGIN SELECT RAISE(ABORT, 'workspace not found'); END`, table, table)
	if _, err := tx.Exec(insertTrigger); err != nil {
		return fmt.Errorf("create %s workspace insert trigger: %w", table, err)
	}
	updateTrigger := fmt.Sprintf(`
		CREATE TRIGGER IF NOT EXISTS trg_%s_workspace_update
		BEFORE UPDATE OF workspace_id ON %s
		WHEN NOT EXISTS (SELECT 1 FROM workspaces WHERE id = NEW.workspace_id)
		BEGIN SELECT RAISE(ABORT, 'workspace not found'); END`, table, table)
	if _, err := tx.Exec(updateTrigger); err != nil {
		return fmt.Errorf("create %s workspace update trigger: %w", table, err)
	}
	return nil
}

func rebuildWorkspaceCache(tx *sql.Tx) error {
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('llm_cache') WHERE name = 'workspace_id'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if _, err := tx.Exec(`
			CREATE TABLE llm_cache_scoped (
				workspace_id TEXT NOT NULL DEFAULT 'default' REFERENCES workspaces(id),
				cache_key TEXT NOT NULL,
				content TEXT NOT NULL,
				input_tokens INTEGER NOT NULL DEFAULT 0,
				output_tokens INTEGER NOT NULL DEFAULT 0,
				expires_at DATETIME NOT NULL,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				PRIMARY KEY (workspace_id, cache_key)
			);
			INSERT INTO llm_cache_scoped (workspace_id, cache_key, content, input_tokens, output_tokens, expires_at, created_at)
			SELECT 'default', cache_key, content, input_tokens, output_tokens, expires_at, created_at FROM llm_cache;
			DROP TABLE llm_cache;
			ALTER TABLE llm_cache_scoped RENAME TO llm_cache;
		`); err != nil {
			return fmt.Errorf("scope llm cache: %w", err)
		}
	}
	_, err := tx.Exec(`
		CREATE INDEX IF NOT EXISTS idx_llm_cache_expires ON llm_cache(expires_at);
		CREATE INDEX IF NOT EXISTS idx_llm_cache_workspace_expires ON llm_cache(workspace_id, expires_at);
	`)
	return err
}

func rebuildWorkspaceSchedulerLocks(tx *sql.Tx) error {
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('scheduler_locks') WHERE name = 'workspace_id'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if _, err := tx.Exec(`
			CREATE TABLE scheduler_locks_scoped (
				workspace_id TEXT NOT NULL DEFAULT 'default' REFERENCES workspaces(id),
				name TEXT NOT NULL,
				owner TEXT,
				expires_at DATETIME,
				acquired_at DATETIME,
				PRIMARY KEY (workspace_id, name)
			);
			INSERT INTO scheduler_locks_scoped (workspace_id, name, owner, expires_at, acquired_at)
			SELECT 'default', name, owner, expires_at, acquired_at FROM scheduler_locks;
			DROP TABLE scheduler_locks;
			ALTER TABLE scheduler_locks_scoped RENAME TO scheduler_locks;
		`); err != nil {
			return fmt.Errorf("scope scheduler locks: %w", err)
		}
	}
	return nil
}

const migration012Schema = `
CREATE TABLE IF NOT EXISTS recordings (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    owner TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('captured','deleted')),
    protocol_version TEXT NOT NULL,
    sanitization_version TEXT NOT NULL,
    redaction_count INTEGER NOT NULL DEFAULT 0,
    removed_field_count INTEGER NOT NULL DEFAULT 0,
    action_count INTEGER NOT NULL DEFAULT 0,
    snapshot_count INTEGER NOT NULL DEFAULT 0,
    compressed_bytes INTEGER NOT NULL DEFAULT 0,
    content_hash TEXT NOT NULL,
    cipher_version TEXT NOT NULL DEFAULT 'aegis1',
    compression TEXT NOT NULL DEFAULT 'gzip',
    encrypted_payload BLOB,
    started_at DATETIME NOT NULL,
    ended_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    expires_at DATETIME NOT NULL,
    deleted_at DATETIME
);
CREATE INDEX IF NOT EXISTS idx_recordings_workspace_created ON recordings(workspace_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_recordings_workspace_expires ON recordings(workspace_id, status, expires_at);
`

func migration012(tx *sql.Tx) error {
	_, err := tx.Exec(migration012Schema)
	return err
}

const migration013Schema = `
CREATE TABLE IF NOT EXISTS rule_versions (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    rule_id TEXT NOT NULL,
    version_number INTEGER NOT NULL CHECK (version_number > 0),
    version_label TEXT NOT NULL,
    rule_json TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    approval_status TEXT NOT NULL CHECK (approval_status IN ('pending','approved','rejected')),
    owner TEXT NOT NULL,
    source TEXT NOT NULL,
    recording_id TEXT,
    created_at DATETIME NOT NULL,
    approved_at DATETIME,
    approved_by TEXT,
    rejected_at DATETIME,
    rejected_by TEXT,
    PRIMARY KEY (workspace_id, rule_id, version_number),
    UNIQUE (workspace_id, rule_id, content_hash)
);
CREATE INDEX IF NOT EXISTS idx_rule_versions_workspace_rule ON rule_versions(workspace_id, rule_id, version_number DESC);
CREATE INDEX IF NOT EXISTS idx_rule_versions_workspace_status ON rule_versions(workspace_id, approval_status, created_at DESC);

CREATE TRIGGER IF NOT EXISTS trg_rule_versions_parent_insert
BEFORE INSERT ON rule_versions
WHEN NOT EXISTS (
    SELECT 1 FROM rules
    WHERE workspace_id = NEW.workspace_id AND id = NEW.rule_id
)
BEGIN SELECT RAISE(ABORT, 'rule not found in workspace'); END;

CREATE TRIGGER IF NOT EXISTS trg_rule_versions_recording_insert
BEFORE INSERT ON rule_versions
WHEN NEW.recording_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM recordings
    WHERE workspace_id = NEW.workspace_id AND id = NEW.recording_id AND status != 'deleted'
)
BEGIN SELECT RAISE(ABORT, 'recording not found in workspace'); END;

CREATE TRIGGER IF NOT EXISTS trg_rule_versions_immutable_content
BEFORE UPDATE ON rule_versions
WHEN NEW.workspace_id IS NOT OLD.workspace_id
  OR NEW.rule_id IS NOT OLD.rule_id
  OR NEW.version_number IS NOT OLD.version_number
  OR NEW.version_label IS NOT OLD.version_label
  OR NEW.rule_json IS NOT OLD.rule_json
  OR NEW.content_hash IS NOT OLD.content_hash
  OR NEW.owner IS NOT OLD.owner
  OR NEW.source IS NOT OLD.source
  OR NEW.recording_id IS NOT OLD.recording_id
  OR NEW.created_at IS NOT OLD.created_at
BEGIN SELECT RAISE(ABORT, 'rule version content is immutable'); END;

CREATE TRIGGER IF NOT EXISTS trg_rule_versions_state_transition
BEFORE UPDATE ON rule_versions
WHEN OLD.approval_status != 'pending'
  OR NEW.approval_status NOT IN ('approved','rejected')
  OR NEW.approval_status = OLD.approval_status
BEGIN SELECT RAISE(ABORT, 'invalid rule version state transition'); END;

CREATE TRIGGER IF NOT EXISTS trg_rule_versions_no_delete
BEFORE DELETE ON rule_versions
BEGIN SELECT RAISE(ABORT, 'rule versions are immutable'); END;
`

func migration013(tx *sql.Tx) error {
	// Repair databases whose migration ledger was populated manually without
	// applying migration 002 (some historical fixtures and installations did
	// this) before reading approval state for the backfill.
	if err := migration002(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(migration013Schema); err != nil {
		return err
	}

	rows, err := tx.Query(`
		SELECT id, workspace_id, version, name, domain, COALESCE(url_pattern, 'null'), enabled, priority, COALESCE(entry, ''),
		       COALESCE(variables, '{}'), COALESCE(selectors, '{}'), COALESCE(humanize, '{}'), steps,
		       COALESCE(output, '{}'), COALESCE(send_policy, '{}'), COALESCE(hooks, '{}'), COALESCE(tags, '{}'),
		       COALESCE(owner, ''), approval_status, source, created_at, updated_at
		FROM rules
		WHERE NOT EXISTS (
			SELECT 1 FROM rule_versions rv
			WHERE rv.workspace_id = rules.workspace_id AND rv.rule_id = rules.id
		)
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type backfill struct {
		rule       models.Rule
		ruleJSON   []byte
		hash       string
		approved   any
		approvedBy string
	}
	var pending []backfill
	for rows.Next() {
		rule := models.Rule{}
		if err := rows.Scan(
			&rule.ID, &rule.WorkspaceID, &rule.Version, &rule.Name, &rule.Domain, &rule.URLPattern,
			&rule.Enabled, &rule.Priority, &rule.Entry, &rule.Variables, &rule.Selectors,
			&rule.Humanize, &rule.Steps, &rule.Output, &rule.SendPolicy, &rule.Hooks,
			&rule.Tags, &rule.Owner, &rule.ApprovalStatus, &rule.Source, &rule.CreatedAt, &rule.UpdatedAt,
		); err != nil {
			return err
		}
		data, err := json.Marshal(&rule)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		approved := any(nil)
		approvedBy := ""
		if rule.ApprovalStatus == string(models.RuleApprovalApproved) {
			approved = rule.UpdatedAt
			approvedBy = rule.Owner
			if approvedBy == "" {
				approvedBy = "migration"
			}
		}
		pending = append(pending, backfill{rule: rule, ruleJSON: data, hash: fmt.Sprintf("%x", digest), approved: approved, approvedBy: approvedBy})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, item := range pending {
		if _, err := tx.Exec(`
			INSERT INTO rule_versions (
				workspace_id, rule_id, version_number, version_label, rule_json, content_hash,
				approval_status, owner, source, created_at, approved_at, approved_by
			) VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, item.rule.WorkspaceID, item.rule.ID, item.rule.Version, item.ruleJSON, item.hash,
			item.rule.ApprovalStatus, item.rule.Owner, item.rule.Source, item.rule.CreatedAt,
			item.approved, item.approvedBy); err != nil {
			return err
		}
	}
	return nil
}

const migration014Schema = `
CREATE TABLE IF NOT EXISTS requirement_jobs (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    recording_id TEXT NOT NULL REFERENCES recordings(id),
    kind TEXT NOT NULL CHECK (kind IN ('candidates','normalize')),
    status TEXT NOT NULL CHECK (status IN ('pending','running','completed','failed')),
    source TEXT NOT NULL CHECK (source IN ('llm','manual')),
    request_artifact BLOB NOT NULL,
    result_artifact BLOB,
    request_hash TEXT NOT NULL,
    result_hash TEXT,
    provider TEXT,
    model TEXT,
    prompt_version TEXT NOT NULL,
    chunk_count INTEGER NOT NULL DEFAULT 0,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts > 0),
    available_at DATETIME NOT NULL,
    lease_until DATETIME,
    error_code TEXT,
    error_message TEXT,
    safety_flags TEXT NOT NULL DEFAULT '[]',
    requirement_id TEXT,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    started_at DATETIME,
    completed_at DATETIME
);
CREATE INDEX IF NOT EXISTS idx_requirement_jobs_workspace_created ON requirement_jobs(workspace_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_requirement_jobs_claim ON requirement_jobs(status, available_at, created_at);
CREATE INDEX IF NOT EXISTS idx_requirement_jobs_recording ON requirement_jobs(workspace_id, recording_id, created_at DESC);

CREATE TABLE IF NOT EXISTS collection_requirements (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    recording_id TEXT NOT NULL REFERENCES recordings(id),
    source_job_id TEXT REFERENCES requirement_jobs(id),
    source TEXT NOT NULL CHECK (source IN ('llm','manual')),
    status TEXT NOT NULL CHECK (status IN ('draft','confirmed')),
    content_artifact BLOB NOT NULL,
    content_hash TEXT NOT NULL,
    owner TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    confirmed_at DATETIME,
    confirmed_by TEXT
);
CREATE INDEX IF NOT EXISTS idx_collection_requirements_workspace_created ON collection_requirements(workspace_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_collection_requirements_recording ON collection_requirements(workspace_id, recording_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_collection_requirements_source_job ON collection_requirements(source_job_id) WHERE source_job_id IS NOT NULL;

CREATE TRIGGER IF NOT EXISTS trg_requirement_jobs_recording_insert
BEFORE INSERT ON requirement_jobs
WHEN NOT EXISTS (
    SELECT 1 FROM recordings
    WHERE workspace_id = NEW.workspace_id AND id = NEW.recording_id AND status != 'deleted'
)
BEGIN SELECT RAISE(ABORT, 'recording not found in workspace'); END;

CREATE TRIGGER IF NOT EXISTS trg_collection_requirements_recording_insert
BEFORE INSERT ON collection_requirements
WHEN NOT EXISTS (
    SELECT 1 FROM recordings
    WHERE workspace_id = NEW.workspace_id AND id = NEW.recording_id AND status != 'deleted'
)
BEGIN SELECT RAISE(ABORT, 'recording not found in workspace'); END;

CREATE TRIGGER IF NOT EXISTS trg_collection_requirements_job_insert
BEFORE INSERT ON collection_requirements
WHEN NEW.source_job_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM requirement_jobs
    WHERE workspace_id = NEW.workspace_id AND id = NEW.source_job_id AND recording_id = NEW.recording_id
)
BEGIN SELECT RAISE(ABORT, 'requirement job not found in workspace'); END;

CREATE TRIGGER IF NOT EXISTS trg_collection_requirements_immutable_content
BEFORE UPDATE ON collection_requirements
WHEN NEW.workspace_id IS NOT OLD.workspace_id
  OR NEW.recording_id IS NOT OLD.recording_id
  OR NEW.source_job_id IS NOT OLD.source_job_id
  OR NEW.source IS NOT OLD.source
  OR NEW.content_artifact IS NOT OLD.content_artifact
  OR NEW.content_hash IS NOT OLD.content_hash
  OR NEW.owner IS NOT OLD.owner
  OR NEW.created_at IS NOT OLD.created_at
BEGIN SELECT RAISE(ABORT, 'requirement content is immutable'); END;

CREATE TRIGGER IF NOT EXISTS trg_collection_requirements_state_transition
BEFORE UPDATE OF status ON collection_requirements
WHEN NEW.status != OLD.status
 AND NOT (OLD.status = 'draft' AND NEW.status = 'confirmed')
BEGIN SELECT RAISE(ABORT, 'invalid requirement state transition'); END;
`

func migration014(tx *sql.Tx) error {
	_, err := tx.Exec(migration014Schema)
	return err
}

const migration015Schema = `
CREATE TABLE IF NOT EXISTS dsl_workflows (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    requirement_id TEXT NOT NULL REFERENCES collection_requirements(id),
    recording_id TEXT NOT NULL REFERENCES recordings(id),
    status TEXT NOT NULL CHECK (status IN (
        'generating','awaiting_replay','replaying','repairing',
        'awaiting_confirmation','failed','approved'
    )),
    browser_profile_id TEXT NOT NULL,
    current_job_id TEXT,
    repair_count INTEGER NOT NULL DEFAULT 0 CHECK (repair_count >= 0),
    max_repairs INTEGER NOT NULL DEFAULT 3 CHECK (max_repairs BETWEEN 0 AND 3),
    provisional_artifact BLOB,
    provisional_hash TEXT,
    last_replay_sequence INTEGER NOT NULL DEFAULT 0 CHECK (last_replay_sequence >= 0),
    error_code TEXT,
    error_message TEXT,
    owner TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    approved_at DATETIME
);
CREATE INDEX IF NOT EXISTS idx_dsl_workflows_workspace_created ON dsl_workflows(workspace_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_dsl_workflows_requirement ON dsl_workflows(workspace_id, requirement_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_dsl_workflows_status ON dsl_workflows(workspace_id, status, updated_at);

CREATE TABLE IF NOT EXISTS dsl_jobs (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    workflow_id TEXT NOT NULL REFERENCES dsl_workflows(id),
    kind TEXT NOT NULL CHECK (kind IN ('generate','repair')),
    status TEXT NOT NULL CHECK (status IN ('pending','running','completed','failed')),
    request_artifact BLOB NOT NULL,
    result_artifact BLOB,
    request_hash TEXT NOT NULL,
    result_hash TEXT,
    provider TEXT,
    model TEXT,
    prompt_version TEXT NOT NULL,
    chunk_count INTEGER NOT NULL DEFAULT 0,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts > 0),
    available_at DATETIME NOT NULL,
    lease_until DATETIME,
    error_code TEXT,
    error_message TEXT,
    safety_flags TEXT NOT NULL DEFAULT '[]',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    started_at DATETIME,
    completed_at DATETIME
);
CREATE INDEX IF NOT EXISTS idx_dsl_jobs_claim ON dsl_jobs(status, available_at, created_at);
CREATE INDEX IF NOT EXISTS idx_dsl_jobs_workflow ON dsl_jobs(workspace_id, workflow_id, created_at DESC);

CREATE TABLE IF NOT EXISTS dsl_replay_attempts (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    workflow_id TEXT NOT NULL REFERENCES dsl_workflows(id),
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    status TEXT NOT NULL CHECK (status IN ('running','succeeded','failed')),
    rule_hash TEXT NOT NULL,
    browser_profile_id TEXT NOT NULL,
    output_valid BOOLEAN NOT NULL DEFAULT 0,
    diagnostics_artifact BLOB,
    diagnostics_hash TEXT,
    output_artifact BLOB,
    output_hash TEXT,
    artifacts_artifact BLOB,
    artifacts_hash TEXT,
    error_code TEXT,
    error_message TEXT,
    started_at DATETIME NOT NULL,
    completed_at DATETIME,
    UNIQUE (workspace_id, workflow_id, sequence)
);
CREATE INDEX IF NOT EXISTS idx_dsl_replay_attempts_workflow ON dsl_replay_attempts(workspace_id, workflow_id, sequence DESC);

CREATE TABLE IF NOT EXISTS dsl_approvals (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    workflow_id TEXT NOT NULL UNIQUE,
    requirement_id TEXT NOT NULL,
    requirement_hash TEXT NOT NULL,
    recording_id TEXT NOT NULL REFERENCES recordings(id),
    recording_hash TEXT NOT NULL,
    rule_id TEXT NOT NULL,
    version_number INTEGER NOT NULL,
    created_at DATETIME NOT NULL,
    PRIMARY KEY (workspace_id, workflow_id),
    FOREIGN KEY (workspace_id, rule_id, version_number)
        REFERENCES rule_versions(workspace_id, rule_id, version_number)
);

CREATE TRIGGER IF NOT EXISTS trg_dsl_workflows_requirement_insert
BEFORE INSERT ON dsl_workflows
WHEN NOT EXISTS (
    SELECT 1 FROM collection_requirements
    WHERE workspace_id = NEW.workspace_id
      AND id = NEW.requirement_id
      AND recording_id = NEW.recording_id
      AND status = 'confirmed'
)
BEGIN SELECT RAISE(ABORT, 'confirmed requirement not found in workspace'); END;

CREATE TRIGGER IF NOT EXISTS trg_dsl_workflows_immutable_identity
BEFORE UPDATE ON dsl_workflows
WHEN NEW.workspace_id IS NOT OLD.workspace_id
  OR NEW.requirement_id IS NOT OLD.requirement_id
  OR NEW.recording_id IS NOT OLD.recording_id
  OR NEW.browser_profile_id IS NOT OLD.browser_profile_id
  OR NEW.max_repairs IS NOT OLD.max_repairs
  OR NEW.owner IS NOT OLD.owner
  OR NEW.created_at IS NOT OLD.created_at
BEGIN SELECT RAISE(ABORT, 'dsl workflow identity is immutable'); END;

CREATE TRIGGER IF NOT EXISTS trg_dsl_workflows_state_transition
BEFORE UPDATE OF status ON dsl_workflows
WHEN NEW.status != OLD.status AND NOT (
       (OLD.status = 'generating' AND NEW.status IN ('awaiting_replay','failed'))
    OR (OLD.status = 'awaiting_replay' AND NEW.status IN ('replaying','failed'))
    OR (OLD.status = 'replaying' AND NEW.status IN ('awaiting_confirmation','repairing','failed'))
    OR (OLD.status = 'repairing' AND NEW.status IN ('awaiting_replay','failed'))
    OR (OLD.status = 'awaiting_confirmation' AND NEW.status IN ('replaying','approved','failed'))
)
BEGIN SELECT RAISE(ABORT, 'invalid dsl workflow state transition'); END;

CREATE TRIGGER IF NOT EXISTS trg_dsl_workflows_terminal_immutable
BEFORE UPDATE ON dsl_workflows
WHEN OLD.status IN ('failed','approved')
BEGIN SELECT RAISE(ABORT, 'terminal dsl workflow is immutable'); END;

CREATE TRIGGER IF NOT EXISTS trg_dsl_jobs_workflow_insert
BEFORE INSERT ON dsl_jobs
WHEN NOT EXISTS (
    SELECT 1 FROM dsl_workflows
    WHERE workspace_id = NEW.workspace_id AND id = NEW.workflow_id
)
BEGIN SELECT RAISE(ABORT, 'dsl workflow not found in workspace'); END;

CREATE TRIGGER IF NOT EXISTS trg_dsl_jobs_immutable_identity
BEFORE UPDATE ON dsl_jobs
WHEN NEW.workspace_id IS NOT OLD.workspace_id
  OR NEW.workflow_id IS NOT OLD.workflow_id
  OR NEW.kind IS NOT OLD.kind
  OR NEW.request_artifact IS NOT OLD.request_artifact
  OR NEW.request_hash IS NOT OLD.request_hash
  OR NEW.max_attempts IS NOT OLD.max_attempts
  OR NEW.created_at IS NOT OLD.created_at
BEGIN SELECT RAISE(ABORT, 'dsl job identity is immutable'); END;

CREATE TRIGGER IF NOT EXISTS trg_dsl_jobs_state_transition
BEFORE UPDATE OF status ON dsl_jobs
WHEN NOT (
       (OLD.status = 'pending' AND NEW.status = 'running')
    OR (OLD.status = 'running' AND NEW.status IN ('running','pending','completed','failed'))
)
BEGIN SELECT RAISE(ABORT, 'invalid dsl job state transition'); END;

CREATE TRIGGER IF NOT EXISTS trg_dsl_jobs_terminal_immutable
BEFORE UPDATE ON dsl_jobs
WHEN OLD.status IN ('completed','failed')
BEGIN SELECT RAISE(ABORT, 'terminal dsl job is immutable'); END;

CREATE TRIGGER IF NOT EXISTS trg_dsl_replay_attempts_workflow_insert
BEFORE INSERT ON dsl_replay_attempts
WHEN NOT EXISTS (
    SELECT 1 FROM dsl_workflows
    WHERE workspace_id = NEW.workspace_id
      AND id = NEW.workflow_id
      AND browser_profile_id = NEW.browser_profile_id
      AND provisional_hash = NEW.rule_hash
)
BEGIN SELECT RAISE(ABORT, 'dsl replay workflow mismatch'); END;

CREATE TRIGGER IF NOT EXISTS trg_dsl_replay_attempts_immutable_identity
BEFORE UPDATE ON dsl_replay_attempts
WHEN NEW.workspace_id IS NOT OLD.workspace_id
  OR NEW.workflow_id IS NOT OLD.workflow_id
  OR NEW.sequence IS NOT OLD.sequence
  OR NEW.rule_hash IS NOT OLD.rule_hash
  OR NEW.browser_profile_id IS NOT OLD.browser_profile_id
  OR NEW.started_at IS NOT OLD.started_at
BEGIN SELECT RAISE(ABORT, 'dsl replay identity is immutable'); END;

CREATE TRIGGER IF NOT EXISTS trg_dsl_replay_attempts_state_transition
BEFORE UPDATE OF status ON dsl_replay_attempts
WHEN OLD.status != 'running'
  OR NEW.status NOT IN ('succeeded','failed')
BEGIN SELECT RAISE(ABORT, 'invalid dsl replay state transition'); END;

CREATE TRIGGER IF NOT EXISTS trg_dsl_replay_attempts_terminal_immutable
BEFORE UPDATE ON dsl_replay_attempts
WHEN OLD.status IN ('succeeded','failed')
BEGIN SELECT RAISE(ABORT, 'terminal dsl replay is immutable'); END;

CREATE TRIGGER IF NOT EXISTS trg_dsl_approvals_workflow_insert
BEFORE INSERT ON dsl_approvals
WHEN NOT EXISTS (
    SELECT 1 FROM dsl_workflows
    WHERE workspace_id = NEW.workspace_id
      AND id = NEW.workflow_id
      AND status = 'approved'
      AND requirement_id = NEW.requirement_id
      AND recording_id = NEW.recording_id
)
BEGIN SELECT RAISE(ABORT, 'approved dsl workflow not found in workspace'); END;

CREATE TRIGGER IF NOT EXISTS trg_dsl_approvals_immutable_update
BEFORE UPDATE ON dsl_approvals
BEGIN SELECT RAISE(ABORT, 'dsl approval lineage is immutable'); END;

CREATE TRIGGER IF NOT EXISTS trg_dsl_approvals_immutable_delete
BEFORE DELETE ON dsl_approvals
BEGIN SELECT RAISE(ABORT, 'dsl approval lineage is immutable'); END;
`

func migration015(tx *sql.Tx) error {
	_, err := tx.Exec(migration015Schema)
	return err
}

const migration016Schema = `
CREATE TABLE IF NOT EXISTS rule_version_contracts (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    rule_id TEXT NOT NULL,
    version_number INTEGER NOT NULL CHECK (version_number > 0),
    input_schema TEXT NOT NULL,
    output_schema TEXT NOT NULL,
    browser_profile_id TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    PRIMARY KEY (workspace_id, rule_id, version_number)
);

CREATE TRIGGER IF NOT EXISTS trg_rule_version_contracts_parent
BEFORE INSERT ON rule_version_contracts
WHEN NOT EXISTS (
    SELECT 1 FROM rule_versions
    WHERE workspace_id = NEW.workspace_id AND rule_id = NEW.rule_id
      AND version_number = NEW.version_number
)
BEGIN SELECT RAISE(ABORT, 'rule version not found in workspace'); END;

CREATE TRIGGER IF NOT EXISTS trg_rule_version_contracts_immutable
BEFORE UPDATE ON rule_version_contracts
BEGIN SELECT RAISE(ABORT, 'rule version contract is immutable'); END;

CREATE TRIGGER IF NOT EXISTS trg_rule_version_contracts_no_delete
BEFORE DELETE ON rule_version_contracts
BEGIN SELECT RAISE(ABORT, 'rule version contract is immutable'); END;

CREATE TABLE IF NOT EXISTS execution_attempts (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    task_id TEXT NOT NULL REFERENCES tasks(id),
    attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
    worker_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('leased','running','waiting_for_human','succeeded','failed','cancelled','lease_expired','dead_letter')),
    lease_until DATETIME,
    started_at DATETIME NOT NULL,
    completed_at DATETIME,
    error_type TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    UNIQUE (workspace_id, task_id, attempt_number)
);
CREATE INDEX IF NOT EXISTS idx_execution_attempts_task ON execution_attempts(workspace_id, task_id, attempt_number DESC);
CREATE INDEX IF NOT EXISTS idx_execution_attempts_active ON execution_attempts(workspace_id, status, lease_until);

CREATE TRIGGER IF NOT EXISTS trg_execution_attempts_task_insert
BEFORE INSERT ON execution_attempts
WHEN NOT EXISTS (
    SELECT 1 FROM tasks
    WHERE workspace_id = NEW.workspace_id AND id = NEW.task_id
)
BEGIN SELECT RAISE(ABORT, 'task not found in workspace'); END;

CREATE TRIGGER IF NOT EXISTS trg_execution_attempts_identity_immutable
BEFORE UPDATE ON execution_attempts
WHEN NEW.id IS NOT OLD.id OR NEW.workspace_id IS NOT OLD.workspace_id
  OR NEW.task_id IS NOT OLD.task_id OR NEW.attempt_number IS NOT OLD.attempt_number
  OR NEW.worker_id IS NOT OLD.worker_id OR NEW.started_at IS NOT OLD.started_at
BEGIN SELECT RAISE(ABORT, 'execution attempt identity is immutable'); END;

CREATE TRIGGER IF NOT EXISTS trg_execution_attempts_terminal_immutable
BEFORE UPDATE ON execution_attempts
WHEN OLD.status IN ('succeeded','failed','cancelled','lease_expired','dead_letter')
  AND (NEW.status IS NOT OLD.status OR NEW.completed_at IS NOT OLD.completed_at
       OR NEW.error_type IS NOT OLD.error_type OR NEW.error_message IS NOT OLD.error_message
       OR NEW.lease_until IS NOT OLD.lease_until)
BEGIN SELECT RAISE(ABORT, 'terminal execution attempt is immutable'); END;

CREATE TRIGGER IF NOT EXISTS trg_versioned_task_contract_immutable
BEFORE UPDATE ON tasks
WHEN OLD.input_schema != '{}'
  AND (NEW.workspace_id IS NOT OLD.workspace_id OR NEW.rule_id IS NOT OLD.rule_id
       OR NEW.rule_version IS NOT OLD.rule_version
       OR NEW.rule_version_number IS NOT OLD.rule_version_number
       OR NEW.variables IS NOT OLD.variables OR NEW.input_schema IS NOT OLD.input_schema
       OR NEW.output_schema IS NOT OLD.output_schema
       OR NEW.browser_profile_id IS NOT OLD.browser_profile_id)
BEGIN SELECT RAISE(ABORT, 'versioned task contract is immutable'); END;

`

const migration016Indexes = `
CREATE UNIQUE INDEX IF NOT EXISTS idx_results_idempotency
ON results(workspace_id, task_id, attempt_id, idempotency_key)
WHERE idempotency_key != '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_results_valid_batch_sequence
ON results(workspace_id, task_id, attempt_id, sequence)
WHERE kind = 'batch' AND valid = 1;
CREATE UNIQUE INDEX IF NOT EXISTS idx_results_one_summary
ON results(workspace_id, task_id, attempt_id)
WHERE kind = 'summary';
CREATE INDEX IF NOT EXISTS idx_results_page
ON results(workspace_id, task_id, valid, kind, sequence, created_at);
`

func migration016(tx *sql.Tx) error {
	columns := []struct{ table, name, definition string }{
		{"tasks", "rule_version_number", "INTEGER NOT NULL DEFAULT 1"},
		{"tasks", "input_schema", "TEXT NOT NULL DEFAULT '{}'"},
		{"tasks", "current_attempt_id", "TEXT"},
		{"tasks", "browser_profile_id", "TEXT NOT NULL DEFAULT ''"},
		{"schedules", "rule_version_number", "INTEGER NOT NULL DEFAULT 1"},
		{"schedules", "input_schema", "TEXT NOT NULL DEFAULT '{}'"},
		{"schedules", "browser_profile_id", "TEXT NOT NULL DEFAULT ''"},
		{"schedules", "timezone", "TEXT NOT NULL DEFAULT 'UTC'"},
		{"results", "attempt_id", "TEXT NOT NULL DEFAULT ''"},
		{"results", "idempotency_key", "TEXT NOT NULL DEFAULT ''"},
		{"results", "sequence", "INTEGER NOT NULL DEFAULT 0"},
		{"results", "kind", "TEXT NOT NULL DEFAULT 'batch'"},
		{"results", "payload_hash", "TEXT NOT NULL DEFAULT ''"},
		{"results", "valid", "BOOLEAN NOT NULL DEFAULT 1"},
		{"results", "validation_error", "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range columns {
		var exists bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info(?) WHERE name = ?)`, column.table, column.name).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			if _, err := tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", column.table, column.name, column.definition)); err != nil {
				return fmt.Errorf("add %s.%s: %w", column.table, column.name, err)
			}
		}
	}
	if _, err := tx.Exec(migration016Schema); err != nil {
		return err
	}

	if _, err := tx.Exec(`
		UPDATE tasks SET rule_version_number = COALESCE((
			SELECT rv.version_number FROM rule_versions rv
			WHERE rv.workspace_id = tasks.workspace_id AND rv.rule_id = tasks.rule_id
			  AND rv.version_label = tasks.rule_version
			LIMIT 1
		), (
			SELECT rv.version_number FROM rule_versions rv
			WHERE rv.workspace_id = tasks.workspace_id AND rv.rule_id = tasks.rule_id
			ORDER BY rv.version_number
			LIMIT 1
		), 1);
		UPDATE schedules SET rule_version_number = COALESCE((
			SELECT rv.version_number FROM rule_versions rv
			WHERE rv.workspace_id = schedules.workspace_id AND rv.rule_id = schedules.rule_id
			  AND rv.version_label = schedules.rule_version
			LIMIT 1
		), (
			SELECT rv.version_number FROM rule_versions rv
			WHERE rv.workspace_id = schedules.workspace_id AND rv.rule_id = schedules.rule_id
			ORDER BY rv.version_number
			LIMIT 1
		), 1);
	`); err != nil {
		return fmt.Errorf("backfill version numbers: %w", err)
	}

	rows, err := tx.Query(`SELECT workspace_id, rule_id, version_number, rule_json, created_at FROM rule_versions`)
	if err != nil {
		return err
	}
	type contractBackfill struct {
		workspace, ruleID string
		version           int
		input, output     models.JSON
		created           time.Time
	}
	var contracts []contractBackfill
	for rows.Next() {
		var workspace, ruleID string
		var version int
		var ruleJSON models.JSON
		var created time.Time
		if err := rows.Scan(&workspace, &ruleID, &version, &ruleJSON, &created); err != nil {
			_ = rows.Close()
			return err
		}
		var versionRule models.Rule
		if err := json.Unmarshal(ruleJSON, &versionRule); err != nil {
			_ = rows.Close()
			return err
		}
		output := versionRule.Output
		if len(output) == 0 {
			output = models.JSON(`{"type":"object","properties":{},"additionalProperties":true}`)
		}
		contracts = append(contracts, contractBackfill{
			workspace: workspace, ruleID: ruleID, version: version,
			input: inferLegacyInputSchema(versionRule.Variables), output: output, created: created,
		})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, contract := range contracts {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO rule_version_contracts
			(workspace_id, rule_id, version_number, input_schema, output_schema, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, contract.workspace, contract.ruleID, contract.version,
			contract.input, contract.output, contract.created); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(`
		UPDATE tasks SET input_schema = COALESCE((SELECT c.input_schema FROM rule_version_contracts c
			WHERE c.workspace_id = tasks.workspace_id AND c.rule_id = tasks.rule_id
			  AND c.version_number = tasks.rule_version_number), '{}'),
			output_schema = COALESCE((SELECT c.output_schema FROM rule_version_contracts c
			WHERE c.workspace_id = tasks.workspace_id AND c.rule_id = tasks.rule_id
			  AND c.version_number = tasks.rule_version_number), output_schema, '{}');
		UPDATE schedules SET input_schema = COALESCE((SELECT c.input_schema FROM rule_version_contracts c
			WHERE c.workspace_id = schedules.workspace_id AND c.rule_id = schedules.rule_id
			  AND c.version_number = schedules.rule_version_number), '{}');
	`); err != nil {
		return err
	}

	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO execution_attempts (
			id, workspace_id, task_id, attempt_number, worker_id, status,
			lease_until, started_at, completed_at, error_type, error_message
		)
		SELECT 'legacy-' || id, workspace_id, id, 1, COALESCE(worker_id, 'legacy-migration'),
			CASE status WHEN 'done' THEN 'succeeded' WHEN 'failed' THEN 'failed'
			WHEN 'cancelled' THEN 'cancelled' WHEN 'dead_letter' THEN 'dead_letter'
			WHEN 'running' THEN 'running' WHEN 'leased' THEN 'leased'
			WHEN 'waiting_for_human' THEN 'waiting_for_human' ELSE 'lease_expired' END,
			lease_until, created_at, completed_at, COALESCE(error_type, ''), COALESCE(error_message, '')
		FROM tasks;
		UPDATE tasks SET current_attempt_id = 'legacy-' || id WHERE current_attempt_id IS NULL;
		UPDATE results SET attempt_id = 'legacy-' || task_id,
			idempotency_key = 'legacy-' || id,
			payload_hash = lower(hex(payload)),
			sequence = (SELECT COUNT(*) FROM results prior
				WHERE prior.workspace_id = results.workspace_id AND prior.task_id = results.task_id
				  AND (prior.created_at < results.created_at OR (prior.created_at = results.created_at AND prior.id <= results.id)))
		WHERE attempt_id = '';
	`); err != nil {
		return fmt.Errorf("backfill execution lineage: %w", err)
	}
	_, err = tx.Exec(migration016Indexes)
	return err
}

func inferLegacyInputSchema(raw models.JSON) models.JSON {
	values := map[string]any{}
	_ = json.Unmarshal(raw, &values)
	properties := map[string]any{}
	for name, value := range values {
		properties[name] = map[string]any{"type": legacyJSONType(value), "default": value}
	}
	encoded, _ := json.Marshal(map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object",
		"properties": properties, "required": []string{}, "additionalProperties": false,
	})
	return models.JSON(encoded)
}

func legacyJSONType(value any) string {
	switch value.(type) {
	case bool:
		return "boolean"
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "string"
	}
}

const migration017Schema = `
CREATE TABLE IF NOT EXISTS mcp_tokens (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    name TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    token_prefix TEXT NOT NULL,
    permissions TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    expires_at DATETIME,
    revoked_at DATETIME,
    last_used_at DATETIME
);
CREATE INDEX IF NOT EXISTS idx_mcp_tokens_workspace
ON mcp_tokens(workspace_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_mcp_tokens_active
ON mcp_tokens(token_hash, revoked_at, expires_at);
`

func migration017(tx *sql.Tx) error {
	_, err := tx.Exec(migration017Schema)
	return err
}

// migration019 adds per-chunk progress to the durable LLM workflow jobs so
// polling clients can render determinate progress while a multi-chunk
// analysis is running.
func migration019(tx *sql.Tx) error {
	columns := []struct{ table, name, definition string }{
		{"requirement_jobs", "completed_chunks", "INTEGER NOT NULL DEFAULT 0"},
		{"dsl_jobs", "completed_chunks", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, column := range columns {
		var exists bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info(?) WHERE name = ?)`, column.table, column.name).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			if _, err := tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", column.table, column.name, column.definition)); err != nil {
				return fmt.Errorf("add %s.%s: %w", column.table, column.name, err)
			}
		}
	}
	return nil
}

const migration020Schema = `
DROP TRIGGER IF EXISTS trg_dsl_workflows_state_transition;
CREATE TRIGGER trg_dsl_workflows_state_transition
BEFORE UPDATE OF status ON dsl_workflows
WHEN NEW.status != OLD.status AND NOT (
       (OLD.status = 'generating' AND NEW.status IN ('awaiting_replay','failed'))
    OR (OLD.status = 'awaiting_replay' AND NEW.status IN ('replaying','failed'))
    OR (OLD.status = 'replaying' AND NEW.status IN ('awaiting_confirmation','repairing','failed'))
    OR (OLD.status = 'repairing' AND NEW.status IN ('awaiting_replay','failed'))
    OR (OLD.status = 'awaiting_confirmation' AND NEW.status IN ('replaying','approved','failed'))
    OR (OLD.status = 'failed' AND NEW.status = 'awaiting_replay')
)
BEGIN SELECT RAISE(ABORT, 'invalid dsl workflow state transition'); END;

DROP TRIGGER IF EXISTS trg_dsl_workflows_terminal_immutable;
CREATE TRIGGER trg_dsl_workflows_terminal_immutable
BEFORE UPDATE ON dsl_workflows
WHEN OLD.status = 'approved'
  OR (OLD.status = 'failed' AND NOT (
       NEW.status = 'awaiting_replay'
   AND NEW.provisional_artifact IS NOT NULL
   AND NEW.provisional_hash IS NOT NULL
   AND (OLD.provisional_hash IS NULL OR NEW.provisional_hash != OLD.provisional_hash)
   AND NEW.current_job_id IS NULL
   AND NEW.error_code IS NULL
   AND NEW.error_message IS NULL
   AND NEW.repair_count = OLD.repair_count
   AND NEW.last_replay_sequence = OLD.last_replay_sequence
   AND NEW.approved_at IS OLD.approved_at
  ))
BEGIN SELECT RAISE(ABORT, 'terminal dsl workflow is immutable'); END;
`

func migration020(tx *sql.Tx) error {
	_, err := tx.Exec(migration020Schema)
	return err
}

const migration021Schema = `
CREATE TABLE IF NOT EXISTS llm_provider_calls (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    recording_id TEXT NOT NULL REFERENCES recordings(id),
    job_type TEXT NOT NULL CHECK (job_type IN ('requirement','dsl')),
    job_id TEXT NOT NULL,
    attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
    call_index INTEGER NOT NULL CHECK (call_index > 0),
    phase TEXT NOT NULL,
    chunk_index INTEGER,
    chunk_count INTEGER NOT NULL DEFAULT 0 CHECK (chunk_count >= 0),
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    prompt_version TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    response_hash TEXT NOT NULL,
    response_id TEXT,
    finish_reason TEXT,
    input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    cache_hit BOOLEAN NOT NULL DEFAULT 0 CHECK (cache_hit IN (0,1)),
    original_bytes INTEGER NOT NULL CHECK (original_bytes >= 0),
    captured_bytes INTEGER NOT NULL CHECK (
        captured_bytes >= 0 AND captured_bytes <= original_bytes
    ),
    artifact_bytes INTEGER NOT NULL CHECK (
        artifact_bytes >= 0 AND artifact_bytes <= 1048576
    ),
    redacted BOOLEAN NOT NULL DEFAULT 0 CHECK (redacted IN (0,1)),
    truncated BOOLEAN NOT NULL DEFAULT 0 CHECK (truncated IN (0,1)),
    replayable BOOLEAN NOT NULL DEFAULT 0 CHECK (
        replayable IN (0,1)
        AND (
            replayable = 0
            OR (
                redacted = 0
                AND truncated = 0
                AND captured_bytes = original_bytes
            )
        )
    ),
    artifact BLOB NOT NULL,
    artifact_hash TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    CHECK (
        chunk_index IS NULL
        OR (
            chunk_count > 0
            AND chunk_index >= 0
            AND chunk_index < chunk_count
        )
    ),
    UNIQUE (workspace_id, job_type, job_id, attempt_number, call_index)
);
CREATE INDEX IF NOT EXISTS idx_llm_provider_calls_job
ON llm_provider_calls(workspace_id, job_type, job_id, attempt_number, call_index);
CREATE INDEX IF NOT EXISTS idx_llm_provider_calls_recording
ON llm_provider_calls(workspace_id, recording_id, created_at);

CREATE TABLE IF NOT EXISTS llm_attempt_reports (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    recording_id TEXT NOT NULL REFERENCES recordings(id),
    job_type TEXT NOT NULL CHECK (job_type IN ('requirement','dsl')),
    job_id TEXT NOT NULL,
    attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
    outcome TEXT NOT NULL CHECK (outcome IN ('succeeded','failed')),
    provider TEXT,
    model TEXT,
    prompt_version TEXT NOT NULL,
    recording_hash TEXT NOT NULL,
    requirement_hash TEXT,
    baseline_hash TEXT,
    selector_catalog_hash TEXT,
    call_count INTEGER NOT NULL DEFAULT 0 CHECK (call_count >= 0),
    input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    validation_phase TEXT,
    error_code TEXT,
    error_message TEXT,
    safety_flags TEXT NOT NULL DEFAULT '[]',
    replayable BOOLEAN NOT NULL DEFAULT 0 CHECK (replayable IN (0,1)),
    artifact_bytes INTEGER NOT NULL CHECK (
        artifact_bytes >= 0 AND artifact_bytes <= 4194304
    ),
    artifact BLOB NOT NULL,
    artifact_hash TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    UNIQUE (workspace_id, job_type, job_id, attempt_number)
);
CREATE INDEX IF NOT EXISTS idx_llm_attempt_reports_job
ON llm_attempt_reports(workspace_id, job_type, job_id, attempt_number);
CREATE INDEX IF NOT EXISTS idx_llm_attempt_reports_recording
ON llm_attempt_reports(workspace_id, recording_id, created_at);

CREATE TRIGGER IF NOT EXISTS trg_llm_provider_calls_requirement_parent
BEFORE INSERT ON llm_provider_calls
WHEN NEW.job_type = 'requirement' AND NOT EXISTS (
    SELECT 1 FROM requirement_jobs
    WHERE workspace_id = NEW.workspace_id
      AND id = NEW.job_id
      AND recording_id = NEW.recording_id
      AND attempt_count >= NEW.attempt_number
)
BEGIN SELECT RAISE(ABORT, 'requirement job attempt not found in workspace'); END;

CREATE TRIGGER IF NOT EXISTS trg_llm_provider_calls_dsl_parent
BEFORE INSERT ON llm_provider_calls
WHEN NEW.job_type = 'dsl' AND NOT EXISTS (
    SELECT 1
    FROM dsl_jobs j
    JOIN dsl_workflows w
      ON w.workspace_id = j.workspace_id AND w.id = j.workflow_id
    WHERE j.workspace_id = NEW.workspace_id
      AND j.id = NEW.job_id
      AND w.recording_id = NEW.recording_id
      AND j.attempt_count >= NEW.attempt_number
)
BEGIN SELECT RAISE(ABORT, 'dsl job attempt not found in workspace'); END;

CREATE TRIGGER IF NOT EXISTS trg_llm_provider_calls_contiguous
BEFORE INSERT ON llm_provider_calls
WHEN NEW.call_index != (
    SELECT COALESCE(MAX(call_index), 0) + 1 FROM llm_provider_calls
    WHERE workspace_id = NEW.workspace_id
      AND job_type = NEW.job_type
      AND job_id = NEW.job_id
      AND attempt_number = NEW.attempt_number
)
BEGIN SELECT RAISE(ABORT, 'llm provider call index is not contiguous'); END;

CREATE TRIGGER IF NOT EXISTS trg_llm_provider_calls_attempt_open
BEFORE INSERT ON llm_provider_calls
WHEN EXISTS (
    SELECT 1 FROM llm_attempt_reports
    WHERE workspace_id = NEW.workspace_id
      AND job_type = NEW.job_type
      AND job_id = NEW.job_id
      AND attempt_number = NEW.attempt_number
)
BEGIN SELECT RAISE(ABORT, 'llm attempt report already recorded'); END;

CREATE TRIGGER IF NOT EXISTS trg_llm_provider_calls_immutable
BEFORE UPDATE ON llm_provider_calls
BEGIN SELECT RAISE(ABORT, 'llm provider call is immutable'); END;

CREATE TRIGGER IF NOT EXISTS trg_llm_attempt_reports_requirement_parent
BEFORE INSERT ON llm_attempt_reports
WHEN NEW.job_type = 'requirement' AND NOT EXISTS (
    SELECT 1 FROM requirement_jobs
    WHERE workspace_id = NEW.workspace_id
      AND id = NEW.job_id
      AND recording_id = NEW.recording_id
      AND attempt_count >= NEW.attempt_number
)
BEGIN SELECT RAISE(ABORT, 'requirement job attempt not found in workspace'); END;

CREATE TRIGGER IF NOT EXISTS trg_llm_attempt_reports_dsl_parent
BEFORE INSERT ON llm_attempt_reports
WHEN NEW.job_type = 'dsl' AND NOT EXISTS (
    SELECT 1
    FROM dsl_jobs j
    JOIN dsl_workflows w
      ON w.workspace_id = j.workspace_id AND w.id = j.workflow_id
    WHERE j.workspace_id = NEW.workspace_id
      AND j.id = NEW.job_id
      AND w.recording_id = NEW.recording_id
      AND j.attempt_count >= NEW.attempt_number
)
BEGIN SELECT RAISE(ABORT, 'dsl job attempt not found in workspace'); END;

CREATE TRIGGER IF NOT EXISTS trg_llm_attempt_reports_call_count
BEFORE INSERT ON llm_attempt_reports
WHEN NEW.call_count != (
    SELECT COUNT(*) FROM llm_provider_calls
    WHERE workspace_id = NEW.workspace_id
      AND job_type = NEW.job_type
      AND job_id = NEW.job_id
      AND attempt_number = NEW.attempt_number
)
BEGIN SELECT RAISE(ABORT, 'llm attempt provider call count mismatch'); END;

CREATE TRIGGER IF NOT EXISTS trg_llm_attempt_reports_replayable
BEFORE INSERT ON llm_attempt_reports
WHEN NEW.replayable = 1 AND (
       NEW.call_count = 0
    OR EXISTS (
        SELECT 1 FROM llm_provider_calls
        WHERE workspace_id = NEW.workspace_id
          AND job_type = NEW.job_type
          AND job_id = NEW.job_id
          AND attempt_number = NEW.attempt_number
          AND replayable = 0
    )
)
BEGIN SELECT RAISE(ABORT, 'llm attempt has non-replayable provider calls'); END;

CREATE TRIGGER IF NOT EXISTS trg_llm_attempt_reports_immutable
BEFORE UPDATE ON llm_attempt_reports
BEGIN SELECT RAISE(ABORT, 'llm attempt report is immutable'); END;
`

func migration021(tx *sql.Tx) error {
	_, err := tx.Exec(migration021Schema)
	return err
}

const migration022Schema = `
ALTER TABLE requirement_jobs
ADD COLUMN attempt_budget INTEGER NOT NULL DEFAULT 3 CHECK (
    attempt_budget BETWEEN 1 AND 3
);
UPDATE requirement_jobs
SET attempt_budget = CASE
    WHEN max_attempts BETWEEN 1 AND 3 THEN max_attempts
    ELSE 3
END;

DROP TRIGGER IF EXISTS trg_llm_provider_calls_requirement_parent;
DROP TRIGGER IF EXISTS trg_llm_provider_calls_dsl_parent;
DROP TRIGGER IF EXISTS trg_llm_provider_calls_contiguous;
DROP TRIGGER IF EXISTS trg_llm_provider_calls_attempt_open;
DROP TRIGGER IF EXISTS trg_llm_provider_calls_immutable;
DROP TRIGGER IF EXISTS trg_llm_attempt_reports_call_count;
DROP TRIGGER IF EXISTS trg_llm_attempt_reports_replayable;

CREATE TABLE llm_provider_calls_hardened (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    recording_id TEXT NOT NULL REFERENCES recordings(id),
    job_type TEXT NOT NULL CHECK (job_type IN ('requirement','dsl')),
    job_id TEXT NOT NULL,
    attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
    call_index INTEGER NOT NULL CHECK (call_index > 0),
    call_kind TEXT NOT NULL CHECK (
        call_kind IN ('provider_response','provider_error','cache_hit')
    ),
    provider_attempt INTEGER NOT NULL CHECK (provider_attempt >= 0),
    phase TEXT NOT NULL,
    chunk_index INTEGER,
    chunk_count INTEGER NOT NULL DEFAULT 0 CHECK (chunk_count >= 0),
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    prompt_version TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    response_hash TEXT NOT NULL,
    response_id TEXT,
    finish_reason TEXT,
    http_status INTEGER NOT NULL DEFAULT 0 CHECK (http_status BETWEEN 0 AND 599),
    error_code TEXT,
    input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    cache_hit BOOLEAN NOT NULL DEFAULT 0 CHECK (cache_hit IN (0,1)),
    original_bytes INTEGER NOT NULL CHECK (original_bytes >= 0),
    original_bytes_exact BOOLEAN NOT NULL CHECK (
        original_bytes_exact IN (0,1)
    ),
    captured_bytes INTEGER NOT NULL CHECK (captured_bytes >= 0),
    artifact_bytes INTEGER NOT NULL CHECK (
        artifact_bytes >= 0 AND artifact_bytes <= 1048576
    ),
    redacted BOOLEAN NOT NULL DEFAULT 0 CHECK (redacted IN (0,1)),
    truncated BOOLEAN NOT NULL DEFAULT 0 CHECK (truncated IN (0,1)),
    replayable BOOLEAN NOT NULL DEFAULT 0 CHECK (
        replayable IN (0,1)
        AND (
            replayable = 0
            OR (
                original_bytes_exact = 1
                AND redacted = 0
                AND truncated = 0
                AND captured_bytes = original_bytes
            )
        )
    ),
    artifact BLOB NOT NULL,
    artifact_hash TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    CHECK (
        chunk_index IS NULL
        OR (
            chunk_count > 0
            AND chunk_index >= 0
            AND chunk_index < chunk_count
        )
    ),
    CHECK (redacted = 1 OR truncated = 1 OR captured_bytes <= original_bytes),
    CHECK (
        (call_kind = 'cache_hit' AND provider_attempt = 0 AND cache_hit = 1)
        OR
        (call_kind != 'cache_hit' AND provider_attempt > 0 AND cache_hit = 0)
    ),
    UNIQUE (workspace_id, job_type, job_id, attempt_number, call_index)
);

INSERT INTO llm_provider_calls_hardened (
    id, workspace_id, recording_id, job_type, job_id, attempt_number,
    call_index, call_kind, provider_attempt, phase, chunk_index, chunk_count,
    provider, model, prompt_version, request_hash, response_hash, response_id,
    finish_reason, http_status, error_code, input_tokens, output_tokens,
    cache_hit, original_bytes, original_bytes_exact, captured_bytes,
    artifact_bytes, redacted, truncated, replayable, artifact, artifact_hash,
    created_at
)
SELECT
    id, workspace_id, recording_id, job_type, job_id, attempt_number,
    call_index,
    CASE WHEN cache_hit = 1 THEN 'cache_hit' ELSE 'provider_response' END,
    CASE WHEN cache_hit = 1 THEN 0 ELSE 1 END,
    phase, chunk_index, chunk_count, provider, model, prompt_version,
    request_hash, response_hash, response_id, finish_reason, 0, NULL,
    input_tokens, output_tokens, cache_hit, original_bytes,
    CASE WHEN truncated = 1 THEN 0 ELSE 1 END,
    captured_bytes,
    artifact_bytes, redacted, truncated, replayable, artifact, artifact_hash,
    created_at
FROM llm_provider_calls;

DROP TABLE llm_provider_calls;
ALTER TABLE llm_provider_calls_hardened RENAME TO llm_provider_calls;
CREATE INDEX idx_llm_provider_calls_job
ON llm_provider_calls(workspace_id, job_type, job_id, attempt_number, call_index);
CREATE INDEX idx_llm_provider_calls_recording
ON llm_provider_calls(workspace_id, recording_id, created_at);

CREATE TRIGGER trg_llm_provider_calls_requirement_parent
BEFORE INSERT ON llm_provider_calls
WHEN NEW.job_type = 'requirement' AND NOT EXISTS (
    SELECT 1 FROM requirement_jobs
    WHERE workspace_id = NEW.workspace_id
      AND id = NEW.job_id
      AND recording_id = NEW.recording_id
      AND attempt_count >= NEW.attempt_number
)
BEGIN SELECT RAISE(ABORT, 'requirement job attempt not found in workspace'); END;

CREATE TRIGGER trg_llm_provider_calls_dsl_parent
BEFORE INSERT ON llm_provider_calls
WHEN NEW.job_type = 'dsl' AND NOT EXISTS (
    SELECT 1
    FROM dsl_jobs j
    JOIN dsl_workflows w
      ON w.workspace_id = j.workspace_id AND w.id = j.workflow_id
    WHERE j.workspace_id = NEW.workspace_id
      AND j.id = NEW.job_id
      AND w.recording_id = NEW.recording_id
      AND j.attempt_count >= NEW.attempt_number
)
BEGIN SELECT RAISE(ABORT, 'dsl job attempt not found in workspace'); END;

CREATE TRIGGER trg_llm_provider_calls_contiguous
BEFORE INSERT ON llm_provider_calls
WHEN NEW.call_index != (
    SELECT COALESCE(MAX(call_index), 0) + 1 FROM llm_provider_calls
    WHERE workspace_id = NEW.workspace_id
      AND job_type = NEW.job_type
      AND job_id = NEW.job_id
      AND attempt_number = NEW.attempt_number
)
BEGIN SELECT RAISE(ABORT, 'llm provider call index is not contiguous'); END;

CREATE TRIGGER trg_llm_provider_calls_attempt_open
BEFORE INSERT ON llm_provider_calls
WHEN EXISTS (
    SELECT 1 FROM llm_attempt_reports
    WHERE workspace_id = NEW.workspace_id
      AND job_type = NEW.job_type
      AND job_id = NEW.job_id
      AND attempt_number = NEW.attempt_number
)
BEGIN SELECT RAISE(ABORT, 'llm attempt report already recorded'); END;

CREATE TRIGGER trg_llm_provider_calls_immutable
BEFORE UPDATE ON llm_provider_calls
BEGIN SELECT RAISE(ABORT, 'llm provider call is immutable'); END;

CREATE TRIGGER trg_llm_attempt_reports_call_count
BEFORE INSERT ON llm_attempt_reports
WHEN NEW.call_count != (
    SELECT COUNT(*) FROM llm_provider_calls
    WHERE workspace_id = NEW.workspace_id
      AND job_type = NEW.job_type
      AND job_id = NEW.job_id
      AND attempt_number = NEW.attempt_number
)
BEGIN SELECT RAISE(ABORT, 'llm attempt provider call count mismatch'); END;

CREATE TRIGGER trg_llm_attempt_reports_replayable
BEFORE INSERT ON llm_attempt_reports
WHEN NEW.replayable = 1 AND (
       NEW.call_count = 0
    OR EXISTS (
        SELECT 1 FROM llm_provider_calls
        WHERE workspace_id = NEW.workspace_id
          AND job_type = NEW.job_type
          AND job_id = NEW.job_id
          AND attempt_number = NEW.attempt_number
          AND replayable = 0
    )
)
BEGIN SELECT RAISE(ABORT, 'llm attempt has non-replayable provider calls'); END;

DROP TABLE llm_cache;
CREATE TABLE llm_cache (
    workspace_id TEXT NOT NULL DEFAULT 'default' REFERENCES workspaces(id),
    cache_key TEXT NOT NULL,
    artifact BLOB NOT NULL,
    artifact_hash TEXT NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    response_id TEXT,
    finish_reason TEXT,
    expires_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (workspace_id, cache_key)
);
CREATE INDEX idx_llm_cache_expires ON llm_cache(expires_at);
CREATE INDEX idx_llm_cache_workspace_expires ON llm_cache(workspace_id, expires_at);
`

func migration022(tx *sql.Tx) error {
	_, err := tx.Exec(migration022Schema)
	return err
}

const migration023Schema = `
DROP TRIGGER IF EXISTS trg_llm_provider_calls_dsl_parent;
DROP TRIGGER IF EXISTS trg_llm_attempt_reports_dsl_parent;

CREATE TRIGGER trg_llm_provider_calls_dsl_parent
BEFORE INSERT ON llm_provider_calls
WHEN NEW.job_type = 'dsl' AND NOT EXISTS (
    SELECT 1
    FROM dsl_jobs j
    JOIN dsl_workflows w
      ON w.workspace_id = j.workspace_id AND w.id = j.workflow_id
    WHERE j.workspace_id = NEW.workspace_id
      AND j.id = NEW.job_id
      AND w.recording_id = NEW.recording_id
      AND j.status = 'running'
      AND j.attempt_count = NEW.attempt_number
)
BEGIN SELECT RAISE(ABORT, 'current dsl job attempt not found in workspace'); END;

CREATE TRIGGER trg_llm_attempt_reports_dsl_parent
BEFORE INSERT ON llm_attempt_reports
WHEN NEW.job_type = 'dsl' AND NOT EXISTS (
    SELECT 1
    FROM dsl_jobs j
    JOIN dsl_workflows w
      ON w.workspace_id = j.workspace_id AND w.id = j.workflow_id
    WHERE j.workspace_id = NEW.workspace_id
      AND j.id = NEW.job_id
      AND w.recording_id = NEW.recording_id
      AND j.status = 'running'
      AND j.attempt_count = NEW.attempt_number
)
BEGIN SELECT RAISE(ABORT, 'current dsl job attempt not found in workspace'); END;
`

func migration023(tx *sql.Tx) error {
	_, err := tx.Exec(migration023Schema)
	return err
}

const migration024Schema = `
DROP TRIGGER IF EXISTS trg_llm_provider_calls_dsl_parent;
DROP TRIGGER IF EXISTS trg_llm_attempt_reports_dsl_parent;
DROP TRIGGER IF EXISTS trg_dsl_jobs_workflow_insert;
DROP TRIGGER IF EXISTS trg_dsl_jobs_immutable_identity;
DROP TRIGGER IF EXISTS trg_dsl_jobs_state_transition;
DROP TRIGGER IF EXISTS trg_dsl_jobs_terminal_immutable;

ALTER TABLE dsl_jobs RENAME TO dsl_jobs_v23;
CREATE TABLE dsl_jobs (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    workflow_id TEXT NOT NULL REFERENCES dsl_workflows(id),
    kind TEXT NOT NULL CHECK (kind IN ('generate','repair','selector_repair')),
    status TEXT NOT NULL CHECK (status IN ('pending','running','completed','failed')),
    request_artifact BLOB NOT NULL,
    result_artifact BLOB,
    request_hash TEXT NOT NULL,
    result_hash TEXT,
    provider TEXT,
    model TEXT,
    prompt_version TEXT NOT NULL,
    chunk_count INTEGER NOT NULL DEFAULT 0,
    completed_chunks INTEGER NOT NULL DEFAULT 0,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts > 0),
    available_at DATETIME NOT NULL,
    lease_until DATETIME,
    provider_dispatched BOOLEAN NOT NULL DEFAULT 0 CHECK (
        provider_dispatched IN (0,1)
        AND (provider_dispatched = 0 OR kind = 'selector_repair')
    ),
    source_attempt_report_id TEXT REFERENCES llm_attempt_reports(id),
    error_code TEXT,
    error_message TEXT,
    safety_flags TEXT NOT NULL DEFAULT '[]',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    started_at DATETIME,
    completed_at DATETIME,
    CHECK (
        (kind = 'selector_repair' AND source_attempt_report_id IS NOT NULL)
        OR (kind != 'selector_repair' AND source_attempt_report_id IS NULL)
    )
);
INSERT INTO dsl_jobs (
    id, workspace_id, workflow_id, kind, status, request_artifact,
    result_artifact, request_hash, result_hash, provider, model,
    prompt_version, chunk_count, completed_chunks, input_tokens, output_tokens,
    attempt_count, max_attempts, available_at, lease_until,
    provider_dispatched, source_attempt_report_id, error_code, error_message,
    safety_flags, created_at, updated_at, started_at, completed_at
)
SELECT
    id, workspace_id, workflow_id, kind, status, request_artifact,
    result_artifact, request_hash, result_hash, provider, model,
    prompt_version, chunk_count, completed_chunks, input_tokens, output_tokens,
    attempt_count, max_attempts, available_at, lease_until,
    0, NULL, error_code, error_message, safety_flags, created_at, updated_at,
    started_at, completed_at
FROM dsl_jobs_v23;
DROP TABLE dsl_jobs_v23;

CREATE INDEX idx_dsl_jobs_claim ON dsl_jobs(status, available_at, created_at);
CREATE INDEX idx_dsl_jobs_workflow ON dsl_jobs(workspace_id, workflow_id, created_at DESC);
CREATE UNIQUE INDEX idx_dsl_jobs_selector_repair_source
ON dsl_jobs(workspace_id, source_attempt_report_id)
WHERE source_attempt_report_id IS NOT NULL;

CREATE TRIGGER trg_dsl_jobs_workflow_insert
BEFORE INSERT ON dsl_jobs
WHEN NOT EXISTS (
    SELECT 1 FROM dsl_workflows
    WHERE workspace_id = NEW.workspace_id AND id = NEW.workflow_id
)
BEGIN SELECT RAISE(ABORT, 'dsl workflow not found in workspace'); END;

CREATE TRIGGER trg_dsl_selector_repair_source_insert
BEFORE INSERT ON dsl_jobs
WHEN NEW.kind = 'selector_repair' AND NOT EXISTS (
    SELECT 1
    FROM llm_attempt_reports r
    JOIN dsl_jobs source
      ON source.workspace_id = r.workspace_id
     AND source.id = r.job_id
    JOIN dsl_workflows workflow
      ON workflow.workspace_id = source.workspace_id
     AND workflow.id = source.workflow_id
    WHERE r.id = NEW.source_attempt_report_id
      AND r.workspace_id = NEW.workspace_id
      AND r.job_type = 'dsl'
      AND r.outcome = 'failed'
      AND r.replayable = 1
      AND source.workflow_id = NEW.workflow_id
      AND source.kind IN ('generate','repair')
      AND source.status = 'failed'
      AND source.attempt_count = r.attempt_number
      AND workflow.recording_id = r.recording_id
)
BEGIN SELECT RAISE(ABORT, 'replayable failed selector repair source report not found in workspace'); END;

CREATE TRIGGER trg_dsl_jobs_immutable_identity
BEFORE UPDATE ON dsl_jobs
WHEN NEW.workspace_id IS NOT OLD.workspace_id
  OR NEW.workflow_id IS NOT OLD.workflow_id
  OR NEW.kind IS NOT OLD.kind
  OR NEW.request_artifact IS NOT OLD.request_artifact
  OR NEW.request_hash IS NOT OLD.request_hash
  OR NEW.max_attempts IS NOT OLD.max_attempts
  OR NEW.source_attempt_report_id IS NOT OLD.source_attempt_report_id
  OR NEW.created_at IS NOT OLD.created_at
BEGIN SELECT RAISE(ABORT, 'dsl job identity is immutable'); END;

CREATE TRIGGER trg_dsl_jobs_state_transition
BEFORE UPDATE OF status ON dsl_jobs
WHEN NOT (
       (OLD.status = 'pending' AND NEW.status = 'running')
    OR (OLD.status = 'running' AND NEW.status IN ('running','pending','completed','failed'))
)
BEGIN SELECT RAISE(ABORT, 'invalid dsl job state transition'); END;

CREATE TRIGGER trg_dsl_jobs_terminal_immutable
BEFORE UPDATE ON dsl_jobs
WHEN OLD.status IN ('completed','failed')
BEGIN SELECT RAISE(ABORT, 'terminal dsl job is immutable'); END;

CREATE TRIGGER trg_dsl_jobs_provider_dispatch_monotonic
BEFORE UPDATE OF provider_dispatched ON dsl_jobs
WHEN OLD.provider_dispatched = 1 AND NEW.provider_dispatched != 1
BEGIN SELECT RAISE(ABORT, 'dsl provider dispatch marker is monotonic'); END;

CREATE TRIGGER trg_llm_provider_calls_dsl_parent
BEFORE INSERT ON llm_provider_calls
WHEN NEW.job_type = 'dsl' AND NOT EXISTS (
    SELECT 1
    FROM dsl_jobs j
    JOIN dsl_workflows w
      ON w.workspace_id = j.workspace_id AND w.id = j.workflow_id
    WHERE j.workspace_id = NEW.workspace_id
      AND j.id = NEW.job_id
      AND w.recording_id = NEW.recording_id
      AND j.status = 'running'
      AND j.attempt_count = NEW.attempt_number
)
BEGIN SELECT RAISE(ABORT, 'current dsl job attempt not found in workspace'); END;

CREATE TRIGGER trg_llm_attempt_reports_dsl_parent
BEFORE INSERT ON llm_attempt_reports
WHEN NEW.job_type = 'dsl' AND NOT EXISTS (
    SELECT 1
    FROM dsl_jobs j
    JOIN dsl_workflows w
      ON w.workspace_id = j.workspace_id AND w.id = j.workflow_id
    WHERE j.workspace_id = NEW.workspace_id
      AND j.id = NEW.job_id
      AND w.recording_id = NEW.recording_id
      AND j.status = 'running'
      AND j.attempt_count = NEW.attempt_number
)
BEGIN SELECT RAISE(ABORT, 'current dsl job attempt not found in workspace'); END;

DROP TRIGGER IF EXISTS trg_dsl_workflows_state_transition;
CREATE TRIGGER trg_dsl_workflows_state_transition
BEFORE UPDATE OF status ON dsl_workflows
WHEN NEW.status != OLD.status AND NOT (
       (OLD.status = 'generating' AND NEW.status IN ('repairing','awaiting_replay','failed'))
    OR (OLD.status = 'awaiting_replay' AND NEW.status IN ('replaying','failed'))
    OR (OLD.status = 'replaying' AND NEW.status IN ('awaiting_confirmation','repairing','failed'))
    OR (OLD.status = 'repairing' AND NEW.status IN ('awaiting_replay','failed'))
    OR (OLD.status = 'awaiting_confirmation' AND NEW.status IN ('replaying','approved','failed'))
    OR (OLD.status = 'failed' AND NEW.status = 'awaiting_replay')
)
BEGIN SELECT RAISE(ABORT, 'invalid dsl workflow state transition'); END;
`

func migration024(tx *sql.Tx) error {
	_, err := tx.Exec(migration024Schema)
	return err
}

const migration025Schema = `
CREATE UNIQUE INDEX IF NOT EXISTS idx_dsl_workflows_admin_reviewed_source
ON dsl_workflows(workspace_id, requirement_id, browser_profile_id, source_export_hash)
WHERE source_kind = 'admin_reviewed_attempt_export'
  AND source_export_hash <> ''
  AND status <> 'failed';

DROP TRIGGER IF EXISTS trg_dsl_workflows_immutable_identity;
CREATE TRIGGER trg_dsl_workflows_immutable_identity
BEFORE UPDATE ON dsl_workflows
WHEN NEW.workspace_id IS NOT OLD.workspace_id
  OR NEW.requirement_id IS NOT OLD.requirement_id
  OR NEW.recording_id IS NOT OLD.recording_id
  OR NEW.browser_profile_id IS NOT OLD.browser_profile_id
  OR NEW.max_repairs IS NOT OLD.max_repairs
  OR NEW.owner IS NOT OLD.owner
  OR NEW.source_kind IS NOT OLD.source_kind
  OR NEW.source_authority IS NOT OLD.source_authority
  OR NEW.source_artifact_hash IS NOT OLD.source_artifact_hash
  OR NEW.source_export_hash IS NOT OLD.source_export_hash
  OR NEW.created_at IS NOT OLD.created_at
BEGIN SELECT RAISE(ABORT, 'dsl workflow identity is immutable'); END;
`

func migration025(tx *sql.Tx) error {
	for _, column := range []struct {
		table      string
		name       string
		definition string
	}{
		{table: "dsl_workflows", name: "source_kind", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "dsl_workflows", name: "source_authority", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "dsl_workflows", name: "source_artifact_hash", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "dsl_workflows", name: "source_export_hash", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "dsl_approvals", name: "source_kind", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "dsl_approvals", name: "source_authority", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "dsl_approvals", name: "source_artifact_hash", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "dsl_approvals", name: "source_export_hash", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "rule_version_contracts", name: "source_kind", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "rule_version_contracts", name: "source_authority", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "rule_version_contracts", name: "source_artifact_hash", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "rule_version_contracts", name: "source_export_hash", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "rule_version_contracts", name: "source_workflow_id", definition: "TEXT NOT NULL DEFAULT ''"},
	} {
		var exists bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info(?) WHERE name = ?)`, column.table, column.name).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			if _, err := tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", column.table, column.name, column.definition)); err != nil {
				return fmt.Errorf("add %s.%s: %w", column.table, column.name, err)
			}
		}
	}
	if _, err := tx.Exec(`
		INSERT INTO audit_logs (
			id, workspace_id, actor, action, resource_type, resource_id, payload, created_at
		)
		SELECT
			'migration-025-dsl-approval-' || hex(randomblob(16)),
			a.workspace_id,
			'system:migration-025',
			'dsl_workflow_approved',
			'dsl_workflow',
			a.workflow_id,
			json_object(
				'ruleId', a.rule_id,
				'version', a.version_number,
				'contentHash', v.content_hash,
				'sourceKind', a.source_kind,
				'sourceAuthority', a.source_authority,
				'sourceArtifactHash', a.source_artifact_hash,
				'sourceExportHash', a.source_export_hash,
				'sourceWorkflowId', CASE
					WHEN a.source_kind = 'admin_reviewed_attempt_export' THEN a.workflow_id
					ELSE ''
				END
			),
			a.created_at
		FROM dsl_approvals a
		JOIN rule_versions v
		  ON v.workspace_id = a.workspace_id
		 AND v.rule_id = a.rule_id
		 AND v.version_number = a.version_number
		WHERE NOT EXISTS (
			SELECT 1
			FROM audit_logs l
			WHERE l.workspace_id = a.workspace_id
			  AND l.action = 'dsl_workflow_approved'
			  AND l.resource_type = 'dsl_workflow'
			  AND l.resource_id = a.workflow_id
		)
	`); err != nil {
		return fmt.Errorf("backfill dsl workflow approval audits: %w", err)
	}
	_, err := tx.Exec(migration025Schema)
	return err
}

// migration026Schema adds the persistent transactional hard-budget ledger. The
// change is additive: it does not rewrite legacy job, attempt, provider-call,
// cache, or qualification records.
//
// One reservation row is one physical-call identity. Rows carry their exact
// rate and token-cap snapshot so a later restart never re-prices history, and
// terminal rows are immutable through application APIs. Amounts are integer
// USD nanos; no budget decision uses binary floating point.
const migration026Schema = `
ALTER TABLE llm_provider_calls ADD COLUMN operation_kind TEXT;
ALTER TABLE llm_provider_calls ADD COLUMN operation_id TEXT;
ALTER TABLE llm_provider_calls ADD COLUMN logical_attempt INTEGER;
ALTER TABLE llm_provider_calls ADD COLUMN physical_ordinal INTEGER;
ALTER TABLE llm_provider_calls ADD COLUMN route_slot TEXT;
ALTER TABLE llm_provider_calls ADD COLUMN policy_fingerprint TEXT;
ALTER TABLE llm_provider_calls ADD COLUMN price_revision TEXT;
ALTER TABLE llm_attempt_reports ADD COLUMN policy_fingerprint TEXT;

CREATE UNIQUE INDEX idx_llm_provider_calls_dispatch
ON llm_provider_calls(
    workspace_id, operation_kind, operation_id, logical_attempt, physical_ordinal
)
WHERE operation_kind IS NOT NULL;

CREATE TRIGGER trg_llm_provider_calls_dispatch_lineage
BEFORE INSERT ON llm_provider_calls
WHEN NOT (
    (
        NEW.operation_kind IS NULL
        AND NEW.operation_id IS NULL
        AND NEW.logical_attempt IS NULL
        AND NEW.physical_ordinal IS NULL
        AND NEW.route_slot IS NULL
        AND NEW.policy_fingerprint IS NULL
        AND NEW.price_revision IS NULL
    )
    OR
    (
        NEW.operation_kind IS NOT NULL
        AND NEW.operation_id IS NOT NULL
        AND NEW.logical_attempt IS NOT NULL
        AND NEW.physical_ordinal IS NOT NULL
        AND NEW.route_slot IS NOT NULL
        AND NEW.policy_fingerprint IS NOT NULL
        AND NEW.price_revision IS NOT NULL
        AND NEW.operation_kind = NEW.job_type
        AND length(NEW.operation_id) > 0
        AND NEW.logical_attempt = NEW.attempt_number
        AND NEW.logical_attempt >= 1
        AND length(NEW.policy_fingerprint) > 0
        AND length(NEW.price_revision) > 0
        AND (
            (NEW.route_slot = 'primary' AND NEW.physical_ordinal = 0)
            OR (NEW.route_slot = 'fallback' AND NEW.physical_ordinal = 1)
        )
    )
)
BEGIN SELECT RAISE(ABORT, 'invalid llm provider call dispatch lineage'); END;

CREATE TRIGGER trg_llm_attempt_reports_policy_lineage
BEFORE INSERT ON llm_attempt_reports
WHEN NEW.policy_fingerprint IS NOT NULL
  AND (
      length(NEW.policy_fingerprint) = 0
      OR EXISTS (
          SELECT 1
          FROM llm_provider_calls c
          WHERE c.workspace_id = NEW.workspace_id
            AND c.job_type = NEW.job_type
            AND c.job_id = NEW.job_id
            AND c.attempt_number = NEW.attempt_number
            AND (
                c.policy_fingerprint IS NULL
                OR c.policy_fingerprint != NEW.policy_fingerprint
            )
      )
  )
BEGIN SELECT RAISE(ABORT, 'llm attempt report policy lineage mismatch'); END;

CREATE TABLE llm_budget_reservations (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    operation_kind TEXT NOT NULL CHECK (length(operation_kind) > 0),
    operation_id TEXT NOT NULL CHECK (length(operation_id) > 0),
    logical_attempt INTEGER NOT NULL CHECK (logical_attempt >= 1),
    physical_ordinal INTEGER NOT NULL CHECK (physical_ordinal IN (0,1)),
    budget_day TEXT NOT NULL CHECK (budget_day GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    route_slot TEXT NOT NULL CHECK (route_slot IN ('primary','fallback')),
    provider TEXT NOT NULL CHECK (length(provider) > 0),
    model TEXT NOT NULL CHECK (length(model) > 0),
    endpoint TEXT NOT NULL CHECK (length(endpoint) > 0),
    policy_fingerprint TEXT NOT NULL CHECK (length(policy_fingerprint) > 0),
    price_revision TEXT NOT NULL CHECK (length(price_revision) > 0),
    request_hash TEXT NOT NULL CHECK (length(request_hash) > 0),
    input_usd_per_million INTEGER NOT NULL CHECK (input_usd_per_million >= 0),
    cached_input_usd_per_million INTEGER NOT NULL CHECK (cached_input_usd_per_million >= 0),
    output_usd_per_million INTEGER NOT NULL CHECK (output_usd_per_million >= 0),
    max_input_tokens INTEGER NOT NULL CHECK (max_input_tokens > 0),
    max_output_tokens INTEGER NOT NULL CHECK (max_output_tokens > 0),
    reserved_usd_nanos INTEGER NOT NULL CHECK (reserved_usd_nanos > 0),
    settled_usd_nanos INTEGER NOT NULL DEFAULT 0 CHECK (settled_usd_nanos >= 0),
    uncached_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (uncached_input_tokens >= 0),
    cached_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cached_input_tokens >= 0),
    output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    cached_input_reported INTEGER NOT NULL DEFAULT 0 CHECK (cached_input_reported IN (0,1)),
    state TEXT NOT NULL CHECK (state IN ('reserved','released','possibly_dispatched','settled','usage_uncertain')),
    error_class TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    dispatched_at DATETIME,
    terminal_at DATETIME,
    -- Ordinal 0 is always the primary route and ordinal 1 the single fallback.
    CHECK (
        (route_slot = 'primary' AND physical_ordinal = 0)
        OR (route_slot = 'fallback' AND physical_ordinal = 1)
    ),
    -- Only terminal settled states carry a settled amount. A released
    -- reservation costs nothing and an uncertain one costs its full worst case.
    CHECK (
        (state IN ('reserved','possibly_dispatched','released') AND settled_usd_nanos = 0)
        OR (state = 'settled' AND settled_usd_nanos <= reserved_usd_nanos)
        OR (state = 'usage_uncertain' AND settled_usd_nanos = reserved_usd_nanos)
    ),
    -- Trusted token counts exist only for an exactly settled entry and must
    -- stay inside the prepared bounds that were reserved for.
    CHECK (
        state = 'settled'
        OR (uncached_input_tokens = 0 AND cached_input_tokens = 0 AND output_tokens = 0 AND cached_input_reported = 0)
    ),
    CHECK (uncached_input_tokens + cached_input_tokens <= max_input_tokens),
    CHECK (output_tokens <= max_output_tokens),
    CHECK (cached_input_tokens = 0 OR cached_input_reported = 1),
    -- A dispatch marker must be durable for anything that may have billed.
    CHECK (
        (state IN ('reserved','released') AND dispatched_at IS NULL)
        OR (state IN ('possibly_dispatched','settled','usage_uncertain') AND dispatched_at IS NOT NULL)
    ),
    CHECK (
        (state IN ('reserved','possibly_dispatched') AND terminal_at IS NULL)
        OR (state IN ('released','settled','usage_uncertain') AND terminal_at IS NOT NULL)
    )
);

CREATE UNIQUE INDEX idx_llm_budget_reservations_identity
ON llm_budget_reservations(workspace_id, operation_kind, operation_id, logical_attempt, physical_ordinal);

CREATE INDEX idx_llm_budget_reservations_global_day
ON llm_budget_reservations(budget_day, state);

CREATE INDEX idx_llm_budget_reservations_workspace_day
ON llm_budget_reservations(workspace_id, budget_day, state);

CREATE INDEX idx_llm_budget_reservations_state
ON llm_budget_reservations(state, created_at);

CREATE INDEX idx_llm_budget_reservations_operation
ON llm_budget_reservations(workspace_id, operation_kind, operation_id, logical_attempt);

CREATE TRIGGER trg_llm_provider_calls_budget_join
BEFORE INSERT ON llm_provider_calls
WHEN NEW.operation_kind IS NOT NULL
  AND NEW.call_kind != 'cache_hit'
  AND NOT EXISTS (
      SELECT 1
      FROM llm_budget_reservations r
      WHERE r.workspace_id = NEW.workspace_id
        AND r.operation_kind = NEW.operation_kind
        AND r.operation_id = NEW.operation_id
        AND r.logical_attempt = NEW.logical_attempt
        AND r.physical_ordinal = NEW.physical_ordinal
        AND r.route_slot = NEW.route_slot
        AND r.provider = NEW.provider
        AND r.model = NEW.model
        AND r.policy_fingerprint = NEW.policy_fingerprint
        AND r.price_revision = NEW.price_revision
        AND r.request_hash = NEW.request_hash
        AND r.state IN ('possibly_dispatched','settled','usage_uncertain')
  )
BEGIN SELECT RAISE(ABORT, 'llm provider call does not match its budget reservation'); END;

CREATE TABLE llm_budget_transitions (
    id TEXT PRIMARY KEY,
    reservation_id TEXT NOT NULL REFERENCES llm_budget_reservations(id),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    event TEXT NOT NULL CHECK (event IN ('reserve','dispatch','settle','release','recover')),
    from_state TEXT CHECK (from_state IS NULL OR from_state IN ('reserved','released','possibly_dispatched','settled','usage_uncertain')),
    to_state TEXT NOT NULL CHECK (to_state IN ('reserved','released','possibly_dispatched','settled','usage_uncertain')),
    amount_usd_nanos INTEGER NOT NULL DEFAULT 0 CHECK (amount_usd_nanos >= 0),
    reason TEXT NOT NULL DEFAULT '',
    policy_fingerprint TEXT NOT NULL CHECK (length(policy_fingerprint) > 0),
    created_at DATETIME NOT NULL
);

CREATE INDEX idx_llm_budget_transitions_reservation
ON llm_budget_transitions(reservation_id, created_at);

CREATE INDEX idx_llm_budget_transitions_workspace
ON llm_budget_transitions(workspace_id, created_at DESC);

CREATE TRIGGER trg_llm_budget_reservations_state_transition
BEFORE UPDATE OF state ON llm_budget_reservations
WHEN NEW.state != OLD.state AND NOT (
       (OLD.state = 'reserved' AND NEW.state IN ('released','possibly_dispatched'))
    OR (OLD.state = 'possibly_dispatched' AND NEW.state IN ('settled','usage_uncertain'))
)
BEGIN SELECT RAISE(ABORT, 'invalid llm budget reservation state transition'); END;

CREATE TRIGGER trg_llm_budget_reservations_terminal_immutable
BEFORE UPDATE ON llm_budget_reservations
WHEN OLD.state IN ('released','settled','usage_uncertain')
BEGIN SELECT RAISE(ABORT, 'terminal llm budget reservation is immutable'); END;

CREATE TRIGGER trg_llm_budget_reservations_immutable_identity
BEFORE UPDATE ON llm_budget_reservations
WHEN NEW.id IS NOT OLD.id
  OR NEW.workspace_id IS NOT OLD.workspace_id
  OR NEW.operation_kind IS NOT OLD.operation_kind
  OR NEW.operation_id IS NOT OLD.operation_id
  OR NEW.logical_attempt IS NOT OLD.logical_attempt
  OR NEW.physical_ordinal IS NOT OLD.physical_ordinal
  OR NEW.budget_day IS NOT OLD.budget_day
  OR NEW.route_slot IS NOT OLD.route_slot
  OR NEW.provider IS NOT OLD.provider
  OR NEW.model IS NOT OLD.model
  OR NEW.endpoint IS NOT OLD.endpoint
  OR NEW.policy_fingerprint IS NOT OLD.policy_fingerprint
  OR NEW.price_revision IS NOT OLD.price_revision
  OR NEW.request_hash IS NOT OLD.request_hash
  OR NEW.input_usd_per_million IS NOT OLD.input_usd_per_million
  OR NEW.cached_input_usd_per_million IS NOT OLD.cached_input_usd_per_million
  OR NEW.output_usd_per_million IS NOT OLD.output_usd_per_million
  OR NEW.max_input_tokens IS NOT OLD.max_input_tokens
  OR NEW.max_output_tokens IS NOT OLD.max_output_tokens
  OR NEW.reserved_usd_nanos IS NOT OLD.reserved_usd_nanos
  OR NEW.created_at IS NOT OLD.created_at
BEGIN SELECT RAISE(ABORT, 'llm budget reservation identity and price snapshot are immutable'); END;

CREATE TRIGGER trg_llm_budget_reservations_dispatch_marker_immutable
BEFORE UPDATE ON llm_budget_reservations
WHEN OLD.dispatched_at IS NOT NULL AND NEW.dispatched_at IS NOT OLD.dispatched_at
BEGIN SELECT RAISE(ABORT, 'llm budget dispatch marker is immutable'); END;

CREATE TRIGGER trg_llm_budget_reservations_no_delete
BEFORE DELETE ON llm_budget_reservations
BEGIN SELECT RAISE(ABORT, 'llm budget reservations cannot be deleted'); END;

CREATE TRIGGER trg_llm_budget_transitions_reservation_insert
BEFORE INSERT ON llm_budget_transitions
WHEN NOT EXISTS (
    SELECT 1 FROM llm_budget_reservations
    WHERE id = NEW.reservation_id AND workspace_id = NEW.workspace_id
)
BEGIN SELECT RAISE(ABORT, 'llm budget reservation not found in workspace'); END;

CREATE TRIGGER trg_llm_budget_transitions_no_update
BEFORE UPDATE ON llm_budget_transitions
BEGIN SELECT RAISE(ABORT, 'llm budget transitions are append-only'); END;

CREATE TRIGGER trg_llm_budget_transitions_no_delete
BEFORE DELETE ON llm_budget_transitions
BEGIN SELECT RAISE(ABORT, 'llm budget transitions are append-only'); END;
`

func migration026(tx *sql.Tx) error {
	_, err := tx.Exec(migration026Schema)
	return err
}

func migration027(tx *sql.Tx) error {
	var exists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('dsl_replay_attempts') WHERE name = 'expires_at')`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := tx.Exec(`ALTER TABLE dsl_replay_attempts ADD COLUMN expires_at DATETIME`); err != nil {
			return fmt.Errorf("add dsl_replay_attempts.expires_at: %w", err)
		}
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_dsl_replay_attempts_expires
		ON dsl_replay_attempts(status, expires_at) WHERE status = 'running' AND expires_at IS NOT NULL`); err != nil {
		return fmt.Errorf("create idx_dsl_replay_attempts_expires: %w", err)
	}
	return nil
}

func migration028(tx *sql.Tx) error {
	for _, column := range []struct {
		table      string
		name       string
		definition string
	}{
		{table: "dsl_workflows", name: "safety_flags", definition: "TEXT NOT NULL DEFAULT '[]'"},
		{table: "rule_versions", name: "safety_flags", definition: "TEXT NOT NULL DEFAULT '[]'"},
	} {
		var exists bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info(?) WHERE name = ?)`, column.table, column.name).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			if _, err := tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", column.table, column.name, column.definition)); err != nil {
				return fmt.Errorf("add %s.%s: %w", column.table, column.name, err)
			}
		}
	}
	return nil
}

// migration029 adds per-workflow budget envelope columns. Units are
// config.USDNanos (nanodollars): $1 = 100_000_000 nanos, so the default 5¢
// envelope is 5_000_000 nanos. Settlement increments spent_usd_nanos by the
// exact settled cost from the ledger.
func migration029(tx *sql.Tx) error {
	for _, column := range []struct {
		table      string
		name       string
		definition string
	}{
		{table: "dsl_workflows", name: "budget_usd_nanos", definition: "INTEGER NOT NULL DEFAULT 5000000"},
		{table: "dsl_workflows", name: "spent_usd_nanos", definition: "INTEGER NOT NULL DEFAULT 0"},
	} {
		var exists bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info(?) WHERE name = ?)`, column.table, column.name).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			if _, err := tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", column.table, column.name, column.definition)); err != nil {
				return fmt.Errorf("add %s.%s: %w", column.table, column.name, err)
			}
		}
	}
	return nil
}

// migration030 adds LLM provenance columns to dsl_approvals and rule_versions.
// At approval time the values are sourced from the latest completed DSL
// generation job by joining dsl_jobs -> llm_attempt_reports ->
// llm_provider_calls on the shared (workspace_id, job_type='dsl', job_id,
// attempt_number) key. Empty-string defaults keep existing rows valid.
func migration030(tx *sql.Tx) error {
	for _, column := range []struct {
		table      string
		name       string
		definition string
	}{
		{table: "dsl_approvals", name: "dsl_job_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "dsl_approvals", name: "provider_call_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "dsl_approvals", name: "prompt_hash", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "dsl_approvals", name: "model_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "dsl_approvals", name: "cache_hit", definition: "BOOLEAN NOT NULL DEFAULT 0"},
		{table: "rule_versions", name: "dsl_workflow_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "rule_versions", name: "dsl_job_id", definition: "TEXT NOT NULL DEFAULT ''"},
	} {
		var exists bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info(?) WHERE name = ?)`, column.table, column.name).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			if _, err := tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", column.table, column.name, column.definition)); err != nil {
				return fmt.Errorf("add %s.%s: %w", column.table, column.name, err)
			}
		}
	}
	return nil
}

// migration031 adds the idempotency_keys table for server-side idempotency
// caching keyed on (workspace_id, Idempotency-Key header).
func migration031(tx *sql.Tx) error {
	_, err := tx.Exec(`
CREATE TABLE IF NOT EXISTS idempotency_keys (
    workspace_id TEXT NOT NULL,
    key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    response_status INTEGER NOT NULL,
    response_body TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME NOT NULL,
    PRIMARY KEY (workspace_id, key)
);
CREATE INDEX IF NOT EXISTS idx_idempotency_keys_expires ON idempotency_keys(expires_at);
`)
	return err
}
