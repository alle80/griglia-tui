package app

import (
	"context"
	"fmt"
	"time"

	"github.com/alle80/griglia-tui/internal/domain"
)

type TaskRepository interface {
	CreateTask(context.Context, domain.Task) (domain.Task, error)
	ListTasks(context.Context) ([]domain.TaskView, error)
	GetTask(context.Context, int64) (domain.TaskView, error)
	EditTask(context.Context, domain.Task, int64) (domain.Task, error)
	TransitionTask(context.Context, domain.Task, int64, string) (domain.Task, error)
	ClaimTask(context.Context, int64, domain.AgentIdentity, time.Time) (domain.TaskView, error)
	ClaimNext(context.Context, domain.AgentIdentity, time.Time) (domain.TaskView, error)
	ReleaseClaim(context.Context, int64, domain.AgentIdentity, string, time.Time) (domain.TaskView, error)
	UpdateProgress(context.Context, int64, int, string, domain.AgentIdentity, time.Time) (domain.TaskView, error)
	CompleteClaimedTask(context.Context, int64, string, domain.AgentIdentity, time.Time) (domain.TaskView, error)
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
	view, err := s.tasks.GetTask(ctx, id)
	if err != nil {
		return domain.Task{}, err
	}
	if view.ActiveClaim != nil {
		return domain.Task{}, fmt.Errorf("task is actively claimed: %w", domain.ErrConflict)
	}
	t := view.Task
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
	view, err := s.tasks.GetTask(ctx, id)
	if err != nil {
		return domain.Task{}, err
	}
	if view.ActiveClaim != nil {
		return domain.Task{}, fmt.Errorf("task is actively claimed: %w", domain.ErrConflict)
	}
	t := view.Task
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

func (s *Service) ListTasks(ctx context.Context) ([]domain.TaskView, error) {
	return s.tasks.ListTasks(ctx)
}
func (s *Service) GetTask(ctx context.Context, id int64) (domain.TaskView, error) {
	return s.tasks.GetTask(ctx, id)
}

func validateIdentity(identity domain.AgentIdentity) error {
	if err := domain.ValidateAgentIdentity(identity); err != nil {
		return fmt.Errorf("agent and instance must be non-empty and at most %d characters: %w", domain.MaxAgentIdentityLength, err)
	}
	return nil
}

func (s *Service) ClaimTask(ctx context.Context, id int64, identity domain.AgentIdentity) (domain.TaskView, error) {
	if err := validateIdentity(identity); err != nil {
		return domain.TaskView{}, err
	}
	return s.tasks.ClaimTask(ctx, id, identity, s.now().UTC())
}
func (s *Service) ClaimNext(ctx context.Context, identity domain.AgentIdentity) (domain.TaskView, error) {
	if err := validateIdentity(identity); err != nil {
		return domain.TaskView{}, err
	}
	return s.tasks.ClaimNext(ctx, identity, s.now().UTC())
}
func (s *Service) ReleaseClaim(ctx context.Context, id int64, identity domain.AgentIdentity, reason string) (domain.TaskView, error) {
	if err := validateIdentity(identity); err != nil {
		return domain.TaskView{}, err
	}
	return s.tasks.ReleaseClaim(ctx, id, identity, reason, s.now().UTC())
}
func (s *Service) UpdateProgress(ctx context.Context, id int64, percent int, message string, identity domain.AgentIdentity) (domain.TaskView, error) {
	if err := validateIdentity(identity); err != nil {
		return domain.TaskView{}, err
	}
	if percent < 0 || percent > 100 {
		return domain.TaskView{}, fmt.Errorf("progress must be between 0 and 100: %w", domain.ErrInvalid)
	}
	return s.tasks.UpdateProgress(ctx, id, percent, message, identity, s.now().UTC())
}
func (s *Service) CompleteClaimedTask(ctx context.Context, id int64, comment string, identity domain.AgentIdentity) (domain.TaskView, error) {
	if err := validateIdentity(identity); err != nil {
		return domain.TaskView{}, err
	}
	return s.tasks.CompleteClaimedTask(ctx, id, comment, identity, s.now().UTC())
}
