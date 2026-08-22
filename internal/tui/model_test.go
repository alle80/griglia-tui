package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/alle80/griglia-tui/internal/app"
	"github.com/alle80/griglia-tui/internal/domain"
)

type fakeService struct {
	tasks   []domain.Task
	listErr error
	addErr  error
	added   []app.AddTaskInput
	nextID  int64
}

func (f *fakeService) ListTasks(context.Context) ([]domain.Task, error) {
	return append([]domain.Task(nil), f.tasks...), f.listErr
}

func (f *fakeService) AddTask(_ context.Context, input app.AddTaskInput) (domain.Task, error) {
	f.added = append(f.added, input)
	if f.addErr != nil {
		return domain.Task{}, f.addErr
	}
	if f.nextID == 0 {
		f.nextID = 10
	}
	task := makeTask(f.nextID, input.Title, input.Priority, input.Lifecycle)
	f.tasks = append(f.tasks, task)
	return task, nil
}

func makeTask(id int64, title string, priority domain.Priority, lifecycle domain.Lifecycle) domain.Task {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	return domain.Task{ID: id, UID: "uid", Title: title, Description: "Description for " + title, Priority: priority, Lifecycle: lifecycle, CreatedAt: now, UpdatedAt: now, Version: 1}
}

func key(value string) tea.KeyPressMsg {
	switch value {
	case "enter":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	case "down":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyDown})
	case "up":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyUp})
	case "esc":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})
	default:
		return tea.KeyPressMsg(tea.Key{Code: []rune(value)[0], Text: value})
	}
}

func update(t *testing.T, model Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := model.Update(msg)
	result, ok := next.(Model)
	if !ok {
		t.Fatalf("model type %T", next)
	}
	return result, cmd
}

func load(t *testing.T, model Model) Model {
	t.Helper()
	cmd := model.Init()
	if cmd == nil {
		t.Fatal("expected initial loading command")
	}
	model, _ = update(t, model, cmd())
	return model
}

func TestInitialLoadingAndEmptyList(t *testing.T) {
	model := New(context.Background(), &fakeService{})
	if !model.loading || !strings.Contains(model.render(), "Loading tasks") {
		t.Fatal("model should begin in a visible loading state")
	}
	model = load(t, model)
	if model.loading || !strings.Contains(model.render(), "No tasks yet") || !strings.Contains(model.render(), "n to create") {
		t.Fatalf("empty view=%q", model.render())
	}
}

func TestPopulatedListMovementAndSelectionPreservedByID(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{makeTask(1, "Urgent", domain.PriorityUrgent, domain.LifecycleBacklog), makeTask(2, "Ready", domain.PriorityHigh, domain.LifecycleReady)}}
	model := load(t, New(context.Background(), service))
	model, _ = update(t, model, key("j"))
	if model.selectedID != 2 {
		t.Fatalf("selected ID=%d", model.selectedID)
	}
	service.tasks = []domain.Task{service.tasks[1], service.tasks[0]}
	model, _ = update(t, model, tasksLoadedMsg{tasks: service.tasks})
	if model.selected != 0 || model.selectedID != 2 {
		t.Fatalf("selection index=%d id=%d", model.selected, model.selectedID)
	}
	view := model.render()
	for _, want := range []string{"Ready", "[ready]", "HIGH"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q: %q", want, view)
		}
	}
}

func TestDetailAndHelpNavigation(t *testing.T) {
	model := load(t, New(context.Background(), &fakeService{tasks: []domain.Task{makeTask(1, "Inspect me", domain.PriorityNormal, domain.LifecycleBacklog)}}))
	model, _ = update(t, model, key("enter"))
	if model.route != routeDetail || !strings.Contains(model.render(), "TASK #1") {
		t.Fatalf("detail route=%v view=%q", model.route, model.render())
	}
	model, _ = update(t, model, key("q"))
	if model.route != routeList {
		t.Fatalf("route=%v", model.route)
	}
	model, _ = update(t, model, key("?"))
	if model.route != routeHelp || !strings.Contains(model.render(), "HELP") {
		t.Fatalf("help route=%v", model.route)
	}
	model, _ = update(t, model, key("?"))
	if model.route != routeList {
		t.Fatalf("route after closing help=%v", model.route)
	}
}

func TestSuccessfulTaskCreation(t *testing.T) {
	service := &fakeService{}
	model := load(t, New(context.Background(), service))
	model, _ = update(t, model, key("n"))
	if model.route != routeForm {
		t.Fatalf("route=%v", model.route)
	}
	model.form.inputs[0].SetValue("Created interactively")
	model.form.inputs[1].SetValue("Details")
	model.form.inputs[2].SetValue("high")
	model.form.focus = 2
	model, cmd := update(t, model, key("enter"))
	if cmd == nil || !model.form.saving {
		t.Fatal("expected save command")
	}
	model, cmd = update(t, model, cmd())
	if model.route != routeList || cmd == nil || len(service.added) != 1 {
		t.Fatalf("route=%v reload=%v adds=%d", model.route, cmd != nil, len(service.added))
	}
	model, _ = update(t, model, cmd())
	if model.loading || model.selectedID != 10 || !strings.Contains(model.render(), "Created interactively") {
		t.Fatalf("created model=%+v view=%q", model, model.render())
	}
}

type repository struct{}

func (repository) CreateTask(context.Context, domain.Task) (domain.Task, error) {
	return domain.Task{}, nil
}
func (repository) ListTasks(context.Context) ([]domain.Task, error)    { return nil, nil }
func (repository) GetTask(context.Context, int64) (domain.Task, error) { return domain.Task{}, nil }

func TestFormValidationErrorIsRecoverable(t *testing.T) {
	model := load(t, New(context.Background(), app.New(repository{})))
	model, _ = update(t, model, key("n"))
	model.form.focus = 2
	model, cmd := update(t, model, key("enter"))
	if cmd == nil {
		t.Fatal("expected application validation command")
	}
	model, _ = update(t, model, cmd())
	if model.route != routeForm || model.form.err == nil || !strings.Contains(model.render(), "title must be non-empty") {
		t.Fatalf("route=%v error=%v", model.route, model.form.err)
	}
	model, _ = update(t, model, key("a"))
	if model.form.err != nil {
		t.Fatal("editing should clear the recoverable error")
	}
}

func TestApplicationErrorsRemainVisible(t *testing.T) {
	loadErr := errors.New("database unavailable")
	model := load(t, New(context.Background(), &fakeService{listErr: loadErr}))
	if model.err == nil || !strings.Contains(model.render(), "database unavailable") || !strings.Contains(model.render(), "retry") {
		t.Fatalf("load error view=%q", model.render())
	}
	service := &fakeService{addErr: errors.New("write failed")}
	model = load(t, New(context.Background(), service))
	model, _ = update(t, model, key("n"))
	model.form.inputs[0].SetValue("Task")
	model.form.focus = 2
	model, cmd := update(t, model, key("enter"))
	model, _ = update(t, model, cmd())
	if model.route != routeForm || !strings.Contains(model.render(), "write failed") {
		t.Fatalf("persistence error view=%q", model.render())
	}
}

func TestResponsiveRenderingAndResize(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{makeTask(1, "A recognisable task", domain.PriorityUrgent, domain.LifecycleReady)}}
	for _, tc := range []struct {
		name  string
		width int
	}{{"wide", 120}, {"medium", 80}, {"narrow", 38}} {
		t.Run(tc.name, func(t *testing.T) {
			model := load(t, New(context.Background(), service))
			model, _ = update(t, model, tea.WindowSizeMsg{Width: tc.width, Height: 20})
			view := model.render()
			for _, want := range []string{"A recognisable task", "[ready]", "URGENT"} {
				if !strings.Contains(view, want) {
					t.Fatalf("%s view missing %q: %q", tc.name, want, view)
				}
			}
			model, _ = update(t, model, tea.WindowSizeMsg{Width: 24, Height: 8})
			if model.width != 24 || model.height != 8 || len(strings.Split(model.render(), "\n")) > 8 {
				t.Fatalf("resize width=%d height=%d view=%q", model.width, model.height, model.render())
			}
		})
	}
}
