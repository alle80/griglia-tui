package sqlite

// Workspace persistence invariants (docs/WORKSPACES.md): the row models the
// Git resource lifecycle only, the partial unique indexes make allocation
// race-safe, and claim transitions never mutate workspace rows. Real SQLite
// handles, no mocks.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/alle80/griglia-tui/internal/domain"
)

func claimedReadyTask(t *testing.T, s *Store, uid string, identity domain.AgentIdentity) domain.Task {
	t.Helper()
	task := createReadyTask(t, s, uid, domain.PriorityNormal)
	if _, err := s.ClaimTask(context.Background(), task.ID, identity, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	return task
}

// workspaceRow reads the raw persisted row so tests can prove byte-for-byte
// that an operation did or did not touch it.
func workspaceRow(t *testing.T, s *Store, id int64) string {
	t.Helper()
	var row string
	err := s.db.QueryRow(`SELECT task_id||'|'||state||'|'||path||'|'||branch||'|'||base_commit||'|'||created_by_agent||'|'||created_by_instance||'|'||created_at||'|'||updated_at||'|'||COALESCE(removed_at,'')||'|'||error FROM workspaces WHERE id=?`, id).Scan(&row)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func countEvents(t *testing.T, s *Store, taskID int64, kind string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND kind=?`, taskID, kind).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestWorkspaceLifecyclePersistsResourceFacts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner := domain.AgentIdentity{AgentName: "claude", InstanceID: "ws-1"}
	task := claimedReadyTask(t, s, "ws-lifecycle", owner)
	now := time.Now().UTC()

	w, err := s.ReserveWorkspace(ctx, task.ID, "/tmp/wt/task-1", "griglia/task-1-ws-lifecycle", "abc123", owner, now)
	if err != nil {
		t.Fatal(err)
	}
	if w.State != domain.WorkspaceAllocating || w.Path != "/tmp/wt/task-1" || w.Branch != "griglia/task-1-ws-lifecycle" || w.BaseCommit != "abc123" || w.CreatedBy != owner {
		t.Fatalf("reserved=%+v", w)
	}
	live, err := s.LiveWorkspaceForTask(ctx, task.ID)
	if err != nil || live == nil || live.ID != w.ID || live.State != domain.WorkspaceAllocating {
		t.Fatalf("live=%+v err=%v", live, err)
	}

	ready, err := s.MarkWorkspaceReady(ctx, w.ID, now.Add(time.Second))
	if err != nil || ready.State != domain.WorkspaceReady || ready.Error != "" {
		t.Fatalf("ready=%+v err=%v", ready, err)
	}
	live, err = s.LiveWorkspaceForTask(ctx, task.ID)
	if err != nil || live == nil || live.State != domain.WorkspaceReady {
		t.Fatalf("live after ready=%+v err=%v", live, err)
	}

	removed, err := s.RemoveWorkspace(ctx, w.ID, now.Add(2*time.Second))
	if err != nil || removed.State != domain.WorkspaceRemoved || removed.RemovedAt == nil {
		t.Fatalf("removed=%+v err=%v", removed, err)
	}
	live, err = s.LiveWorkspaceForTask(ctx, task.ID)
	if err != nil || live != nil {
		t.Fatalf("live after remove=%+v err=%v", live, err)
	}

	all, err := s.ListWorkspaces(ctx)
	if err != nil || len(all) != 1 || all[0].State != domain.WorkspaceRemoved {
		t.Fatalf("history=%+v err=%v", all, err)
	}
	for _, kind := range []string{"workspace_allocating", "workspace_ready", "workspace_removed"} {
		if n := countEvents(t, s, task.ID, kind); n != 1 {
			t.Fatalf("%s events=%d", kind, n)
		}
	}
}

func TestReserveWorkspaceRequiresActiveClaimOwner(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner := domain.AgentIdentity{AgentName: "claude", InstanceID: "ws-owner"}
	other := domain.AgentIdentity{AgentName: "codex", InstanceID: "ws-other"}
	now := time.Now().UTC()

	unclaimed := createReadyTask(t, s, "ws-unclaimed", domain.PriorityNormal)
	if _, err := s.ReserveWorkspace(ctx, unclaimed.ID, "/tmp/wt/u", "griglia/u", "", owner, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("unclaimed err=%v", err)
	}

	claimed := claimedReadyTask(t, s, "ws-claimed", owner)
	if _, err := s.ReserveWorkspace(ctx, claimed.ID, "/tmp/wt/c", "griglia/c", "", other, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("non-owner err=%v", err)
	}
	if _, err := s.ReserveWorkspace(ctx, 999, "/tmp/wt/n", "griglia/n", "", owner, now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing task err=%v", err)
	}
	if _, err := s.ReserveWorkspace(ctx, claimed.ID, "  ", "griglia/c", "", owner, now); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("blank path err=%v", err)
	}
	if _, err := s.ReserveWorkspace(ctx, claimed.ID, "/tmp/wt/c", "", "", owner, now); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("blank branch err=%v", err)
	}

	// No failed attempt may leave a row or an event behind.
	if all, err := s.ListWorkspaces(ctx); err != nil || len(all) != 0 {
		t.Fatalf("rows=%+v err=%v", all, err)
	}
	for _, id := range []int64{unclaimed.ID, claimed.ID} {
		if n := countEvents(t, s, id, "workspace_allocating"); n != 0 {
			t.Fatalf("task %d allocating events=%d", id, n)
		}
	}
}

func TestSecondLiveReservationForTaskConflicts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner := domain.AgentIdentity{AgentName: "claude", InstanceID: "ws-dup"}
	task := claimedReadyTask(t, s, "ws-dup", owner)
	now := time.Now().UTC()
	if _, err := s.ReserveWorkspace(ctx, task.ID, "/tmp/wt/dup", "griglia/dup", "", owner, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveWorkspace(ctx, task.ID, "/tmp/wt/dup-2", "griglia/dup-2", "", owner, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second reservation err=%v", err)
	}
}

// raceReservations runs one ReserveWorkspace per prepared argument set
// concurrently over independent connections and requires exactly one winner;
// every loser must observe the stable conflict error.
func raceReservations(t *testing.T, stores []*Store, reserve func(i int) (domain.Workspace, error)) domain.Workspace {
	t.Helper()
	start := make(chan struct{})
	type result struct {
		w   domain.Workspace
		err error
	}
	results := make(chan result, len(stores))
	for i := range stores {
		go func(i int) {
			<-start
			w, err := reserve(i)
			results <- result{w, err}
		}(i)
	}
	close(start)
	var winner domain.Workspace
	wins := 0
	for range stores {
		r := <-results
		switch {
		case r.err == nil:
			winner, wins = r.w, wins+1
		case errors.Is(r.err, domain.ErrConflict):
		default:
			t.Fatalf("unexpected err=%v", r.err)
		}
	}
	if wins != 1 {
		t.Fatalf("wins=%d", wins)
	}
	return winner
}

func TestConcurrentReservationsSameTaskAllocateOnce(t *testing.T) {
	stores := openSharedStores(t, 2)
	owner := domain.AgentIdentity{AgentName: "claude", InstanceID: "ws-race"}
	task := claimedReadyTask(t, stores[0], "ws-race-task", owner)
	raceReservations(t, stores, func(i int) (domain.Workspace, error) {
		return stores[i].ReserveWorkspace(context.Background(), task.ID, fmt.Sprintf("/tmp/wt/race-%d", i), fmt.Sprintf("griglia/race-%d", i), "", owner, time.Now().UTC())
	})
	var liveRows, events int
	if err := stores[0].db.QueryRow(`SELECT COUNT(*) FROM workspaces WHERE task_id=? AND state IN ('allocating','ready')`, task.ID).Scan(&liveRows); err != nil || liveRows != 1 {
		t.Fatalf("live=%d err=%v", liveRows, err)
	}
	if err := stores[0].db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND kind='workspace_allocating'`, task.ID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("events=%d err=%v", events, err)
	}
}

func TestConcurrentReservationsCannotShareALivePathOrBranch(t *testing.T) {
	for _, tc := range []struct {
		name            string
		paths, branches [2]string
	}{
		{"path collision", [2]string{"/tmp/wt/shared", "/tmp/wt/shared"}, [2]string{"griglia/a", "griglia/b"}},
		{"branch collision", [2]string{"/tmp/wt/a", "/tmp/wt/b"}, [2]string{"griglia/shared", "griglia/shared"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stores := openSharedStores(t, 2)
			identities := [2]domain.AgentIdentity{{AgentName: "claude", InstanceID: "ws-c1"}, {AgentName: "codex", InstanceID: "ws-c2"}}
			tasks := [2]domain.Task{claimedReadyTask(t, stores[0], "ws-col-1", identities[0]), claimedReadyTask(t, stores[1], "ws-col-2", identities[1])}
			raceReservations(t, stores, func(i int) (domain.Workspace, error) {
				return stores[i].ReserveWorkspace(context.Background(), tasks[i].ID, tc.paths[i], tc.branches[i], "", identities[i], time.Now().UTC())
			})
			var live int
			if err := stores[0].db.QueryRow(`SELECT COUNT(*) FROM workspaces WHERE state IN ('allocating','ready')`).Scan(&live); err != nil || live != 1 {
				t.Fatalf("live=%d err=%v", live, err)
			}
		})
	}
}

func TestFailedAllocationAllowsRetryWithSameFacts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner := domain.AgentIdentity{AgentName: "claude", InstanceID: "ws-retry"}
	task := claimedReadyTask(t, s, "ws-retry", owner)
	now := time.Now().UTC()

	first, err := s.ReserveWorkspace(ctx, task.ID, "/tmp/wt/retry", "griglia/retry", "", owner, now)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := s.MarkWorkspaceFailed(ctx, first.ID, "fatal: branch already exists", now.Add(time.Second))
	if err != nil || failed.State != domain.WorkspaceFailed || failed.Error != "fatal: branch already exists" {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	if live, liveErr := s.LiveWorkspaceForTask(ctx, task.ID); liveErr != nil || live != nil {
		t.Fatalf("failed row must not stay live: %+v err=%v", live, liveErr)
	}

	second, err := s.ReserveWorkspace(ctx, task.ID, "/tmp/wt/retry", "griglia/retry", "", owner, now.Add(2*time.Second))
	if err != nil || second.ID == first.ID {
		t.Fatalf("retry=%+v err=%v", second, err)
	}
	if _, err = s.MarkWorkspaceReady(ctx, second.ID, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if all, listErr := s.ListWorkspaces(ctx); listErr != nil || len(all) != 2 || all[0].State != domain.WorkspaceFailed || all[1].State != domain.WorkspaceReady {
		t.Fatalf("history=%+v err=%v", all, listErr)
	}
	if n := countEvents(t, s, task.ID, "workspace_failed"); n != 1 {
		t.Fatalf("failed events=%d", n)
	}
}

func TestRemovedWorkspaceAllowsLaterAllocation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner := domain.AgentIdentity{AgentName: "claude", InstanceID: "ws-realloc"}
	task := claimedReadyTask(t, s, "ws-realloc", owner)
	now := time.Now().UTC()

	first, err := s.ReserveWorkspace(ctx, task.ID, "/tmp/wt/realloc", "griglia/realloc", "", owner, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.MarkWorkspaceReady(ctx, first.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RemoveWorkspace(ctx, first.ID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	second, err := s.ReserveWorkspace(ctx, task.ID, "/tmp/wt/realloc", "griglia/realloc", "", owner, now.Add(2*time.Second))
	if err != nil || second.ID == first.ID {
		t.Fatalf("reallocation=%+v err=%v", second, err)
	}
}

func TestAllocationTransitionsAreGuarded(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner := domain.AgentIdentity{AgentName: "claude", InstanceID: "ws-guard"}
	task := claimedReadyTask(t, s, "ws-guard", owner)
	now := time.Now().UTC()

	w, err := s.ReserveWorkspace(ctx, task.ID, "/tmp/wt/guard", "griglia/guard", "", owner, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.MarkWorkspaceReady(ctx, w.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.MarkWorkspaceReady(ctx, w.ID, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("ready twice err=%v", err)
	}
	if _, err = s.MarkWorkspaceFailed(ctx, w.ID, "late failure", now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("fail after ready err=%v", err)
	}
	if _, err = s.MarkWorkspaceReady(ctx, 999, now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing workspace err=%v", err)
	}

	// Removing an already removed workspace is idempotent and writes no event.
	if _, err = s.RemoveWorkspace(ctx, w.ID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	again, err := s.RemoveWorkspace(ctx, w.ID, now.Add(2*time.Second))
	if err != nil || again.State != domain.WorkspaceRemoved {
		t.Fatalf("idempotent remove=%+v err=%v", again, err)
	}
	if n := countEvents(t, s, task.ID, "workspace_removed"); n != 1 {
		t.Fatalf("removed events=%d", n)
	}

	// A stuck allocating row (crash between phases) may be removed directly.
	stuck, err := s.ReserveWorkspace(ctx, task.ID, "/tmp/wt/stuck", "griglia/stuck", "", owner, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RemoveWorkspace(ctx, stuck.ID, now.Add(4*time.Second)); err != nil {
		t.Fatalf("remove stuck allocating err=%v", err)
	}
}

// Claim transitions must never mutate workspace rows: a ready workspace
// stays byte-for-byte identical through progress, release, re-claim by a
// different identity, completion, and cancellation.
func TestClaimTransitionsNeverTouchWorkspaceRows(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner := domain.AgentIdentity{AgentName: "claude", InstanceID: "ws-decouple-1"}
	other := domain.AgentIdentity{AgentName: "codex", InstanceID: "ws-decouple-2"}
	now := time.Now().UTC()

	done := claimedReadyTask(t, s, "ws-done", owner)
	w, err := s.ReserveWorkspace(ctx, done.ID, "/tmp/wt/done", "griglia/done", "", owner, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.MarkWorkspaceReady(ctx, w.ID, now); err != nil {
		t.Fatal(err)
	}
	before := workspaceRow(t, s, w.ID)

	if _, err = s.UpdateProgress(ctx, done.ID, 50, "halfway", owner, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := workspaceRow(t, s, w.ID); got != before {
		t.Fatalf("progress mutated workspace:\n%s\n%s", before, got)
	}
	if _, err = s.ReleaseClaim(ctx, done.ID, owner, "handoff", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := workspaceRow(t, s, w.ID); got != before {
		t.Fatalf("release mutated workspace:\n%s\n%s", before, got)
	}
	if _, err = s.ClaimTask(ctx, done.ID, other, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := workspaceRow(t, s, w.ID); got != before {
		t.Fatalf("re-claim mutated workspace:\n%s\n%s", before, got)
	}
	if _, err = s.CompleteClaimedTask(ctx, done.ID, "shipped", other, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := workspaceRow(t, s, w.ID); got != before {
		t.Fatalf("done mutated workspace:\n%s\n%s", before, got)
	}
	if live, liveErr := s.LiveWorkspaceForTask(ctx, done.ID); liveErr != nil || live == nil || live.State != domain.WorkspaceReady {
		t.Fatalf("workspace of done task=%+v err=%v", live, liveErr)
	}

	cancelled := claimedReadyTask(t, s, "ws-cancel", owner)
	cw, err := s.ReserveWorkspace(ctx, cancelled.ID, "/tmp/wt/cancel", "griglia/cancel", "", owner, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.MarkWorkspaceReady(ctx, cw.ID, now); err != nil {
		t.Fatal(err)
	}
	cancelBefore := workspaceRow(t, s, cw.ID)
	if _, err = s.ReleaseClaim(ctx, cancelled.ID, owner, "", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	at := now.Add(2 * time.Second)
	cancelled.Lifecycle, cancelled.CancelledAt, cancelled.UpdatedAt, cancelled.Version = domain.LifecycleCancelled, &at, at, cancelled.Version+1
	if _, err = s.TransitionTask(ctx, cancelled, cancelled.Version-1, "superseded"); err != nil {
		t.Fatal(err)
	}
	if got := workspaceRow(t, s, cw.ID); got != cancelBefore {
		t.Fatalf("cancel mutated workspace:\n%s\n%s", cancelBefore, got)
	}
	if live, liveErr := s.LiveWorkspaceForTask(ctx, cancelled.ID); liveErr != nil || live == nil || live.State != domain.WorkspaceReady {
		t.Fatalf("workspace of cancelled task=%+v err=%v", live, liveErr)
	}
}
