package app

import (
	"context"
	"fmt"
	"time"

	"github.com/alle80/griglia-tui/internal/domain"
)

type TaskRepository interface {
	CreateTask(context.Context, domain.Task) (domain.Task, error)
	ListTasks(context.Context) ([]domain.Task, error)
	GetTask(context.Context, int64) (domain.Task, error)
	EditTask(context.Context, domain.Task, int64) (domain.Task, error)
	TransitionTask(context.Context, domain.Task, int64, string) (domain.Task, error)
}

type EditTaskInput struct {
	Title       *string
	Description *string
	Priority    *domain.Priority
}

func (s *Service) EditTask(ctx context.Context, id int64, in EditTaskInput) (domain.Task, error) {
	if in.Title == nil && in.Description == nil && in.Priority == nil {
		return domain.Task{}, fmt.Errorf("at least one editable field is required: %w", domain.ErrInvalid)
	}
	t, err := s.tasks.GetTask(ctx, id)
	if err != nil {
		return domain.Task{}, err
	}
	if t.Lifecycle == domain.LifecycleDone || t.Lifecycle == domain.LifecycleCancelled {
		return domain.Task{}, fmt.Errorf("terminal tasks cannot be edited: %w", domain.ErrConflict)
	}
	if in.Title != nil {
		if err := domain.ValidateTitle(*in.Title); err != nil {
			return domain.Task{}, fmt.Errorf("title must be non-empty and at most %d characters: %w", domain.MaxTitleLength, err)
		}
		t.Title = *in.Title
	}
	if in.Description != nil {
		t.Description = *in.Description
	}
	if in.Priority != nil {
		if _, err := domain.ParsePriority(string(*in.Priority)); err != nil {
			return domain.Task{}, fmt.Errorf("invalid priority: %w", err)
		}
		t.Priority = *in.Priority
	}
	expected := t.Version
	t.UpdatedAt = s.now().UTC()
	t.Version++
	return s.tasks.EditTask(ctx, t, expected)
}

func (s *Service) MarkReady(ctx context.Context, id int64) (domain.Task, error) {
	return s.transition(ctx, id, domain.LifecycleReady, "")
}
func (s *Service) CompleteTask(ctx context.Context, id int64) (domain.Task, error) {
	return s.transition(ctx, id, domain.LifecycleDone, "")
}
func (s *Service) CancelTask(ctx context.Context, id int64, reason string) (domain.Task, error) {
	return s.transition(ctx, id, domain.LifecycleCancelled, reason)
}

func (s *Service) transition(ctx context.Context, id int64, to domain.Lifecycle, reason string) (domain.Task, error) {
	t, err := s.tasks.GetTask(ctx, id)
	if err != nil {
		return domain.Task{}, err
	}
	if err = domain.ValidateTransition(t.Lifecycle, to); err != nil {
		return domain.Task{}, err
	}
	expected, now := t.Version, s.now().UTC()
	t.Lifecycle, t.UpdatedAt, t.Version = to, now, t.Version+1
	switch to {
	case domain.LifecycleDone:
		t.Progress, t.CompletedAt, t.CancelledAt = 100, &now, nil
	case domain.LifecycleCancelled:
		t.CancelledAt, t.CompletedAt = &now, nil
	}
	return s.tasks.TransitionTask(ctx, t, expected, reason)
}

type Service struct {
	tasks TaskRepository
	now   func() time.Time
}

func New(tasks TaskRepository) *Service { return &Service{tasks: tasks, now: time.Now} }

type AddTaskInput struct {
	Title, Description string
	Priority           domain.Priority
	Lifecycle          domain.Lifecycle
}

func (s *Service) AddTask(ctx context.Context, in AddTaskInput) (domain.Task, error) {
	if err := domain.ValidateTitle(in.Title); err != nil {
		return domain.Task{}, fmt.Errorf("title must be non-empty and at most %d characters: %w", domain.MaxTitleLength, err)
	}
	now := s.now().UTC()
	uid, err := domain.NewUUID()
	if err != nil {
		return domain.Task{}, fmt.Errorf("generate task UUID: %w", err)
	}
	t := domain.Task{UID: uid, Title: in.Title, Description: in.Description, Priority: in.Priority, Lifecycle: in.Lifecycle, CreatedAt: now, UpdatedAt: now, Version: 1}
	return s.tasks.CreateTask(ctx, t)
}

func (s *Service) ListTasks(ctx context.Context) ([]domain.Task, error) {
	return s.tasks.ListTasks(ctx)
}
func (s *Service) GetTask(ctx context.Context, id int64) (domain.Task, error) {
	return s.tasks.GetTask(ctx, id)
}
