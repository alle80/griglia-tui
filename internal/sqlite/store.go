package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
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
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
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
func (s *Store) DB() *sql.DB  { return s.db }

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
	r, err := s.db.ExecContext(ctx, `INSERT INTO tasks(uid,title,description,lifecycle,priority,progress,phase,completion_summary,created_at,updated_at,version) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, t.UID, t.Title, t.Description, t.Lifecycle, t.Priority, t.Progress, t.Phase, t.CompletionSummary, formatTime(t.CreatedAt), formatTime(t.UpdatedAt), t.Version)
	if err != nil {
		return domain.Task{}, err
	}
	t.ID, err = r.LastInsertId()
	return t, err
}

const taskColumns = `id,uid,title,description,lifecycle,priority,progress,phase,completion_summary,created_at,updated_at,completed_at,cancelled_at,version`

func (s *Store) ListTasks(ctx context.Context) ([]domain.Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks ORDER BY CASE priority WHEN 'urgent' THEN 4 WHEN 'high' THEN 3 WHEN 'normal' THEN 2 ELSE 1 END DESC, created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := make([]domain.Task, 0)
	for rows.Next() {
		t, scanErr := scanTask(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (s *Store) GetTask(ctx context.Context, id int64) (domain.Task, error) {
	t, err := scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, domain.ErrNotFound
	}
	return t, err
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
