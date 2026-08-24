package sqlite

// Read-model invariants for `workspace show`/`workspace list`: each task's
// current workspace (the live row, else the latest failed row; removed rows
// never surface) joined with the task's active claim in a single query, so
// derived usage needs no per-row lookups.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alle80/griglia-tui/internal/domain"
)

func TestWorkspaceViewDerivesClaimAxis(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner := domain.AgentIdentity{AgentName: "claude", InstanceID: "view-1"}
	task := claimedReadyTask(t, s, "ws-view", owner)
	now := time.Now().UTC()

	w, err := s.ReserveWorkspace(ctx, task.ID, "/tmp/wt/view", "griglia/view", "abc", owner, now)
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.WorkspaceViewForTask(ctx, task.ID)
	if err != nil || view.ID != w.ID || view.State != domain.WorkspaceAllocating {
		t.Fatalf("view=%+v err=%v", view, err)
	}
	if view.Usage() != domain.WorkspaceInUse || view.ActiveClaim == nil || view.ActiveClaim.AgentName != owner.AgentName || view.ActiveClaim.InstanceID != owner.InstanceID {
		t.Fatalf("claimed view=%+v", view)
	}
	if _, err = s.MarkWorkspaceReady(ctx, w.ID, now); err != nil {
		t.Fatal(err)
	}

	// Release parks the workspace; a claim by a different identity resumes it
	// — the workspace row itself never changes.
	if _, err = s.ReleaseClaim(ctx, task.ID, owner, "", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	view, err = s.WorkspaceViewForTask(ctx, task.ID)
	if err != nil || view.Usage() != domain.WorkspaceIdle || view.ActiveClaim != nil {
		t.Fatalf("idle view=%+v err=%v", view, err)
	}
	other := domain.AgentIdentity{AgentName: "codex", InstanceID: "view-2"}
	if _, err = s.ClaimTask(ctx, task.ID, other, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	view, err = s.WorkspaceViewForTask(ctx, task.ID)
	if err != nil || view.Usage() != domain.WorkspaceInUse || view.ActiveClaim.InstanceID != "view-2" {
		t.Fatalf("reclaimed view=%+v err=%v", view, err)
	}
}

func TestWorkspaceViewSurfacesCurrentRowOnly(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner := domain.AgentIdentity{AgentName: "claude", InstanceID: "cur-1"}
	task := claimedReadyTask(t, s, "ws-current", owner)
	now := time.Now().UTC()

	if _, err := s.WorkspaceViewForTask(ctx, task.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("no workspace err=%v want ErrNotFound", err)
	}
	if _, err := s.WorkspaceViewForTask(ctx, 99); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing task err=%v want ErrNotFound", err)
	}

	// A failed allocation is the current workspace while nothing live exists:
	// its recorded error is what makes the failure diagnosable.
	failed, err := s.ReserveWorkspace(ctx, task.ID, "/tmp/wt/cur-a", "griglia/cur-a", "", owner, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.MarkWorkspaceFailed(ctx, failed.ID, "boom", now); err != nil {
		t.Fatal(err)
	}
	view, err := s.WorkspaceViewForTask(ctx, task.ID)
	if err != nil || view.ID != failed.ID || view.State != domain.WorkspaceFailed || view.Error != "boom" {
		t.Fatalf("failed view=%+v err=%v", view, err)
	}

	// A successful retry supersedes the failed row.
	retry, err := s.ReserveWorkspace(ctx, task.ID, "/tmp/wt/cur-b", "griglia/cur-b", "", owner, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.MarkWorkspaceReady(ctx, retry.ID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	view, err = s.WorkspaceViewForTask(ctx, task.ID)
	if err != nil || view.ID != retry.ID || view.State != domain.WorkspaceReady {
		t.Fatalf("retry view=%+v err=%v", view, err)
	}

	// Removal ends the history: the older failed row must not resurface.
	if _, err = s.RemoveWorkspace(ctx, retry.ID, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.WorkspaceViewForTask(ctx, task.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("after remove err=%v want ErrNotFound", err)
	}
}

func TestListWorkspaceViewsIsDeterministic(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner := domain.AgentIdentity{AgentName: "claude", InstanceID: "list-1"}
	now := time.Now().UTC()

	first := claimedReadyTask(t, s, "ws-list-a", owner)
	second := claimedReadyTask(t, s, "ws-list-b", owner)
	third := claimedReadyTask(t, s, "ws-list-c", owner)

	// Insert out of task order to prove the ordering comes from the query.
	bw, err := s.ReserveWorkspace(ctx, second.ID, "/tmp/wt/list-b", "griglia/list-b", "", owner, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.MarkWorkspaceReady(ctx, bw.ID, now); err != nil {
		t.Fatal(err)
	}
	aw, err := s.ReserveWorkspace(ctx, first.ID, "/tmp/wt/list-a", "griglia/list-a", "", owner, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.MarkWorkspaceFailed(ctx, aw.ID, "boom", now); err != nil {
		t.Fatal(err)
	}
	cw, err := s.ReserveWorkspace(ctx, third.ID, "/tmp/wt/list-c", "griglia/list-c", "", owner, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.MarkWorkspaceReady(ctx, cw.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RemoveWorkspace(ctx, cw.ID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReleaseClaim(ctx, second.ID, owner, "", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	views, err := s.ListWorkspaceViews(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("views=%+v", views)
	}
	if views[0].TaskID != first.ID || views[0].State != domain.WorkspaceFailed || views[0].Usage() != domain.WorkspaceInUse {
		t.Fatalf("views[0]=%+v", views[0])
	}
	if views[1].TaskID != second.ID || views[1].State != domain.WorkspaceReady || views[1].Usage() != domain.WorkspaceIdle {
		t.Fatalf("views[1]=%+v", views[1])
	}
}
