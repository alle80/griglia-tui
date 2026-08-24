package sqlite

// Migration hardening: every historical schema version must upgrade to the
// latest schema without losing data, reopen cleanly, and keep the recorded
// migration order and checksums deterministic. Shipped migrations are never
// rewritten, so each historical state is reproduced by applying the shipped
// files up to that version.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alle80/griglia-tui/internal/domain"
)

var shippedMigrations = []string{
	"001_initial.sql",
	"002_events.sql",
	"003_claims.sql",
	"004_questions.sql",
	"005_dependencies.sql",
	"006_workspaces.sql",
}

const latestSchemaVersion = 6

// buildHistoricalDatabase creates a database exactly as a binary shipped at
// schema version upTo would have left it, including recorded checksums.
func buildHistoricalDatabase(t *testing.T, path string, upTo int) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for index, name := range shippedMigrations[:upTo] {
		body, readErr := migrationFiles.ReadFile("migrations/" + name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(body))
		if _, err = db.Exec(`INSERT INTO schema_migrations(version,checksum,applied_at) VALUES(?,?,?)`, index+1, checksum, formatTime(time.Now().UTC())); err != nil {
			t.Fatal(err)
		}
	}
	seedHistoricalData(t, db, upTo)
}

// seedHistoricalData inserts realistic rows using only the tables that
// existed at the given schema version.
func seedHistoricalData(t *testing.T, db *sql.DB, version int) {
	t.Helper()
	now := formatTime(time.Now().UTC())
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	mustExec(`INSERT INTO projects(id,name,created_at) VALUES('p-1','Legacy project',?)`, now)
	mustExec(`INSERT INTO tasks(uid,title,description,lifecycle,priority,progress,phase,completion_summary,created_at,updated_at,version) VALUES('t-backlog','Backlog task','notes','backlog','low',0,'','',?,?,1)`, now, now)
	mustExec(`INSERT INTO tasks(uid,title,description,lifecycle,priority,progress,phase,completion_summary,created_at,updated_at,version) VALUES('t-ready','Ready task','','ready','urgent',40,'building','',?,?,3)`, now, now)
	mustExec(`INSERT INTO tasks(uid,title,description,lifecycle,priority,progress,phase,completion_summary,created_at,updated_at,completed_at,version) VALUES('t-done','Done task','','done','normal',100,'','shipped',?,?,?,4)`, now, now, now)
	mustExec(`INSERT INTO tasks(uid,title,description,lifecycle,priority,progress,phase,completion_summary,created_at,updated_at,cancelled_at,version) VALUES('t-cancelled','Cancelled task','','cancelled','high',10,'','',?,?,?,2)`, now, now, now)
	if version >= 2 {
		mustExec(`INSERT INTO events(task_id,kind,actor_kind,actor_name,payload_json,created_at) VALUES(2,'task_created','human','','{}',?)`, now)
		mustExec(`INSERT INTO events(task_id,kind,actor_kind,actor_name,payload_json,created_at) VALUES(2,'task_ready','human','','{}',?)`, now)
	}
	if version >= 3 {
		mustExec(`INSERT INTO claims(task_id,agent_name,instance_id,claimed_at,last_activity_at,released_at,release_reason) VALUES(2,'codex','session-1',?,?,?,'handoff')`, now, now, now)
		mustExec(`INSERT INTO claims(task_id,agent_name,instance_id,claimed_at,last_activity_at) VALUES(2,'claude','session-2',?,?)`, now, now)
	}
	if version >= 4 {
		mustExec(`INSERT INTO questions(task_id,body,blocking,asked_by_agent_name,asked_by_instance_id,asked_at,answer,answered_at,acknowledged_at) VALUES(2,'Which DB?',0,'claude','session-2',?,'SQLite',?,?)`, now, now, now)
		mustExec(`INSERT INTO questions(task_id,body,blocking,asked_by_agent_name,asked_by_instance_id,asked_at) VALUES(2,'Blocking one?',1,'claude','session-2',?)`, now)
	}
	if version >= 5 {
		mustExec(`INSERT INTO dependencies(task_id,depends_on_task_id,created_at) VALUES(1,3,?)`, now)
	}
	if version >= 6 {
		mustExec(`INSERT INTO workspaces(task_id,state,path,branch,base_commit,created_by_agent,created_by_instance,created_at,updated_at) VALUES(2,'ready','/tmp/wt/task-2','griglia/task-2-ready','abc123','claude','session-2',?,?)`, now, now)
	}
}

func TestMigrationMatrixPreservesRealisticData(t *testing.T) {
	for from := 1; from <= latestSchemaVersion-1; from++ {
		t.Run(fmt.Sprintf("v%d to latest", from), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "historical.db")
			buildHistoricalDatabase(t, path, from)

			s, err := Open(path)
			if err != nil {
				t.Fatalf("migrate from v%d: %v", from, err)
			}
			verifyMigratedData(t, s, from)
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}

			// Reopening must be a no-op migration with identical results.
			s, err = Open(path)
			if err != nil {
				t.Fatalf("reopen after migration: %v", err)
			}
			defer s.Close()
			verifyMigratedData(t, s, from)
			verifyMigrationLedger(t, s)
		})
	}
}

func verifyMigratedData(t *testing.T, s *Store, from int) {
	t.Helper()
	ctx := context.Background()

	var projects int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM projects WHERE name='Legacy project'`).Scan(&projects); err != nil || projects != 1 {
		t.Fatalf("projects=%d err=%v", projects, err)
	}

	views, err := s.ListTasks(ctx)
	if err != nil || len(views) != 4 {
		t.Fatalf("tasks=%d err=%v", len(views), err)
	}
	byUID := map[string]domain.TaskView{}
	for _, view := range views {
		byUID[view.UID] = view
	}
	ready := byUID["t-ready"]
	if ready.Lifecycle != domain.LifecycleReady || ready.Progress != 40 || ready.Phase != "building" || ready.Version != 3 {
		t.Fatalf("ready task=%+v", ready.Task)
	}
	if done := byUID["t-done"]; done.Lifecycle != domain.LifecycleDone || done.CompletedAt == nil || done.CompletionSummary != "shipped" {
		t.Fatalf("done task=%+v", done.Task)
	}
	if cancelled := byUID["t-cancelled"]; cancelled.CancelledAt == nil || cancelled.Version != 2 {
		t.Fatalf("cancelled task=%+v", cancelled.Task)
	}

	if from >= 2 {
		// Scoped to the seeded kinds: post-migration write checks below add
		// their own events to this task on every verification pass.
		var events int
		if err = s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=2 AND kind IN ('task_created','task_ready')`).Scan(&events); err != nil || events != 2 {
			t.Fatalf("events=%d err=%v", events, err)
		}
	}
	if from >= 3 {
		var total, active int
		if err = s.db.QueryRow(`SELECT COUNT(*), COUNT(*) FILTER (WHERE released_at IS NULL) FROM claims WHERE task_id=2`).Scan(&total, &active); err != nil || total != 2 || active != 1 {
			t.Fatalf("claims total=%d active=%d err=%v", total, active, err)
		}
		if ready.ActiveClaim == nil || ready.ActiveClaim.AgentName != "claude" || ready.ActiveClaim.InstanceID != "session-2" {
			t.Fatalf("active claim=%+v", ready.ActiveClaim)
		}
	}
	if from >= 4 {
		questions, listErr := s.ListQuestions(ctx, 2, domain.QuestionsAll)
		if listErr != nil || len(questions) != 2 {
			t.Fatalf("questions=%v err=%v", questions, listErr)
		}
		if !questions[0].Answered() || !questions[0].Acknowledged() || *questions[0].Answer != "SQLite" {
			t.Fatalf("answered question=%+v", questions[0])
		}
		if questions[1].Answered() || !questions[1].Blocking {
			t.Fatalf("blocking question=%+v", questions[1])
		}
		if ready.OperationalState == nil || *ready.OperationalState != domain.OperationalWaitingForHuman {
			t.Fatalf("ready state=%v", ready.OperationalState)
		}
	}

	// Dependencies always exist after migration; edges pre-exist from v5 on.
	dependencies, err := s.ListDependencies(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	wantEdges := 0
	if from >= 5 {
		wantEdges = 1
	}
	if len(dependencies) != wantEdges {
		t.Fatalf("migrated edges=%v want=%d", dependencies, wantEdges)
	}

	// The migrated schema must accept new writes in every subsystem.
	now := time.Now().UTC()
	if _, err = s.AddDependency(ctx, 4, 3, now); err != nil {
		t.Fatalf("post-migration dependency write: %v", err)
	}
	if err = s.RemoveDependency(ctx, 4, 3, now); err != nil {
		t.Fatalf("post-migration dependency delete: %v", err)
	}

	verifyWorkspaceWritesAfterMigration(t, s, from)
}

// verifyWorkspaceWritesAfterMigration proves the migrated schema accepts the
// full workspace allocation cycle. It ends with the workspace removed so the
// post-reopen verification pass can allocate again.
func verifyWorkspaceWritesAfterMigration(t *testing.T, s *Store, from int) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	identity := domain.AgentIdentity{AgentName: "claude", InstanceID: "session-2"}
	if from < 3 {
		// No seeded claim exists before v3; reserving requires the owner.
		if _, err := s.ClaimTask(ctx, 2, identity, now); err != nil {
			t.Fatalf("post-migration claim: %v", err)
		}
	}
	w, err := s.ReserveWorkspace(ctx, 2, "/tmp/wt/migrated-task-2", "griglia/task-2-migrated", "abc123", identity, now)
	if err != nil {
		t.Fatalf("post-migration workspace reserve: %v", err)
	}
	if _, err = s.MarkWorkspaceReady(ctx, w.ID, now); err != nil {
		t.Fatalf("post-migration workspace ready: %v", err)
	}
	live, err := s.LiveWorkspaceForTask(ctx, 2)
	if err != nil || live == nil || live.State != domain.WorkspaceReady {
		t.Fatalf("post-migration live workspace=%+v err=%v", live, err)
	}
	if _, err = s.RemoveWorkspace(ctx, w.ID, now); err != nil {
		t.Fatalf("post-migration workspace remove: %v", err)
	}
}

func verifyMigrationLedger(t *testing.T, s *Store) {
	t.Helper()
	rows, err := s.db.Query(`SELECT version, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	next := 1
	for rows.Next() {
		var version int
		var checksum string
		if err = rows.Scan(&version, &checksum); err != nil {
			t.Fatal(err)
		}
		if version != next {
			t.Fatalf("migration order broken: got %d want %d", version, next)
		}
		body, readErr := migrationFiles.ReadFile("migrations/" + shippedMigrations[version-1])
		if readErr != nil {
			t.Fatal(readErr)
		}
		if want := fmt.Sprintf("%x", sha256.Sum256(body)); checksum != want {
			t.Fatalf("migration %d checksum drifted", version)
		}
		next++
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if next != latestSchemaVersion+1 {
		t.Fatalf("recorded migrations=%d want=%d", next-1, latestSchemaVersion)
	}
}

// A fresh database must apply every shipped migration in order, record the
// full checksummed ledger, and accept writes in every subsystem, including
// the workspace cycle introduced by migration 006.
func TestFreshDatabaseMigratesToLatest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	verifyMigrationLedger(t, s)
	ctx := context.Background()
	now := time.Now().UTC()
	task := createReadyTask(t, s, "fresh-task", domain.PriorityNormal)
	identity := domain.AgentIdentity{AgentName: "claude", InstanceID: "fresh-1"}
	if _, err = s.ClaimTask(ctx, task.ID, identity, now); err != nil {
		t.Fatal(err)
	}
	w, err := s.ReserveWorkspace(ctx, task.ID, "/tmp/wt/fresh", "griglia/fresh", "abc123", identity, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.MarkWorkspaceReady(ctx, w.ID, now); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopening is a no-op migration that preserves the workspace.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	verifyMigrationLedger(t, s)
	live, err := s.LiveWorkspaceForTask(ctx, task.ID)
	if err != nil || live == nil || live.State != domain.WorkspaceReady || live.Branch != "griglia/fresh" {
		t.Fatalf("live=%+v err=%v", live, err)
	}
}

func TestMigrationChecksumMismatchIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tampered.db")
	buildHistoricalDatabase(t, path, 2)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE schema_migrations SET checksum='deadbeef' WHERE version=1`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(path); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("tampered migration must be rejected, got err=%v", err)
	}
}

func TestNewerDatabaseSchemaIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO schema_migrations(version,checksum,applied_at) VALUES(?,?,?)`, latestSchemaVersion+1, "future", formatTime(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(path); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("newer schema must be rejected, got err=%v", err)
	}
}
