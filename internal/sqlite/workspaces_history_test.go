package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alle80/griglia-tui/internal/domain"
)

func TestWorkspacesForTaskReturnsFullHistory(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	identity := domain.AgentIdentity{AgentName: "claude", InstanceID: "history-1"}
	task := claimedReadyTask(t, s, "history", identity)
	other := claimedReadyTask(t, s, "history-other", identity)
	now := time.Now().UTC()

	failed, err := s.ReserveWorkspace(ctx, task.ID, "/tmp/ws/task-a", "griglia/task-a", "c1", identity, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.MarkWorkspaceFailed(ctx, failed.ID, "boom", now); err != nil {
		t.Fatal(err)
	}
	live, err := s.ReserveWorkspace(ctx, task.ID, "/tmp/ws/task-a2", "griglia/task-a2", "c2", identity, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReserveWorkspace(ctx, other.ID, "/tmp/ws/task-b", "griglia/task-b", "c3", identity, now); err != nil {
		t.Fatal(err)
	}

	history, err := s.WorkspacesForTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].ID != failed.ID || history[1].ID != live.ID {
		t.Fatalf("history=%+v", history)
	}
	if history[0].State != domain.WorkspaceFailed || history[1].State != domain.WorkspaceAllocating {
		t.Fatalf("states=%s,%s", history[0].State, history[1].State)
	}

	if _, err = s.WorkspacesForTask(ctx, 9999); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown task err=%v", err)
	}
}
