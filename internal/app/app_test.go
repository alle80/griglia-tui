package app

import (
	"context"
	"errors"
	"strings"
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
	return []domain.TaskView{domain.NewTaskView(r.task, nil, false)}, nil
}
func (r *memoryRepository) GetTask(context.Context, int64) (domain.TaskView, error) {
	return domain.NewTaskView(r.task, nil, false), nil
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
func (r *memoryRepository) AskQuestion(_ context.Context, taskID int64, body string, blocking bool, identity domain.AgentIdentity, now time.Time) (domain.Question, error) {
	return domain.Question{ID: 1, TaskID: taskID, Body: body, Blocking: blocking, AskedBy: identity, AskedAt: now}, nil
}
func (r *memoryRepository) AnswerQuestion(_ context.Context, questionID int64, answer string, now time.Time) (domain.Question, error) {
	return domain.Question{ID: questionID, Answer: &answer, AnsweredAt: &now}, nil
}
func (r *memoryRepository) AcknowledgeQuestion(_ context.Context, questionID int64, identity domain.AgentIdentity, now time.Time) (domain.Question, error) {
	return domain.Question{ID: questionID, AskedBy: identity, AcknowledgedAt: &now}, nil
}
func (r *memoryRepository) ListQuestions(context.Context, int64, domain.QuestionFilter) ([]domain.Question, error) {
	return nil, nil
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

func TestQuestionInputValidation(t *testing.T) {
	s := New(&memoryRepository{})
	ctx := context.Background()
	identity := domain.AgentIdentity{AgentName: "codex", InstanceID: "one"}
	long := strings.Repeat("x", domain.MaxQuestionTextLength+1)
	if _, err := s.AskQuestion(ctx, 1, "valid?", true, domain.AgentIdentity{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing identity err=%v", err)
	}
	for _, body := range []string{"", "   ", long} {
		if _, err := s.AskQuestion(ctx, 1, body, true, identity); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("body=%q err=%v", body, err)
		}
		if _, err := s.AnswerQuestion(ctx, 1, body); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("answer=%q err=%v", body, err)
		}
	}
	if _, err := s.AcknowledgeQuestion(ctx, 1, domain.AgentIdentity{AgentName: "codex"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("partial identity err=%v", err)
	}
	q, err := s.AskQuestion(ctx, 7, "Should malformed nodes be preserved?", true, identity)
	if err != nil || q.TaskID != 7 || !q.Blocking || q.AskedBy != identity {
		t.Fatalf("ask=%+v err=%v", q, err)
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
