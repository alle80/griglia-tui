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
	if version != 5 {
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

func TestMigrationsFromExistingV1Database(t *testing.T) {
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
	if err = s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 5 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if _, err = s.db.Exec(`INSERT INTO events(kind,actor_kind,payload_json,created_at) VALUES('probe','human','{}',?)`, formatTime(time.Now().UTC())); err != nil {
		t.Fatalf("events table unavailable: %v", err)
	}
}

func TestMigration003FromExistingV2Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for version, name := range []string{"001_initial.sql", "002_events.sql"} {
		body, readErr := migrationFiles.ReadFile("migrations/" + name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(body))
		if _, err = db.Exec(`INSERT INTO schema_migrations(version,checksum,applied_at) VALUES(?,?,?)`, version+1, checksum, formatTime(time.Now().UTC())); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err = s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 5 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	var table string
	if err = s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='claims'`).Scan(&table); err != nil || table != "claims" {
		t.Fatalf("claims=%q err=%v", table, err)
	}
}

func TestMigration004FromExistingV3Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v3.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for version, name := range []string{"001_initial.sql", "002_events.sql", "003_claims.sql"} {
		body, readErr := migrationFiles.ReadFile("migrations/" + name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(body))
		if _, err = db.Exec(`INSERT INTO schema_migrations(version,checksum,applied_at) VALUES(?,?,?)`, version+1, checksum, formatTime(time.Now().UTC())); err != nil {
			t.Fatal(err)
		}
	}
	now := formatTime(time.Now().UTC())
	if _, err = db.Exec(`INSERT INTO tasks(uid,title,description,lifecycle,priority,progress,phase,completion_summary,created_at,updated_at,version) VALUES('m4','Milestone 4 task','','ready','normal',0,'','',?,?,1)`, now, now); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err = s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 5 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	view, err := s.GetTask(context.Background(), 1)
	if err != nil || view.Title != "Milestone 4 task" || *view.OperationalState != domain.OperationalAvailable {
		t.Fatalf("existing task=%+v err=%v", view, err)
	}
	questions, err := s.ListQuestions(context.Background(), 1, domain.QuestionsAll)
	if err != nil || len(questions) != 0 {
		t.Fatalf("questions=%v err=%v", questions, err)
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

func TestListTasksIncludesOnlyActiveClaimsInSingleQuery(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	create := func(uid string, lifecycle domain.Lifecycle, priority domain.Priority, offset time.Duration) domain.Task {
		t.Helper()
		task, err := s.CreateTask(ctx, domain.Task{UID: uid, Title: uid, Lifecycle: lifecycle, Priority: priority, CreatedAt: base.Add(offset), UpdatedAt: base.Add(offset), Version: 1})
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	backlog := create("backlog", domain.LifecycleBacklog, domain.PriorityUrgent, 0)
	claimed := create("claimed", domain.LifecycleReady, domain.PriorityHigh, time.Second)
	historical := create("historical", domain.LifecycleReady, domain.PriorityNormal, 2*time.Second)
	unclaimed := create("unclaimed", domain.LifecycleReady, domain.PriorityLow, 3*time.Second)

	activeOwner := domain.AgentIdentity{AgentName: "codex", InstanceID: "active-instance"}
	activeView, err := s.ClaimTask(ctx, claimed.ID, activeOwner, base.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	historicalOwner := domain.AgentIdentity{AgentName: "claude", InstanceID: "released-instance"}
	if _, err = s.ClaimTask(ctx, historical.ID, historicalOwner, base.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReleaseClaim(ctx, historical.ID, historicalOwner, "handoff", base.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}

	views, err := s.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []int64{backlog.ID, claimed.ID, historical.ID, unclaimed.ID}
	if len(views) != len(wantOrder) {
		t.Fatalf("len=%d want=%d", len(views), len(wantOrder))
	}
	seen := make(map[int64]bool, len(views))
	for i, view := range views {
		if view.ID != wantOrder[i] {
			t.Fatalf("order[%d]=%d want=%d", i, view.ID, wantOrder[i])
		}
		if seen[view.ID] {
			t.Fatalf("task %d appeared more than once", view.ID)
		}
		seen[view.ID] = true
	}
	if views[0].OperationalState != nil || views[0].ActiveClaim != nil {
		t.Fatalf("backlog view=%+v", views[0])
	}
	if views[1].OperationalState == nil || *views[1].OperationalState != domain.OperationalWorking {
		t.Fatalf("claimed state=%v", views[1].OperationalState)
	}
	if views[1].ActiveClaim == nil || views[1].ActiveClaim.ID != activeView.ActiveClaim.ID || views[1].ActiveClaim.TaskID != claimed.ID || views[1].ActiveClaim.AgentName != activeOwner.AgentName || views[1].ActiveClaim.InstanceID != activeOwner.InstanceID || !views[1].ActiveClaim.ClaimedAt.Equal(base.Add(4*time.Second)) || views[1].ActiveClaim.ReleasedAt != nil {
		t.Fatalf("active claim=%+v", views[1].ActiveClaim)
	}
	for _, index := range []int{2, 3} {
		if views[index].OperationalState == nil || *views[index].OperationalState != domain.OperationalAvailable || views[index].ActiveClaim != nil {
			t.Fatalf("unclaimed ready view=%+v", views[index])
		}
	}
}

func TestListTasksQueryJoinsActiveClaims(t *testing.T) {
	normalized := strings.Join(strings.Fields(strings.ToLower(listTasksQuery)), " ")
	// One outer select over tasks plus the correlated questions and
	// dependencies EXISTS subqueries — never a per-task query issued from Go.
	if strings.Count(normalized, "select ") != 3 {
		t.Fatalf("list query must remain a single statement: %s", normalized)
	}
	if strings.Count(normalized, "from tasks") != 1 {
		t.Fatalf("list query must read tasks exactly once: %s", normalized)
	}
	if !strings.Contains(normalized, "left join claims on claims.task_id=tasks.id and claims.released_at is null") {
		t.Fatalf("list query must join active claims: %s", normalized)
	}
	if !strings.Contains(normalized, "exists(select 1 from questions where questions.task_id=tasks.id and questions.blocking=1 and questions.answered_at is null)") {
		t.Fatalf("list query must derive unanswered blocking questions inline: %s", normalized)
	}
	if !strings.Contains(normalized, "exists(select 1 from dependencies join tasks prerequisite on prerequisite.id=dependencies.depends_on_task_id where dependencies.task_id=tasks.id and prerequisite.lifecycle<>'done')") {
		t.Fatalf("list query must derive unsatisfied dependencies inline: %s", normalized)
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

func createReadyTask(t *testing.T, store *Store, uid string, priority domain.Priority) domain.Task {
	t.Helper()
	now := time.Now().UTC()
	task, err := store.CreateTask(context.Background(), domain.Task{UID: uid, Title: uid, Lifecycle: domain.LifecycleReady, Priority: priority, CreatedAt: now, UpdatedAt: now, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestClaimOwnershipProgressReleaseAndHistory(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task := createReadyTask(t, s, "coordination", domain.PriorityHigh)
	owner := domain.AgentIdentity{AgentName: "codex", InstanceID: "one"}
	other := domain.AgentIdentity{AgentName: "claude", InstanceID: "two"}
	now := time.Now().UTC()
	view, err := s.ClaimTask(ctx, task.ID, owner, now)
	if err != nil || view.ActiveClaim == nil || *view.OperationalState != domain.OperationalWorking {
		t.Fatalf("claim=%+v err=%v", view, err)
	}
	repeated, err := s.ClaimTask(ctx, task.ID, owner, now.Add(time.Second))
	if err != nil || repeated.ActiveClaim.ID != view.ActiveClaim.ID {
		t.Fatalf("idempotent=%+v err=%v", repeated, err)
	}
	if _, err = s.ClaimTask(ctx, task.ID, other, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("other claim=%v", err)
	}
	if _, err = s.UpdateProgress(ctx, task.ID, 60, "building", other, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("other progress=%v", err)
	}
	view, err = s.UpdateProgress(ctx, task.ID, 60, "building", owner, now.Add(2*time.Second))
	if err != nil || view.Progress != 60 || view.Phase != "building" || view.Version != 2 {
		t.Fatalf("progress=%+v err=%v", view, err)
	}
	view, err = s.UpdateProgress(ctx, task.ID, 20, "reworking", owner, now.Add(3*time.Second))
	if err != nil || view.Progress != 20 {
		t.Fatalf("regression=%+v err=%v", view, err)
	}
	if _, err = s.ReleaseClaim(ctx, task.ID, other, "", now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("other release=%v", err)
	}
	view, err = s.ReleaseClaim(ctx, task.ID, owner, "handoff", now.Add(4*time.Second))
	if err != nil || view.ActiveClaim != nil || *view.OperationalState != domain.OperationalAvailable || view.Progress != 20 {
		t.Fatalf("release=%+v err=%v", view, err)
	}
	var released, events int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM claims WHERE task_id=? AND released_at IS NOT NULL`, task.ID).Scan(&released)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND actor_kind='agent'`, task.ID).Scan(&events)
	if released != 1 || events != 4 {
		t.Fatalf("released=%d events=%d", released, events)
	}
	view, err = s.ClaimTask(ctx, task.ID, other, now.Add(5*time.Second))
	if err != nil || view.ActiveClaim == nil || view.ActiveClaim.ID == repeated.ActiveClaim.ID {
		t.Fatalf("reclaim=%+v err=%v", view, err)
	}
	var history int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM claims WHERE task_id=?`, task.ID).Scan(&history); err != nil || history != 2 {
		t.Fatalf("history=%d err=%v", history, err)
	}
}

func TestAgentCompletionIsAtomic(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task := createReadyTask(t, s, "complete", domain.PriorityNormal)
	owner := domain.AgentIdentity{AgentName: "codex", InstanceID: "one"}
	now := time.Now().UTC()
	if _, err := s.ClaimTask(ctx, task.ID, owner, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_agent_completion BEFORE INSERT ON events WHEN NEW.kind='task_completed' AND NEW.actor_kind='agent' BEGIN SELECT RAISE(ABORT, 'reject completion'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteClaimedTask(ctx, task.ID, "summary", owner, now.Add(time.Second)); err == nil {
		t.Fatal("expected completion failure")
	}
	view, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Lifecycle != domain.LifecycleReady || view.Progress != 0 || view.CompletionSummary != "" || view.ActiveClaim == nil {
		t.Fatalf("partial completion=%+v", view)
	}
	if _, err = s.db.Exec(`DROP TRIGGER reject_agent_completion`); err != nil {
		t.Fatal(err)
	}
	view, err = s.CompleteClaimedTask(ctx, task.ID, "summary", owner, now.Add(2*time.Second))
	if err != nil || view.Lifecycle != domain.LifecycleDone || view.Progress != 100 || view.CompletionSummary != "summary" || view.ActiveClaim != nil {
		t.Fatalf("completion=%+v err=%v", view, err)
	}
}

func TestConcurrentClaims(t *testing.T) {
	for _, tc := range []struct {
		name        string
		next        bool
		tasks       int
		wantSuccess int
	}{{"explicit one task", false, 1, 1}, {"next one task", true, 1, 1}, {"next two tasks", true, 2, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "race.db")
			first, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close()
			second, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close()
			var target domain.Task
			for i := 0; i < tc.tasks; i++ {
				target = createReadyTask(t, first, fmt.Sprintf("race-%d", i), domain.PriorityUrgent)
			}
			stores := []*Store{first, second}
			start := make(chan struct{})
			results := make(chan struct {
				view domain.TaskView
				err  error
			}, 2)
			for i := 0; i < 2; i++ {
				go func(i int) {
					<-start
					identity := domain.AgentIdentity{AgentName: fmt.Sprintf("agent-%d", i), InstanceID: fmt.Sprintf("instance-%d", i)}
					var view domain.TaskView
					var err error
					if tc.next {
						view, err = stores[i].ClaimNext(context.Background(), identity, time.Now().UTC())
					} else {
						view, err = stores[i].ClaimTask(context.Background(), target.ID, identity, time.Now().UTC())
					}
					results <- struct {
						view domain.TaskView
						err  error
					}{view, err}
				}(i)
			}
			close(start)
			successes := 0
			ids := map[int64]bool{}
			for i := 0; i < 2; i++ {
				result := <-results
				if result.err == nil {
					successes++
					if ids[result.view.ID] {
						t.Fatalf("task %d claimed twice", result.view.ID)
					}
					ids[result.view.ID] = true
				} else if !errors.Is(result.err, domain.ErrConflict) && !errors.Is(result.err, domain.ErrNoEligible) {
					t.Fatalf("unexpected err=%v", result.err)
				}
			}
			if successes != tc.wantSuccess {
				t.Fatalf("successes=%d want=%d", successes, tc.wantSuccess)
			}
			var active, distinct int
			if err = first.db.QueryRow(`SELECT COUNT(*),COUNT(DISTINCT task_id) FROM claims WHERE released_at IS NULL`).Scan(&active, &distinct); err != nil || active != distinct || active != tc.wantSuccess {
				t.Fatalf("active=%d distinct=%d err=%v", active, distinct, err)
			}
		})
	}
}
