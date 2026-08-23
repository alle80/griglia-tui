package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alle80/griglia-tui/internal/domain"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err = s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) CreateProject(ctx context.Context, project domain.Project) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO projects(id,name,created_at) VALUES(?,?,?)`, project.ID, project.Name, formatTime(project.CreatedAt))
	return err
}

func (s *Store) migrate(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if _, err = conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	latest := 0
	for _, entry := range entries {
		parts := strings.SplitN(entry.Name(), "_", 2)
		version, parseErr := strconv.Atoi(parts[0])
		if parseErr != nil {
			return fmt.Errorf("invalid migration name %s", entry.Name())
		}
		latest = version
		body, readErr := migrationFiles.ReadFile("migrations/" + entry.Name())
		if readErr != nil {
			return readErr
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(body))
		var existing string
		scanErr := conn.QueryRowContext(ctx, "SELECT checksum FROM schema_migrations WHERE version=?", version).Scan(&existing)
		if scanErr == nil {
			if existing != checksum {
				return fmt.Errorf("migration %d checksum mismatch", version)
			}
			continue
		}
		if !errors.Is(scanErr, sql.ErrNoRows) {
			return scanErr
		}
		if _, err = conn.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("migration %d: %w", version, err)
		}
		if _, err = conn.ExecContext(ctx, "INSERT INTO schema_migrations(version,checksum,applied_at) VALUES(?,?,?)", version, checksum, formatTime(time.Now().UTC())); err != nil {
			return err
		}
	}
	var dbVersion int
	if err = conn.QueryRowContext(ctx, "SELECT COALESCE(MAX(version),0) FROM schema_migrations").Scan(&dbVersion); err != nil {
		return err
	}
	if dbVersion > latest {
		return fmt.Errorf("database schema %d is newer than supported schema %d", dbVersion, latest)
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	ok = true
	return nil
}

func (s *Store) CreateTask(ctx context.Context, t domain.Task) (domain.Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Task{}, err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `INSERT INTO tasks(uid,title,description,lifecycle,priority,progress,phase,completion_summary,created_at,updated_at,version) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, t.UID, t.Title, t.Description, t.Lifecycle, t.Priority, t.Progress, t.Phase, t.CompletionSummary, formatTime(t.CreatedAt), formatTime(t.UpdatedAt), t.Version)
	if err != nil {
		return domain.Task{}, err
	}
	t.ID, err = r.LastInsertId()
	if err != nil {
		return domain.Task{}, err
	}
	if err = insertEvent(ctx, tx, t.ID, "task_created", map[string]any{}, t.UpdatedAt); err != nil {
		return domain.Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.Task{}, err
	}
	return t, nil
}

func (s *Store) EditTask(ctx context.Context, t domain.Task, expected int64) (domain.Task, error) {
	payload := map[string]any{"title": t.Title, "description": t.Description, "priority": t.Priority}
	return s.mutateTask(ctx, t, expected, "task_edited", payload)
}

func (s *Store) TransitionTask(ctx context.Context, t domain.Task, expected int64, reason string) (domain.Task, error) {
	kind := map[domain.Lifecycle]string{domain.LifecycleReady: "task_ready", domain.LifecycleDone: "task_completed", domain.LifecycleCancelled: "task_cancelled"}[t.Lifecycle]
	payload := map[string]any{}
	if t.Lifecycle == domain.LifecycleCancelled && reason != "" {
		payload["reason"] = reason
	}
	return s.mutateTask(ctx, t, expected, kind, payload)
}

func (s *Store) mutateTask(ctx context.Context, t domain.Task, expected int64, kind string, payload map[string]any) (domain.Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Task{}, err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `UPDATE tasks SET title=?,description=?,lifecycle=?,priority=?,progress=?,updated_at=?,completed_at=?,cancelled_at=?,version=? WHERE id=? AND version=? AND NOT EXISTS(SELECT 1 FROM claims WHERE task_id=? AND released_at IS NULL)`, t.Title, t.Description, t.Lifecycle, t.Priority, t.Progress, formatTime(t.UpdatedAt), nullableTime(t.CompletedAt), nullableTime(t.CancelledAt), t.Version, t.ID, expected, t.ID)
	if err != nil {
		return domain.Task{}, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return domain.Task{}, err
	}
	if n == 0 {
		var exists int
		if scanErr := tx.QueryRowContext(ctx, `SELECT 1 FROM tasks WHERE id=?`, t.ID).Scan(&exists); errors.Is(scanErr, sql.ErrNoRows) {
			return domain.Task{}, domain.ErrNotFound
		}
		return domain.Task{}, fmt.Errorf("task changed since it was read: %w", domain.ErrConflict)
	}
	if err = insertEvent(ctx, tx, t.ID, kind, payload, t.UpdatedAt); err != nil {
		return domain.Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.Task{}, err
	}
	return t, nil
}

func insertEvent(ctx context.Context, tx *sql.Tx, taskID int64, kind string, payload map[string]any, at time.Time) error {
	return insertActorEvent(ctx, tx, taskID, kind, "human", "", payload, at)
}

func insertActorEvent(ctx context.Context, tx *sql.Tx, taskID int64, kind, actorKind, actorName string, payload map[string]any, at time.Time) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO events(task_id,kind,actor_kind,actor_name,payload_json,created_at) VALUES(?,?,?,?,?,?)`, taskID, kind, actorKind, actorName, string(body), formatTime(at))
	return err
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

const taskColumns = `id,uid,title,description,lifecycle,priority,progress,phase,completion_summary,created_at,updated_at,completed_at,cancelled_at,version`

func (s *Store) ListTasks(ctx context.Context) ([]domain.TaskView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks ORDER BY CASE priority WHEN 'urgent' THEN 4 WHEN 'high' THEN 3 WHEN 'normal' THEN 2 ELSE 1 END DESC, created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := make([]domain.TaskView, 0)
	for rows.Next() {
		t, scanErr := scanTask(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		claim, claimErr := s.activeClaim(ctx, t.ID)
		if claimErr != nil {
			return nil, claimErr
		}
		tasks = append(tasks, domain.NewTaskView(t, claim))
	}
	return tasks, rows.Err()
}

func (s *Store) GetTask(ctx context.Context, id int64) (domain.TaskView, error) {
	t, err := scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TaskView{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.TaskView{}, err
	}
	claim, err := s.activeClaim(ctx, id)
	if err != nil {
		return domain.TaskView{}, err
	}
	return domain.NewTaskView(t, claim), nil
}

func (s *Store) activeClaim(ctx context.Context, taskID int64) (*domain.Claim, error) {
	return scanOptionalClaim(s.db.QueryRowContext(ctx, `SELECT id,task_id,agent_name,instance_id,claimed_at,released_at FROM claims WHERE task_id=? AND released_at IS NULL`, taskID))
}

func scanOptionalClaim(row scanner) (*domain.Claim, error) {
	var claim domain.Claim
	var claimed string
	var released sql.NullString
	if err := row.Scan(&claim.ID, &claim.TaskID, &claim.AgentName, &claim.InstanceID, &claimed, &released); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var err error
	if claim.ClaimedAt, err = parseTime(claimed); err != nil {
		return nil, err
	}
	if released.Valid {
		value, parseErr := parseTime(released.String)
		if parseErr != nil {
			return nil, parseErr
		}
		claim.ReleasedAt = &value
	}
	return &claim, nil
}

func taskFromTx(ctx context.Context, tx *sql.Tx, id int64) (domain.Task, error) {
	t, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, domain.ErrNotFound
	}
	return t, err
}
func claimFromTx(ctx context.Context, tx *sql.Tx, id int64) (*domain.Claim, error) {
	return scanOptionalClaim(tx.QueryRowContext(ctx, `SELECT id,task_id,agent_name,instance_id,claimed_at,released_at FROM claims WHERE task_id=? AND released_at IS NULL`, id))
}

func owns(claim *domain.Claim, identity domain.AgentIdentity) bool {
	return claim != nil && claim.AgentName == identity.AgentName && claim.InstanceID == identity.InstanceID
}

func (s *Store) ClaimTask(ctx context.Context, id int64, identity domain.AgentIdentity, now time.Time) (domain.TaskView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.TaskView{}, err
	}
	defer tx.Rollback()
	task, err := taskFromTx(ctx, tx, id)
	if err != nil {
		return domain.TaskView{}, err
	}
	claim, err := claimFromTx(ctx, tx, id)
	if err != nil {
		return domain.TaskView{}, err
	}
	if claim != nil {
		if owns(claim, identity) {
			return domain.NewTaskView(task, claim), nil
		}
		return domain.TaskView{}, fmt.Errorf("task is already claimed: %w", domain.ErrConflict)
	}
	if task.Lifecycle != domain.LifecycleReady {
		return domain.TaskView{}, fmt.Errorf("only ready tasks can be claimed: %w", domain.ErrConflict)
	}
	claim, err = createClaim(ctx, tx, task.ID, identity, now)
	if err != nil {
		return domain.TaskView{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.TaskView{}, err
	}
	return domain.NewTaskView(task, claim), nil
}

func (s *Store) ClaimNext(ctx context.Context, identity domain.AgentIdentity, now time.Time) (domain.TaskView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.TaskView{}, err
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks t WHERE lifecycle='ready' AND NOT EXISTS(SELECT 1 FROM claims c WHERE c.task_id=t.id AND c.released_at IS NULL) ORDER BY CASE priority WHEN 'urgent' THEN 4 WHEN 'high' THEN 3 WHEN 'normal' THEN 2 ELSE 1 END DESC, created_at ASC, id ASC LIMIT 1`)
	task, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TaskView{}, domain.ErrNoEligible
	}
	if err != nil {
		return domain.TaskView{}, err
	}
	claim, err := createClaim(ctx, tx, task.ID, identity, now)
	if err != nil {
		return domain.TaskView{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.TaskView{}, err
	}
	return domain.NewTaskView(task, claim), nil
}

func createClaim(ctx context.Context, tx *sql.Tx, taskID int64, identity domain.AgentIdentity, now time.Time) (*domain.Claim, error) {
	r, err := tx.ExecContext(ctx, `INSERT INTO claims(task_id,agent_name,instance_id,claimed_at,last_activity_at) VALUES(?,?,?,?,?)`, taskID, identity.AgentName, identity.InstanceID, formatTime(now), formatTime(now))
	if err != nil {
		return nil, err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return nil, err
	}
	claim := &domain.Claim{ID: id, TaskID: taskID, AgentName: identity.AgentName, InstanceID: identity.InstanceID, ClaimedAt: now}
	if err = insertActorEvent(ctx, tx, taskID, "task_claimed", "agent", identity.AgentName, map[string]any{"instance_id": identity.InstanceID}, now); err != nil {
		return nil, err
	}
	return claim, nil
}

func (s *Store) ReleaseClaim(ctx context.Context, id int64, identity domain.AgentIdentity, reason string, now time.Time) (domain.TaskView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.TaskView{}, err
	}
	defer tx.Rollback()
	task, err := taskFromTx(ctx, tx, id)
	if err != nil {
		return domain.TaskView{}, err
	}
	claim, err := claimFromTx(ctx, tx, id)
	if err != nil {
		return domain.TaskView{}, err
	}
	if !owns(claim, identity) {
		return domain.TaskView{}, fmt.Errorf("only the active owner can release the task: %w", domain.ErrConflict)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE claims SET released_at=?,release_reason=? WHERE id=? AND released_at IS NULL`, formatTime(now), reason, claim.ID); err != nil {
		return domain.TaskView{}, err
	}
	if err = insertActorEvent(ctx, tx, id, "task_released", "agent", identity.AgentName, map[string]any{"instance_id": identity.InstanceID, "reason": reason}, now); err != nil {
		return domain.TaskView{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.TaskView{}, err
	}
	return domain.NewTaskView(task, nil), nil
}

func (s *Store) UpdateProgress(ctx context.Context, id int64, percent int, message string, identity domain.AgentIdentity, now time.Time) (domain.TaskView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.TaskView{}, err
	}
	defer tx.Rollback()
	task, err := taskFromTx(ctx, tx, id)
	if err != nil {
		return domain.TaskView{}, err
	}
	claim, err := claimFromTx(ctx, tx, id)
	if err != nil {
		return domain.TaskView{}, err
	}
	if task.Lifecycle != domain.LifecycleReady || !owns(claim, identity) {
		return domain.TaskView{}, fmt.Errorf("only the active owner of a ready task can update progress: %w", domain.ErrConflict)
	}
	phase := task.Phase
	if message != "" {
		phase = message
	}
	r, err := tx.ExecContext(ctx, `UPDATE tasks SET progress=?,phase=?,updated_at=?,version=version+1 WHERE id=? AND version=?`, percent, phase, formatTime(now), id, task.Version)
	if err != nil {
		return domain.TaskView{}, err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return domain.TaskView{}, fmt.Errorf("task changed since it was read: %w", domain.ErrConflict)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE claims SET last_activity_at=? WHERE id=?`, formatTime(now), claim.ID); err != nil {
		return domain.TaskView{}, err
	}
	if err = insertActorEvent(ctx, tx, id, "task_progress", "agent", identity.AgentName, map[string]any{"instance_id": identity.InstanceID, "progress": percent, "message": message}, now); err != nil {
		return domain.TaskView{}, err
	}
	task.Progress, task.Phase, task.UpdatedAt, task.Version = percent, phase, now, task.Version+1
	if err = tx.Commit(); err != nil {
		return domain.TaskView{}, err
	}
	return domain.NewTaskView(task, claim), nil
}

func (s *Store) CompleteClaimedTask(ctx context.Context, id int64, summary string, identity domain.AgentIdentity, now time.Time) (domain.TaskView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.TaskView{}, err
	}
	defer tx.Rollback()
	task, err := taskFromTx(ctx, tx, id)
	if err != nil {
		return domain.TaskView{}, err
	}
	claim, err := claimFromTx(ctx, tx, id)
	if err != nil {
		return domain.TaskView{}, err
	}
	if task.Lifecycle != domain.LifecycleReady || !owns(claim, identity) {
		return domain.TaskView{}, fmt.Errorf("only the active owner can complete the task: %w", domain.ErrConflict)
	}
	r, err := tx.ExecContext(ctx, `UPDATE tasks SET lifecycle='done',progress=100,completion_summary=?,completed_at=?,cancelled_at=NULL,updated_at=?,version=version+1 WHERE id=? AND version=?`, summary, formatTime(now), formatTime(now), id, task.Version)
	if err != nil {
		return domain.TaskView{}, err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return domain.TaskView{}, fmt.Errorf("task changed since it was read: %w", domain.ErrConflict)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE claims SET released_at=?,release_reason='completed',last_activity_at=? WHERE id=? AND released_at IS NULL`, formatTime(now), formatTime(now), claim.ID); err != nil {
		return domain.TaskView{}, err
	}
	if err = insertActorEvent(ctx, tx, id, "task_completed", "agent", identity.AgentName, map[string]any{"instance_id": identity.InstanceID, "comment": summary}, now); err != nil {
		return domain.TaskView{}, err
	}
	task.Lifecycle, task.Progress, task.CompletionSummary, task.CompletedAt, task.UpdatedAt, task.Version = domain.LifecycleDone, 100, summary, &now, now, task.Version+1
	if err = tx.Commit(); err != nil {
		return domain.TaskView{}, err
	}
	return domain.NewTaskView(task, nil), nil
}

type scanner interface{ Scan(...any) error }

func scanTask(row scanner) (domain.Task, error) {
	var t domain.Task
	var created, updated string
	var completed, cancelled sql.NullString
	err := row.Scan(&t.ID, &t.UID, &t.Title, &t.Description, &t.Lifecycle, &t.Priority, &t.Progress, &t.Phase, &t.CompletionSummary, &created, &updated, &completed, &cancelled, &t.Version)
	if err != nil {
		return t, err
	}
	if t.CreatedAt, err = parseTime(created); err != nil {
		return t, err
	}
	if t.UpdatedAt, err = parseTime(updated); err != nil {
		return t, err
	}
	if completed.Valid {
		v, e := parseTime(completed.String)
		if e != nil {
			return t, e
		}
		t.CompletedAt = &v
	}
	if cancelled.Valid {
		v, e := parseTime(cancelled.String)
		if e != nil {
			return t, e
		}
		t.CancelledAt = &v
	}
	return t, nil
}

func formatTime(t time.Time) string         { return t.UTC().Format("2006-01-02T15:04:05.000000Z") }
func parseTime(v string) (time.Time, error) { return time.Parse("2006-01-02T15:04:05.000000Z", v) }
