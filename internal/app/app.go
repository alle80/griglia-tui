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
