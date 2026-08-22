package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
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
	if version != 1 {
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
