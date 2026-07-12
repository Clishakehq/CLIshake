// Package state implements clishake's SQLite persistence layer: durable
// storage for the current materialized state of sessions, agents, tasks,
// messages, and approvals. The event log in internal/events is the append
// -only audit trail and recovery source; this package holds the latest
// snapshot for fast, structured queries.
package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/clishakehq/clishake/internal/domain"
)

// schemaVersion is the schema version this package creates and expects.
const schemaVersion = 1

// scanner is satisfied by both *sql.Row and *sql.Rows, letting row-decoding
// helpers work for single-row and multi-row queries alike.
type scanner interface {
	Scan(dest ...any) error
}

// Store is a SQLite-backed persistence handle. It is safe for concurrent
// use from multiple goroutines: the underlying connection pool is capped
// at a single connection, which serializes all access through
// database/sql's pool and keeps the per-connection pragmas (WAL,
// busy_timeout) in effect for every statement.
type Store struct {
	db *sql.DB
}

// Open opens (creating if necessary) the SQLite database at path and
// ensures its schema exists. Safe to call repeatedly against the same
// path; schema creation is idempotent.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("state: open %s: %w", path, err)
	}
	// A single connection keeps per-connection pragmas (busy_timeout in
	// particular) in effect for every statement, and gives us correct
	// serialization of concurrent callers for free via database/sql's
	// connection pool, without a separate mutex.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA foreign_keys = ON;",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("state: pragma %q: %w", pragma, err)
		}
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("state: migrate: %w", err)
	}
	return s, nil
}

// Close closes the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// migrate creates the schema if needed. It always runs CREATE TABLE/INDEX
// IF NOT EXISTS, so it is safe to call on every Open, whether the database
// is brand new or already initialized.
func (s *Store) migrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS schema_version (
			version INTEGER NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id           TEXT PRIMARY KEY,
			project_path TEXT NOT NULL DEFAULT '',
			tmux_session TEXT NOT NULL DEFAULT '',
			created_at   TEXT NOT NULL DEFAULT '',
			last_seen    TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE TABLE IF NOT EXISTS agents (
			id                TEXT PRIMARY KEY,
			name              TEXT NOT NULL DEFAULT '',
			role              TEXT NOT NULL DEFAULT '',
			adapter           TEXT NOT NULL DEFAULT '',
			parent_id         TEXT NOT NULL DEFAULT '',
			team              TEXT NOT NULL DEFAULT '',
			task              TEXT NOT NULL DEFAULT '',
			task_id           TEXT NOT NULL DEFAULT '',
			status            TEXT NOT NULL DEFAULT '',
			tmux_json         TEXT NOT NULL DEFAULT '',
			pid               INTEGER NOT NULL DEFAULT 0,
			work_dir          TEXT NOT NULL DEFAULT '',
			branch            TEXT NOT NULL DEFAULT '',
			capabilities_json TEXT NOT NULL DEFAULT '',
			permissions_json  TEXT NOT NULL DEFAULT '',
			config_json       TEXT NOT NULL DEFAULT '',
			created_at        TEXT NOT NULL DEFAULT '',
			last_activity     TEXT NOT NULL DEFAULT '',
			restart_count     INTEGER NOT NULL DEFAULT 0,
			exit_code         INTEGER,
			health            TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE INDEX IF NOT EXISTS idx_agents_name ON agents(name);`,
		`CREATE INDEX IF NOT EXISTS idx_agents_created_at ON agents(created_at);`,
		`CREATE TABLE IF NOT EXISTS tasks (
			id                TEXT PRIMARY KEY,
			title             TEXT NOT NULL DEFAULT '',
			description       TEXT NOT NULL DEFAULT '',
			owner             TEXT NOT NULL DEFAULT '',
			contributors_json TEXT NOT NULL DEFAULT '',
			status            TEXT NOT NULL DEFAULT '',
			priority          INTEGER NOT NULL DEFAULT 0,
			depends_on_json   TEXT NOT NULL DEFAULT '',
			files_json        TEXT NOT NULL DEFAULT '',
			branch            TEXT NOT NULL DEFAULT '',
			created_at        TEXT NOT NULL DEFAULT '',
			updated_at        TEXT NOT NULL DEFAULT '',
			summary           TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_created_at ON tasks(created_at);`,
		`CREATE TABLE IF NOT EXISTS messages (
			seq        INTEGER PRIMARY KEY AUTOINCREMENT,
			id         TEXT NOT NULL UNIQUE,
			sender     TEXT NOT NULL DEFAULT '',
			selector   TEXT NOT NULL DEFAULT '',
			recipient  TEXT NOT NULL DEFAULT '',
			type       TEXT NOT NULL DEFAULT '',
			body       TEXT NOT NULL DEFAULT '',
			task_id    TEXT NOT NULL DEFAULT '',
			reply_to   TEXT NOT NULL DEFAULT '',
			delivery   TEXT NOT NULL DEFAULT '',
			meta_json  TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE INDEX IF NOT EXISTS idx_messages_sender ON messages(sender);`,
		`CREATE INDEX IF NOT EXISTS idx_messages_recipient ON messages(recipient);`,
		`CREATE TABLE IF NOT EXISTS approvals (
			id             TEXT PRIMARY KEY,
			agent_name     TEXT NOT NULL DEFAULT '',
			action         TEXT NOT NULL DEFAULT '',
			command        TEXT NOT NULL DEFAULT '',
			reason         TEXT NOT NULL DEFAULT '',
			resources_json TEXT NOT NULL DEFAULT '',
			risk           TEXT NOT NULL DEFAULT '',
			state          TEXT NOT NULL DEFAULT '',
			created_at     TEXT NOT NULL DEFAULT '',
			decided_at     TEXT
		);`,
		`CREATE INDEX IF NOT EXISTS idx_approvals_state ON approvals(state);`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt, err)
		}
	}

	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if _, err := tx.Exec(`INSERT INTO schema_version(version) VALUES (?)`, schemaVersion); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// ---------------------------------------------------------------------------
// time helpers
// ---------------------------------------------------------------------------

// formatTime renders t as RFC3339Nano in UTC.
func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// parseTime parses an RFC3339Nano string; "" round-trips to the zero time.
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

// formatNullTime renders a nullable *time.Time for a query argument.
func formatNullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

// parseNullTime parses a nullable column back into a *time.Time.
func parseNullTime(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	t, err := parseTime(ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// nullIntPtr renders a nullable *int for a query argument.
func nullIntPtr(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// ---------------------------------------------------------------------------
// JSON helpers
// ---------------------------------------------------------------------------

func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unmarshalJSON(s string, out any) error {
	if s == "" {
		return nil
	}
	return json.Unmarshal([]byte(s), out)
}

// ---------------------------------------------------------------------------
// Session
// ---------------------------------------------------------------------------

// SaveSession upserts sess by ID.
func (s *Store) SaveSession(sess domain.Session) error {
	_, err := s.db.Exec(`
		INSERT INTO sessions (id, project_path, tmux_session, created_at, last_seen)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			project_path = excluded.project_path,
			tmux_session = excluded.tmux_session,
			created_at   = excluded.created_at,
			last_seen    = excluded.last_seen
	`, sess.ID, sess.ProjectPath, sess.TmuxSession, formatTime(sess.CreatedAt), formatTime(sess.LastSeen))
	if err != nil {
		return fmt.Errorf("state: save session: %w", err)
	}
	return nil
}

// GetSession returns the most recently created session, or (nil, nil) if
// none exists.
func (s *Store) GetSession() (*domain.Session, error) {
	row := s.db.QueryRow(`
		SELECT id, project_path, tmux_session, created_at, last_seen
		FROM sessions
		ORDER BY created_at DESC, rowid DESC
		LIMIT 1
	`)
	var (
		sess                domain.Session
		createdAt, lastSeen string
	)
	if err := row.Scan(&sess.ID, &sess.ProjectPath, &sess.TmuxSession, &createdAt, &lastSeen); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("state: get session: %w", err)
	}
	var err error
	if sess.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("state: parse session created_at: %w", err)
	}
	if sess.LastSeen, err = parseTime(lastSeen); err != nil {
		return nil, fmt.Errorf("state: parse session last_seen: %w", err)
	}
	return &sess, nil
}

// ---------------------------------------------------------------------------
// Agents
// ---------------------------------------------------------------------------

const agentColumns = `id, name, role, adapter, parent_id, team, task, task_id, status,
	tmux_json, pid, work_dir, branch, capabilities_json, permissions_json, config_json,
	created_at, last_activity, restart_count, exit_code, health`

// SaveAgent upserts a by ID.
func (s *Store) SaveAgent(a *domain.Agent) error {
	tmuxJSON, err := marshalJSON(a.Tmux)
	if err != nil {
		return fmt.Errorf("state: marshal agent tmux: %w", err)
	}
	capsJSON, err := marshalJSON(a.Capabilities)
	if err != nil {
		return fmt.Errorf("state: marshal agent capabilities: %w", err)
	}
	permsJSON, err := marshalJSON(a.Permissions)
	if err != nil {
		return fmt.Errorf("state: marshal agent permissions: %w", err)
	}
	cfgJSON, err := marshalJSON(a.Config)
	if err != nil {
		return fmt.Errorf("state: marshal agent config: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT INTO agents (
			id, name, role, adapter, parent_id, team, task, task_id, status,
			tmux_json, pid, work_dir, branch, capabilities_json, permissions_json, config_json,
			created_at, last_activity, restart_count, exit_code, health
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name              = excluded.name,
			role              = excluded.role,
			adapter           = excluded.adapter,
			parent_id         = excluded.parent_id,
			team              = excluded.team,
			task              = excluded.task,
			task_id           = excluded.task_id,
			status            = excluded.status,
			tmux_json         = excluded.tmux_json,
			pid               = excluded.pid,
			work_dir          = excluded.work_dir,
			branch            = excluded.branch,
			capabilities_json = excluded.capabilities_json,
			permissions_json  = excluded.permissions_json,
			config_json       = excluded.config_json,
			created_at        = excluded.created_at,
			last_activity     = excluded.last_activity,
			restart_count     = excluded.restart_count,
			exit_code         = excluded.exit_code,
			health            = excluded.health
	`,
		a.ID, a.Name, a.Role, a.Adapter, a.ParentID, a.Team, a.Task, a.TaskID, string(a.Status),
		tmuxJSON, a.PID, a.WorkDir, a.Branch, capsJSON, permsJSON, cfgJSON,
		formatTime(a.CreatedAt), formatTime(a.LastActivity), a.RestartCount, nullIntPtr(a.ExitCode), a.Health,
	)
	if err != nil {
		return fmt.Errorf("state: save agent: %w", err)
	}
	return nil
}

func scanAgent(sc scanner) (*domain.Agent, error) {
	var (
		a                       domain.Agent
		status                  string
		tmuxJSON, capsJSON      string
		permsJSON, cfgJSON      string
		createdAt, lastActivity string
		exitCode                sql.NullInt64
	)
	if err := sc.Scan(
		&a.ID, &a.Name, &a.Role, &a.Adapter, &a.ParentID, &a.Team, &a.Task, &a.TaskID, &status,
		&tmuxJSON, &a.PID, &a.WorkDir, &a.Branch, &capsJSON, &permsJSON, &cfgJSON,
		&createdAt, &lastActivity, &a.RestartCount, &exitCode, &a.Health,
	); err != nil {
		return nil, err
	}
	a.Status = domain.AgentStatus(status)
	if err := unmarshalJSON(tmuxJSON, &a.Tmux); err != nil {
		return nil, fmt.Errorf("unmarshal tmux: %w", err)
	}
	if err := unmarshalJSON(capsJSON, &a.Capabilities); err != nil {
		return nil, fmt.Errorf("unmarshal capabilities: %w", err)
	}
	if err := unmarshalJSON(permsJSON, &a.Permissions); err != nil {
		return nil, fmt.Errorf("unmarshal permissions: %w", err)
	}
	if err := unmarshalJSON(cfgJSON, &a.Config); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	var err error
	if a.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	if a.LastActivity, err = parseTime(lastActivity); err != nil {
		return nil, fmt.Errorf("parse last_activity: %w", err)
	}
	if exitCode.Valid {
		v := int(exitCode.Int64)
		a.ExitCode = &v
	}
	return &a, nil
}

// GetAgent returns the agent with id, or (nil, nil) if not found.
func (s *Store) GetAgent(id string) (*domain.Agent, error) {
	row := s.db.QueryRow(`SELECT `+agentColumns+` FROM agents WHERE id = ?`, id)
	a, err := scanAgent(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("state: get agent: %w", err)
	}
	return a, nil
}

// GetAgentByName returns the agent named name, or (nil, nil) if not found.
// Matching is case-insensitive (COLLATE NOCASE) so "@Jean-Pierre" and
// "@jean-pierre" resolve to the same agent; the add-time duplicate check runs
// through here too, so names can't collide by case. If multiple agents somehow
// share a name, the most recently created wins.
func (s *Store) GetAgentByName(name string) (*domain.Agent, error) {
	row := s.db.QueryRow(`SELECT `+agentColumns+` FROM agents WHERE name = ? COLLATE NOCASE ORDER BY created_at DESC, rowid DESC LIMIT 1`, name)
	a, err := scanAgent(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("state: get agent by name: %w", err)
	}
	return a, nil
}

// ListAgents returns all agents ordered by created_at ascending.
func (s *Store) ListAgents() ([]*domain.Agent, error) {
	rows, err := s.db.Query(`SELECT ` + agentColumns + ` FROM agents ORDER BY created_at ASC, rowid ASC`)
	if err != nil {
		return nil, fmt.Errorf("state: list agents: %w", err)
	}
	defer rows.Close()
	var out []*domain.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("state: scan agent: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteAgent removes the agent with id, if any.
func (s *Store) DeleteAgent(id string) error {
	if _, err := s.db.Exec(`DELETE FROM agents WHERE id = ?`, id); err != nil {
		return fmt.Errorf("state: delete agent: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Tasks
// ---------------------------------------------------------------------------

const taskColumns = `id, title, description, owner, contributors_json, status, priority,
	depends_on_json, files_json, branch, created_at, updated_at, summary`

// SaveTask upserts t by ID.
func (s *Store) SaveTask(t *domain.Task) error {
	contribJSON, err := marshalJSON(t.Contributors)
	if err != nil {
		return fmt.Errorf("state: marshal task contributors: %w", err)
	}
	dependsJSON, err := marshalJSON(t.DependsOn)
	if err != nil {
		return fmt.Errorf("state: marshal task depends_on: %w", err)
	}
	filesJSON, err := marshalJSON(t.Files)
	if err != nil {
		return fmt.Errorf("state: marshal task files: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT INTO tasks (
			id, title, description, owner, contributors_json, status, priority,
			depends_on_json, files_json, branch, created_at, updated_at, summary
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title             = excluded.title,
			description       = excluded.description,
			owner             = excluded.owner,
			contributors_json = excluded.contributors_json,
			status            = excluded.status,
			priority          = excluded.priority,
			depends_on_json   = excluded.depends_on_json,
			files_json        = excluded.files_json,
			branch            = excluded.branch,
			created_at        = excluded.created_at,
			updated_at        = excluded.updated_at,
			summary           = excluded.summary
	`,
		t.ID, t.Title, t.Description, t.Owner, contribJSON, string(t.Status), t.Priority,
		dependsJSON, filesJSON, t.Branch, formatTime(t.CreatedAt), formatTime(t.UpdatedAt), t.Summary,
	)
	if err != nil {
		return fmt.Errorf("state: save task: %w", err)
	}
	return nil
}

func scanTask(sc scanner) (*domain.Task, error) {
	var (
		t                                   domain.Task
		status                              string
		contribJSON, dependsJSON, filesJSON string
		createdAt, updatedAt                string
	)
	if err := sc.Scan(
		&t.ID, &t.Title, &t.Description, &t.Owner, &contribJSON, &status, &t.Priority,
		&dependsJSON, &filesJSON, &t.Branch, &createdAt, &updatedAt, &t.Summary,
	); err != nil {
		return nil, err
	}
	t.Status = domain.TaskStatus(status)
	if err := unmarshalJSON(contribJSON, &t.Contributors); err != nil {
		return nil, fmt.Errorf("unmarshal contributors: %w", err)
	}
	if err := unmarshalJSON(dependsJSON, &t.DependsOn); err != nil {
		return nil, fmt.Errorf("unmarshal depends_on: %w", err)
	}
	if err := unmarshalJSON(filesJSON, &t.Files); err != nil {
		return nil, fmt.Errorf("unmarshal files: %w", err)
	}
	var err error
	if t.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	if t.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}
	return &t, nil
}

// GetTask returns the task with id, or (nil, nil) if not found.
func (s *Store) GetTask(id string) (*domain.Task, error) {
	row := s.db.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id)
	t, err := scanTask(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("state: get task: %w", err)
	}
	return t, nil
}

// ListTasks returns all tasks ordered by created_at ascending.
func (s *Store) ListTasks() ([]*domain.Task, error) {
	rows, err := s.db.Query(`SELECT ` + taskColumns + ` FROM tasks ORDER BY created_at ASC, rowid ASC`)
	if err != nil {
		return nil, fmt.Errorf("state: list tasks: %w", err)
	}
	defer rows.Close()
	var out []*domain.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("state: scan task: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteTask removes the task with id, if any.
func (s *Store) DeleteTask(id string) error {
	if _, err := s.db.Exec(`DELETE FROM tasks WHERE id = ?`, id); err != nil {
		return fmt.Errorf("state: delete task: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

const messageColumns = `id, sender, selector, recipient, type, body, task_id, reply_to, delivery, meta_json, created_at`

// SaveMessage upserts m by ID so delivery state can be updated in place
// without disturbing its position in delivery order.
func (s *Store) SaveMessage(m *domain.Message) error {
	metaJSON, err := marshalJSON(m.Meta)
	if err != nil {
		return fmt.Errorf("state: marshal message meta: %w", err)
	}
	_, err = s.db.Exec(`
		INSERT INTO messages (
			id, sender, selector, recipient, type, body, task_id, reply_to, delivery, meta_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			sender     = excluded.sender,
			selector   = excluded.selector,
			recipient  = excluded.recipient,
			type       = excluded.type,
			body       = excluded.body,
			task_id    = excluded.task_id,
			reply_to   = excluded.reply_to,
			delivery   = excluded.delivery,
			meta_json  = excluded.meta_json,
			created_at = excluded.created_at
	`,
		m.ID, m.Sender, m.Selector, m.Recipient, string(m.Type), m.Body, m.TaskID, m.ReplyTo,
		string(m.Delivery), metaJSON, formatTime(m.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("state: save message: %w", err)
	}
	return nil
}

func scanMessage(sc scanner) (*domain.Message, error) {
	var (
		m                   domain.Message
		typ, delivery       string
		metaJSON, createdAt string
	)
	if err := sc.Scan(
		&m.ID, &m.Sender, &m.Selector, &m.Recipient, &typ, &m.Body, &m.TaskID, &m.ReplyTo,
		&delivery, &metaJSON, &createdAt,
	); err != nil {
		return nil, err
	}
	m.Type = domain.MessageType(typ)
	m.Delivery = domain.DeliveryState(delivery)
	if err := unmarshalJSON(metaJSON, &m.Meta); err != nil {
		return nil, fmt.Errorf("unmarshal meta: %w", err)
	}
	var err error
	if m.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	return &m, nil
}

// ListMessages returns messages ordered oldest-first (newest last). If
// limit <= 0 all messages are returned; otherwise only the most recent
// `limit` messages are returned, still in oldest-first order.
func (s *Store) ListMessages(limit int) ([]*domain.Message, error) {
	return s.queryMessages(`SELECT `+messageColumns+` FROM messages`, nil, limit)
}

// ListMessagesByDelivery returns up to `limit` most-recent messages in the
// given delivery state (oldest-first / newest-last), for supervisor
// redelivery of messages that an agent-side send could not push itself.
func (s *Store) ListMessagesByDelivery(state domain.DeliveryState, limit int) ([]*domain.Message, error) {
	return s.queryMessages(
		`SELECT `+messageColumns+` FROM messages WHERE delivery = ?`,
		[]any{string(state)},
		limit,
	)
}

// ListMessagesWith returns messages where agentName is the sender or the
// resolved recipient, ordered and limited as in ListMessages.
func (s *Store) ListMessagesWith(agentName string, limit int) ([]*domain.Message, error) {
	return s.queryMessages(
		`SELECT `+messageColumns+` FROM messages WHERE sender = ? OR recipient = ?`,
		[]any{agentName, agentName},
		limit,
	)
}

func (s *Store) queryMessages(baseQuery string, args []any, limit int) ([]*domain.Message, error) {
	query := baseQuery
	if limit > 0 {
		query += ` ORDER BY seq DESC LIMIT ?`
		args = append(args, limit)
	} else {
		query += ` ORDER BY seq ASC`
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("state: list messages: %w", err)
	}
	defer rows.Close()
	var out []*domain.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("state: scan message: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if limit > 0 {
		// The query above fetched the newest `limit` rows in
		// descending order; reverse to oldest-first / newest-last.
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Approvals
// ---------------------------------------------------------------------------

const approvalColumns = `id, agent_name, action, command, reason, resources_json, risk, state, created_at, decided_at`

// SaveApproval upserts ap by ID.
func (s *Store) SaveApproval(ap *domain.Approval) error {
	resJSON, err := marshalJSON(ap.Resources)
	if err != nil {
		return fmt.Errorf("state: marshal approval resources: %w", err)
	}
	_, err = s.db.Exec(`
		INSERT INTO approvals (
			id, agent_name, action, command, reason, resources_json, risk, state, created_at, decided_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			agent_name     = excluded.agent_name,
			action         = excluded.action,
			command        = excluded.command,
			reason         = excluded.reason,
			resources_json = excluded.resources_json,
			risk           = excluded.risk,
			state          = excluded.state,
			created_at     = excluded.created_at,
			decided_at     = excluded.decided_at
	`,
		ap.ID, ap.AgentName, ap.Action, ap.Command, ap.Reason, resJSON, ap.Risk,
		string(ap.State), formatTime(ap.CreatedAt), formatNullTime(ap.DecidedAt),
	)
	if err != nil {
		return fmt.Errorf("state: save approval: %w", err)
	}
	return nil
}

func scanApproval(sc scanner) (*domain.Approval, error) {
	var (
		ap                 domain.Approval
		state              string
		resJSON, createdAt string
		decidedAt          sql.NullString
	)
	if err := sc.Scan(
		&ap.ID, &ap.AgentName, &ap.Action, &ap.Command, &ap.Reason, &resJSON, &ap.Risk,
		&state, &createdAt, &decidedAt,
	); err != nil {
		return nil, err
	}
	ap.State = domain.ApprovalState(state)
	if err := unmarshalJSON(resJSON, &ap.Resources); err != nil {
		return nil, fmt.Errorf("unmarshal resources: %w", err)
	}
	var err error
	if ap.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	if ap.DecidedAt, err = parseNullTime(decidedAt); err != nil {
		return nil, fmt.Errorf("parse decided_at: %w", err)
	}
	return &ap, nil
}

// GetApproval returns the approval with id, or (nil, nil) if not found.
func (s *Store) GetApproval(id string) (*domain.Approval, error) {
	row := s.db.QueryRow(`SELECT `+approvalColumns+` FROM approvals WHERE id = ?`, id)
	ap, err := scanApproval(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("state: get approval: %w", err)
	}
	return ap, nil
}

// ListApprovals returns approvals matching state, ordered by created_at
// ascending. An empty state matches all approvals.
func (s *Store) ListApprovals(state domain.ApprovalState) ([]*domain.Approval, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if state == "" {
		rows, err = s.db.Query(`SELECT ` + approvalColumns + ` FROM approvals ORDER BY created_at ASC, rowid ASC`)
	} else {
		rows, err = s.db.Query(`SELECT `+approvalColumns+` FROM approvals WHERE state = ? ORDER BY created_at ASC, rowid ASC`, string(state))
	}
	if err != nil {
		return nil, fmt.Errorf("state: list approvals: %w", err)
	}
	defer rows.Close()
	var out []*domain.Approval
	for rows.Next() {
		ap, err := scanApproval(rows)
		if err != nil {
			return nil, fmt.Errorf("state: scan approval: %w", err)
		}
		out = append(out, ap)
	}
	return out, rows.Err()
}
