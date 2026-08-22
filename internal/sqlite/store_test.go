package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alle80/griglia-tui/internal/domain"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMigrationAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err = s.db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("version=%d", version)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var mode string
	if err = s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode=%q", mode)
	}
	var fk int
	if err = s.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys=%d", fk)
	}
}

func TestMigration002FromExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	body, err := migrationFiles.ReadFile("migrations/001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256(body))
	if _, err = db.Exec(`INSERT INTO schema_migrations(version,checksum,applied_at) VALUES(1,?,?)`, checksum, formatTime(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err = s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if _, err = s.db.Exec(`INSERT INTO events(kind,actor_kind,payload_json,created_at) VALUES('probe','human','{}',?)`, formatTime(time.Now().UTC())); err != nil {
		t.Fatalf("events table unavailable: %v", err)
	}
}

func TestMutationsAreVersionedAndAudited(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	task, err := s.CreateTask(ctx, domain.Task{UID: "audit", Title: "Old", Lifecycle: domain.LifecycleBacklog, Priority: domain.PriorityNormal, CreatedAt: now, UpdatedAt: now, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	task.Title, task.Priority, task.Version, task.UpdatedAt = "New", domain.PriorityHigh, 2, now.Add(time.Second)
	task, err = s.EditTask(ctx, task, 1)
	if err != nil {
		t.Fatal(err)
	}
	stale := task
	stale.Version = 3
	if _, err = s.EditTask(ctx, stale, 1); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale err=%v", err)
	}
	task.Lifecycle, task.Version, task.UpdatedAt = domain.LifecycleReady, 3, now.Add(2*time.Second)
	task, err = s.TransitionTask(ctx, task, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	cancelled := now.Add(3 * time.Second)
	task.Lifecycle, task.CancelledAt, task.Version, task.UpdatedAt = domain.LifecycleCancelled, &cancelled, 4, cancelled
	if _, err = s.TransitionTask(ctx, task, 3, "superseded"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=?`, task.ID).Scan(&count); err != nil || count != 4 {
		t.Fatalf("events=%d err=%v", count, err)
	}
	var payload string
	if err = s.db.QueryRow(`SELECT payload_json FROM events WHERE task_id=? AND kind='task_cancelled'`, task.ID).Scan(&payload); err != nil || !strings.Contains(payload, "superseded") {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
}

func TestMutationRollsBackWhenAuditInsertFails(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	task, err := s.CreateTask(ctx, domain.Task{UID: "rollback", Title: "Before", Lifecycle: domain.LifecycleBacklog, Priority: domain.PriorityNormal, CreatedAt: now, UpdatedAt: now, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER reject_audit BEFORE INSERT ON events WHEN NEW.kind='task_edited' BEGIN SELECT RAISE(ABORT, 'reject audit'); END`); err != nil {
		t.Fatal(err)
	}
	task.Title, task.Version, task.UpdatedAt = "After", 2, now.Add(time.Second)
	if _, err = s.EditTask(ctx, task, 1); err == nil {
		t.Fatal("expected audit failure")
	}
	stored, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Title != "Before" || stored.Version != 1 {
		t.Fatalf("partial mutation persisted: %+v", stored)
	}
}

func TestTasksOrderedAndLookedUp(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC()
	for i, p := range []domain.Priority{domain.PriorityLow, domain.PriorityUrgent, domain.PriorityHigh} {
		_, err := s.CreateTask(ctx, domain.Task{UID: fmt.Sprintf("u-%d", i), Title: fmt.Sprintf("t-%d", i), Lifecycle: domain.LifecycleBacklog, Priority: p, CreatedAt: base.Add(time.Duration(i) * time.Second), UpdatedAt: base, Version: 1})
		if err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := s.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := []domain.Priority{tasks[0].Priority, tasks[1].Priority, tasks[2].Priority}; got[0] != domain.PriorityUrgent || got[1] != domain.PriorityHigh || got[2] != domain.PriorityLow {
		t.Fatalf("order=%v", got)
	}
	got, err := s.GetTask(ctx, tasks[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "t-2" {
		t.Fatalf("title=%q", got.Title)
	}
	if _, err = s.GetTask(ctx, 999); err != domain.ErrNotFound {
		t.Fatalf("not found err=%v", err)
	}
}

func TestBasicConcurrentAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s1.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	stores := []*Store{s1, s2}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			now := time.Now().UTC()
			_, e := stores[i%2].CreateTask(context.Background(), domain.Task{UID: fmt.Sprintf("c-%d", i), Title: "concurrent", Lifecycle: domain.LifecycleBacklog, Priority: domain.PriorityNormal, CreatedAt: now, UpdatedAt: now, Version: 1})
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := s1.ListTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 20 {
		t.Fatalf("len=%d", len(tasks))
	}
}
