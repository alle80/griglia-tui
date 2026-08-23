package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alle80/griglia-tui/internal/domain"
)

type memoryRepository struct {
	task     domain.Task
	expected int64
	reason   string
	conflict bool
}

func (r *memoryRepository) CreateTask(context.Context, domain.Task) (domain.Task, error) {
	return domain.Task{}, nil
}
func (r *memoryRepository) ListTasks(context.Context) ([]domain.TaskView, error) {
	return []domain.TaskView{domain.NewTaskView(r.task, nil)}, nil
}
func (r *memoryRepository) GetTask(context.Context, int64) (domain.TaskView, error) {
	return domain.NewTaskView(r.task, nil), nil
}
func (r *memoryRepository) EditTask(_ context.Context, task domain.Task, expected int64) (domain.Task, error) {
	if r.conflict {
		return domain.Task{}, domain.ErrConflict
	}
	r.expected, r.task = expected, task
	return task, nil
}
func (r *memoryRepository) TransitionTask(_ context.Context, task domain.Task, expected int64, reason string) (domain.Task, error) {
	if r.conflict {
		return domain.Task{}, domain.ErrConflict
	}
	r.expected, r.reason, r.task = expected, reason, task
	return task, nil
}
func (r *memoryRepository) ClaimTask(context.Context, int64, domain.AgentIdentity, time.Time) (domain.TaskView, error) {
	return domain.TaskView{}, nil
}
func (r *memoryRepository) ClaimNext(context.Context, domain.AgentIdentity, time.Time) (domain.TaskView, error) {
	return domain.TaskView{}, nil
}
func (r *memoryRepository) ReleaseClaim(context.Context, int64, domain.AgentIdentity, string, time.Time) (domain.TaskView, error) {
	return domain.TaskView{}, nil
}
func (r *memoryRepository) UpdateProgress(context.Context, int64, int, string, domain.AgentIdentity, time.Time) (domain.TaskView, error) {
	return domain.TaskView{}, nil
}
func (r *memoryRepository) CompleteClaimedTask(context.Context, int64, string, domain.AgentIdentity, time.Time) (domain.TaskView, error) {
	return domain.TaskView{}, nil
}

func TestEditAndLifecycleSemantics(t *testing.T) {
	base := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	current := base.Add(time.Hour)
	r := &memoryRepository{task: domain.Task{ID: 1, Title: "Old", Description: "before", Priority: domain.PriorityLow, Lifecycle: domain.LifecycleBacklog, Progress: 42, UpdatedAt: base, Version: 7}}
	s := New(r)
	s.now = func() time.Time { return current }
	title, description, priority := "New", "after", domain.PriorityUrgent
	task, err := s.EditTask(context.Background(), 1, EditTaskInput{Title: &title, Description: &description, Priority: &priority})
	if err != nil || task.Title != title || task.Description != description || task.Priority != priority || task.Version != 8 || r.expected != 7 || !task.UpdatedAt.Equal(current) {
		t.Fatalf("edit task=%+v expected=%d err=%v", task, r.expected, err)
	}
	task, err = s.MarkReady(context.Background(), 1)
	if err != nil || task.Lifecycle != domain.LifecycleReady || task.Version != 9 {
		t.Fatalf("ready=%+v err=%v", task, err)
	}
	task, err = s.CompleteTask(context.Background(), 1)
	if err != nil || task.Progress != 100 || task.CompletedAt == nil || task.CancelledAt != nil || task.Version != 10 {
		t.Fatalf("done=%+v err=%v", task, err)
	}
	if _, err = s.CancelTask(context.Background(), 1, "late"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("terminal cancel err=%v", err)
	}
}

func TestCancelPreservesProgressAndRejectsMissingEditFields(t *testing.T) {
	now := time.Now().UTC()
	r := &memoryRepository{task: domain.Task{ID: 1, Title: "Task", Priority: domain.PriorityNormal, Lifecycle: domain.LifecycleReady, Progress: 63, Version: 1}}
	s := New(r)
	s.now = func() time.Time { return now }
	task, err := s.CancelTask(context.Background(), 1, "obsolete")
	if err != nil || task.Progress != 63 || task.CancelledAt == nil || task.CompletedAt != nil || r.reason != "obsolete" {
		t.Fatalf("cancel=%+v reason=%q err=%v", task, r.reason, err)
	}
	if _, err = s.EditTask(context.Background(), 1, EditTaskInput{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing fields err=%v", err)
	}
}
