package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/alle80/griglia-tui/internal/domain"
)

func TestMigration005FromExistingV4Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v4.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for version, name := range []string{"001_initial.sql", "002_events.sql", "003_claims.sql", "004_questions.sql"} {
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
	if _, err = db.Exec(`INSERT INTO tasks(uid,title,description,lifecycle,priority,progress,phase,completion_summary,created_at,updated_at,version) VALUES('m5','Milestone 5 task','','ready','normal',0,'','',?,?,1)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO questions(task_id,body,blocking,asked_by_agent_name,asked_by_instance_id,asked_at) VALUES(1,'kept?',1,'codex','one',?)`, now); err != nil {
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
	questions, err := s.ListQuestions(context.Background(), 1, domain.QuestionsAll)
	if err != nil || len(questions) != 1 {
		t.Fatalf("existing questions=%v err=%v", questions, err)
	}
	dependencies, err := s.ListDependencies(context.Background(), 1)
	if err != nil || len(dependencies) != 0 {
		t.Fatalf("dependencies=%v err=%v", dependencies, err)
	}
}

func TestDependencyValidationAndIdempotency(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	a := createReadyTask(t, s, "a", domain.PriorityNormal)
	b := createReadyTask(t, s, "b", domain.PriorityNormal)

	if _, err := s.AddDependency(ctx, a.ID, a.ID, now); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("self edge err=%v", err)
	}
	if _, err := s.AddDependency(ctx, 999, b.ID, now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing task err=%v", err)
	}
	if _, err := s.AddDependency(ctx, a.ID, 999, now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing prerequisite err=%v", err)
	}

	d, err := s.AddDependency(ctx, a.ID, b.ID, now)
	if err != nil || d.TaskID != a.ID || d.DependsOnTaskID != b.ID || d.Satisfied() {
		t.Fatalf("dependency=%+v err=%v", d, err)
	}
	// Duplicate adds are idempotent and write no second event.
	repeat, err := s.AddDependency(ctx, a.ID, b.ID, now.Add(time.Second))
	if err != nil || formatTime(repeat.CreatedAt) != formatTime(now) {
		t.Fatalf("duplicate=%+v err=%v", repeat, err)
	}
	var added int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind='dependency_added'`).Scan(&added); err != nil || added != 1 {
		t.Fatalf("added events=%d err=%v", added, err)
	}

	// Removal is idempotent as well; only real removals write events.
	if err = s.RemoveDependency(ctx, a.ID, b.ID, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = s.RemoveDependency(ctx, a.ID, b.ID, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = s.RemoveDependency(ctx, 999, b.ID, now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing task remove err=%v", err)
	}
	var removed int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind='dependency_removed'`).Scan(&removed); err != nil || removed != 1 {
		t.Fatalf("removed events=%d err=%v", removed, err)
	}
	dependencies, err := s.ListDependencies(ctx, a.ID)
	if err != nil || len(dependencies) != 0 {
		t.Fatalf("dependencies=%v err=%v", dependencies, err)
	}
	if _, err = s.ListDependencies(ctx, 999); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("list missing task err=%v", err)
	}
}

func TestDependencyCycleRejection(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	a := createReadyTask(t, s, "a", domain.PriorityNormal)
	b := createReadyTask(t, s, "b", domain.PriorityNormal)
	c := createReadyTask(t, s, "c", domain.PriorityNormal)
	d := createReadyTask(t, s, "d", domain.PriorityNormal)

	if _, err := s.AddDependency(ctx, a.ID, b.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDependency(ctx, b.ID, c.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDependency(ctx, c.ID, a.ID, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("three-node cycle err=%v", err)
	}
	if _, err := s.AddDependency(ctx, c.ID, d.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDependency(ctx, d.ID, a.ID, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("four-node cycle err=%v", err)
	}
	var edges int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM dependencies`).Scan(&edges); err != nil || edges != 3 {
		t.Fatalf("edges=%d err=%v", edges, err)
	}
}

func TestDependencyAuditAtomicity(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	a := createReadyTask(t, s, "a", domain.PriorityNormal)
	b := createReadyTask(t, s, "b", domain.PriorityNormal)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_dependency BEFORE INSERT ON events WHEN NEW.kind='dependency_added' BEGIN SELECT RAISE(ABORT, 'reject dependency'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDependency(ctx, a.ID, b.ID, now); err == nil {
		t.Fatal("expected audit failure")
	}
	var edges int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM dependencies`).Scan(&edges); err != nil || edges != 0 {
		t.Fatalf("edge persisted despite audit failure: %d err=%v", edges, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_dependency`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDependency(ctx, a.ID, b.ID, now); err != nil {
		t.Fatal(err)
	}
}

func TestBlockedStateAndSatisfactionSemantics(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	owner := domain.AgentIdentity{AgentName: "codex", InstanceID: "one"}
	dependent := createReadyTask(t, s, "dependent", domain.PriorityUrgent)
	done := createReadyTask(t, s, "done", domain.PriorityNormal)
	cancelled := createReadyTask(t, s, "cancelled", domain.PriorityNormal)

	if _, err := s.ClaimTask(ctx, done.ID, owner, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteClaimedTask(ctx, done.ID, "ok", owner, now); err != nil {
		t.Fatal(err)
	}
	view, err := s.GetTask(ctx, cancelled.ID)
	if err != nil {
		t.Fatal(err)
	}
	task := view.Task
	task.Lifecycle, task.UpdatedAt, task.Version = domain.LifecycleCancelled, now, task.Version+1
	if _, err = s.TransitionTask(ctx, task, view.Version, "dropped"); err != nil {
		t.Fatal(err)
	}

	// A done prerequisite satisfies; the task stays available.
	if _, err = s.AddDependency(ctx, dependent.ID, done.ID, now); err != nil {
		t.Fatal(err)
	}
	view, err = s.GetTask(ctx, dependent.ID)
	if err != nil || *view.OperationalState != domain.OperationalAvailable {
		t.Fatalf("satisfied view=%+v err=%v", view, err)
	}

	// A cancelled prerequisite is unsatisfied and blocks.
	if _, err = s.AddDependency(ctx, dependent.ID, cancelled.ID, now); err != nil {
		t.Fatal(err)
	}
	view, err = s.GetTask(ctx, dependent.ID)
	if err != nil || *view.OperationalState != domain.OperationalBlocked {
		t.Fatalf("blocked view=%+v err=%v", view, err)
	}

	// Blocked tasks cannot be claimed explicitly.
	if _, err = s.ClaimTask(ctx, dependent.ID, owner, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("claim blocked err=%v", err)
	}

	list, err := s.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range list {
		if v.ID == dependent.ID && *v.OperationalState != domain.OperationalBlocked {
			t.Fatalf("list state=%v", v.OperationalState)
		}
	}
	dependencies, err := s.ListDependencies(ctx, dependent.ID)
	if err != nil || len(dependencies) != 2 {
		t.Fatalf("dependencies=%v err=%v", dependencies, err)
	}
	for _, d := range dependencies {
		if d.DependsOnTaskID == done.ID && !d.Satisfied() {
			t.Fatalf("done prerequisite unsatisfied: %+v", d)
		}
		if d.DependsOnTaskID == cancelled.ID && d.Satisfied() {
			t.Fatalf("cancelled prerequisite satisfied: %+v", d)
		}
	}
}

func TestClaimNextSkipsBlockedAndPrerequisiteCompletionUnblocks(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	owner := domain.AgentIdentity{AgentName: "codex", InstanceID: "session-a"}
	schema := createReadyTask(t, s, "Schema", domain.PriorityHigh)
	backend := createReadyTask(t, s, "Backend", domain.PriorityUrgent)
	if _, err := s.AddDependency(ctx, backend.ID, schema.ID, now); err != nil {
		t.Fatal(err)
	}

	// claim-next must skip the urgent blocked task and pick the high one.
	view, err := s.ClaimNext(ctx, owner, now)
	if err != nil || view.ID != schema.ID {
		t.Fatalf("claim-next view=%+v err=%v", view, err)
	}
	if _, err = s.CompleteClaimedTask(ctx, schema.ID, "done", owner, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	// The dependent becomes available and claimable with no extra mutation.
	view, err = s.GetTask(ctx, backend.ID)
	if err != nil || *view.OperationalState != domain.OperationalAvailable {
		t.Fatalf("unblocked view=%+v err=%v", view, err)
	}
	view, err = s.ClaimNext(ctx, owner, now.Add(2*time.Second))
	if err != nil || view.ID != backend.ID || *view.OperationalState != domain.OperationalWorking {
		t.Fatalf("claim unblocked=%+v err=%v", view, err)
	}
}

func TestClaimedTaskDependencyMutationConflict(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	owner := domain.AgentIdentity{AgentName: "codex", InstanceID: "one"}
	claimed := createReadyTask(t, s, "claimed", domain.PriorityNormal)
	free := createReadyTask(t, s, "free", domain.PriorityNormal)
	if _, err := s.AddDependency(ctx, claimed.ID, free.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimTask(ctx, claimed.ID, owner, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("claim blocked err=%v", err)
	}
	if err := s.RemoveDependency(ctx, claimed.ID, free.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimTask(ctx, claimed.ID, owner, now); err != nil {
		t.Fatal(err)
	}
	// Claimed target: neither adds nor removals are allowed.
	if _, err := s.AddDependency(ctx, claimed.ID, free.ID, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("add on claimed err=%v", err)
	}
	if err := s.RemoveDependency(ctx, claimed.ID, free.ID, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("remove on claimed err=%v", err)
	}
	// The prerequisite side may still change: free is unclaimed.
	if _, err := s.AddDependency(ctx, free.ID, claimed.ID, now); err != nil {
		t.Fatalf("prerequisite side add err=%v", err)
	}
}

func TestOppositeEdgeRace(t *testing.T) {
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
	ctx := context.Background()
	a := createReadyTask(t, first, "a", domain.PriorityNormal)
	b := createReadyTask(t, first, "b", domain.PriorityNormal)

	stores := []*Store{first, second}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			<-start
			var e error
			if i == 0 {
				_, e = stores[i].AddDependency(ctx, a.ID, b.ID, time.Now().UTC())
			} else {
				_, e = stores[i].AddDependency(ctx, b.ID, a.ID, time.Now().UTC())
			}
			results <- e
		}(i)
	}
	close(start)
	failures := 0
	for i := 0; i < 2; i++ {
		if e := <-results; e != nil {
			if !errors.Is(e, domain.ErrConflict) {
				t.Fatalf("unexpected race err=%v", e)
			}
			failures++
		}
	}
	if failures != 1 {
		t.Fatalf("exactly one writer must lose, failures=%d", failures)
	}
	var edges int
	if err = first.db.QueryRow(`SELECT COUNT(*) FROM dependencies`).Scan(&edges); err != nil || edges != 1 {
		t.Fatalf("edges=%d err=%v", edges, err)
	}
}

func TestClaimVersusDependencyRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claimrace.db")
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
	ctx := context.Background()
	target := createReadyTask(t, first, "target", domain.PriorityNormal)
	prerequisite := createReadyTask(t, first, "prerequisite", domain.PriorityNormal)
	owner := domain.AgentIdentity{AgentName: "codex", InstanceID: "one"}

	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, e := first.ClaimTask(ctx, target.ID, owner, time.Now().UTC())
		results <- e
	}()
	go func() {
		<-start
		_, e := second.AddDependency(ctx, target.ID, prerequisite.ID, time.Now().UTC())
		results <- e
	}()
	close(start)
	for i := 0; i < 2; i++ {
		if e := <-results; e != nil && !errors.Is(e, domain.ErrConflict) {
			t.Fatalf("unexpected err=%v", e)
		}
	}
	// Whatever the interleaving, the forbidden final state is an active
	// claim together with a newly introduced unsatisfied dependency.
	var claimed, blocked int
	if err = first.db.QueryRow(`SELECT COUNT(*) FROM claims WHERE task_id=? AND released_at IS NULL`, target.ID).Scan(&claimed); err != nil {
		t.Fatal(err)
	}
	if err = first.db.QueryRow(`SELECT COUNT(*) FROM dependencies WHERE task_id=?`, target.ID).Scan(&blocked); err != nil {
		t.Fatal(err)
	}
	if claimed == 1 && blocked == 1 {
		t.Fatal("claimed task acquired an unsatisfied dependency")
	}
}
