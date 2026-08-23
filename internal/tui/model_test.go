package tui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/alle80/griglia-tui/internal/app"
	"github.com/alle80/griglia-tui/internal/domain"
)

type fakeService struct {
	tasks        []domain.Task
	claims       map[int64]*domain.Claim
	waiting      map[int64]bool
	blocked      map[int64]bool
	listErr      error
	listCalls    int
	addErr       error
	added        []app.AddTaskInput
	nextID       int64
	actionErr    error
	questions    []domain.Question
	qListErr     error
	answerErr    error
	dependencies []domain.DependencyView
	depListCalls []int64
	depErr       error
}

func (f *fakeService) EditTask(_ context.Context, id int64, in app.EditTaskInput) (domain.Task, error) {
	for i := range f.tasks {
		if f.tasks[i].ID == id {
			if in.Title != nil {
				f.tasks[i].Title = *in.Title
			}
			if in.Description != nil {
				f.tasks[i].Description = *in.Description
			}
			if in.Priority != nil {
				f.tasks[i].Priority = *in.Priority
			}
			f.tasks[i].Version++
			return f.tasks[i], nil
		}
	}
	return domain.Task{}, domain.ErrNotFound
}
func (f *fakeService) MarkReady(_ context.Context, id int64) (domain.Task, error) {
	return f.setLifecycle(id, domain.LifecycleReady)
}
func (f *fakeService) CompleteTask(_ context.Context, id int64) (domain.Task, error) {
	return f.setLifecycle(id, domain.LifecycleDone)
}
func (f *fakeService) CancelTask(_ context.Context, id int64, _ string) (domain.Task, error) {
	return f.setLifecycle(id, domain.LifecycleCancelled)
}
func (f *fakeService) setLifecycle(id int64, lifecycle domain.Lifecycle) (domain.Task, error) {
	if f.actionErr != nil {
		return domain.Task{}, f.actionErr
	}
	for i := range f.tasks {
		if f.tasks[i].ID == id {
			f.tasks[i].Lifecycle = lifecycle
			f.tasks[i].Version++
			return f.tasks[i], nil
		}
	}
	return domain.Task{}, domain.ErrNotFound
}

func (f *fakeService) ListTasks(context.Context) ([]domain.TaskView, error) {
	f.listCalls++
	views := make([]domain.TaskView, 0, len(f.tasks))
	for _, task := range f.tasks {
		views = append(views, domain.NewTaskView(task, f.claims[task.ID], f.waiting[task.ID], f.blocked[task.ID]))
	}
	return views, f.listErr
}

func (f *fakeService) ListQuestions(_ context.Context, taskID int64, _ domain.QuestionFilter) ([]domain.Question, error) {
	if f.qListErr != nil {
		return nil, f.qListErr
	}
	questions := make([]domain.Question, 0, len(f.questions))
	for _, q := range f.questions {
		if q.TaskID == taskID {
			questions = append(questions, q)
		}
	}
	return questions, nil
}

func (f *fakeService) AnswerQuestion(_ context.Context, questionID int64, answer string) (domain.Question, error) {
	if f.answerErr != nil {
		return domain.Question{}, f.answerErr
	}
	now := time.Date(2026, 8, 23, 13, 0, 0, 0, time.UTC)
	for i := range f.questions {
		if f.questions[i].ID == questionID {
			f.questions[i].Answer, f.questions[i].AnsweredAt = &answer, &now
			return f.questions[i], nil
		}
	}
	return domain.Question{}, domain.ErrNotFound
}

func (f *fakeService) ListDependencies(_ context.Context, taskID int64) ([]domain.DependencyView, error) {
	f.depListCalls = append(f.depListCalls, taskID)
	dependencies := make([]domain.DependencyView, 0, len(f.dependencies))
	for _, d := range f.dependencies {
		if d.TaskID == taskID {
			dependencies = append(dependencies, d)
		}
	}
	return dependencies, nil
}

func (f *fakeService) AddDependency(_ context.Context, taskID, dependsOnTaskID int64) (domain.DependencyView, error) {
	if f.depErr != nil {
		return domain.DependencyView{}, f.depErr
	}
	d := domain.DependencyView{TaskID: taskID, DependsOnTaskID: dependsOnTaskID, Title: fmt.Sprintf("Task %d", dependsOnTaskID), Lifecycle: domain.LifecycleReady}
	f.dependencies = append(f.dependencies, d)
	return d, nil
}

func (f *fakeService) RemoveDependency(_ context.Context, taskID, dependsOnTaskID int64) error {
	if f.depErr != nil {
		return f.depErr
	}
	kept := f.dependencies[:0]
	for _, d := range f.dependencies {
		if !(d.TaskID == taskID && d.DependsOnTaskID == dependsOnTaskID) {
			kept = append(kept, d)
		}
	}
	f.dependencies = kept
	return nil
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

// focusedValue reads the value of the form field that currently has focus.
func focusedValue(f formModel) string {
	switch f.focus {
	case focusDescription:
		return f.description.Value()
	case focusPriority:
		return f.priority.Value()
	default:
		return f.input.Value()
	}
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
	case "tab":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyTab})
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
	// Substitute the wall-clock tick with an inert stub so tests stay
	// deterministic; auto-refresh behavior is driven by explicit tickMsg.
	model.tick = func() tea.Cmd { return nil }
	cmd := model.Init()
	if cmd == nil {
		t.Fatal("expected initial loading command")
	}
	return runCmd(t, model, cmd)
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
	views, _ := service.ListTasks(context.Background())
	model, _ = update(t, model, tasksLoadedMsg{tasks: views})
	if model.selected != 0 || model.selectedID != 2 {
		t.Fatalf("selection index=%d id=%d", model.selected, model.selectedID)
	}
	view := model.render()
	for _, want := range []string{"Ready", "[available]", "HIGH"} {
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
	model.form.input.SetValue("Created interactively")
	model.form.description.SetValue("Details")
	model.form.priority.SetValue("high")
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

func TestPrintableShortcutsAreTextInEditForm(t *testing.T) {
	for _, printable := range []string{"e", "a", "d", "x", "Q"} {
		t.Run(printable, func(t *testing.T) {
			service := &fakeService{tasks: []domain.Task{makeTask(1, "Original", domain.PriorityNormal, domain.LifecycleBacklog)}}
			model := load(t, New(context.Background(), service))
			model, _ = update(t, model, key("e"))
			model.form.input.SetValue("")
			model, cmd := update(t, model, key(printable))
			assertPrintableStayedInForm(t, model, cmd, printable)
			if service.tasks[0].Title != "Original" || service.tasks[0].Lifecycle != domain.LifecycleBacklog {
				t.Fatalf("shortcut triggered a task action: %+v", service.tasks[0])
			}
		})
	}
}

func TestPrintableShortcutsAreTextInCancellationReasonForm(t *testing.T) {
	for _, printable := range []string{"e", "a", "d", "x", "Q"} {
		t.Run(printable, func(t *testing.T) {
			service := &fakeService{tasks: []domain.Task{makeTask(1, "Original", domain.PriorityNormal, domain.LifecycleBacklog)}}
			model := load(t, New(context.Background(), service))
			model, _ = update(t, model, key("x"))
			model, cmd := update(t, model, key(printable))
			assertPrintableStayedInForm(t, model, cmd, printable)
			if service.tasks[0].Lifecycle != domain.LifecycleBacklog {
				t.Fatalf("shortcut triggered cancellation: %+v", service.tasks[0])
			}
		})
	}
}

func assertPrintableStayedInForm(t *testing.T, model Model, cmd tea.Cmd, want string) {
	t.Helper()
	if cmd != nil && reflect.TypeOf(cmd()) == reflect.TypeOf(tea.Quit()) {
		t.Fatalf("%q returned the quit command while editing", want)
	}
	if model.route != routeForm {
		t.Fatalf("%q changed route to %v", want, model.route)
	}
	if got := model.form.input.Value(); got != want {
		t.Fatalf("input value=%q, want %q", got, want)
	}
}

type repository struct{}

func (repository) CreateTask(context.Context, domain.Task) (domain.Task, error) {
	return domain.Task{}, nil
}
func (repository) ListTasks(context.Context) ([]domain.TaskView, error) { return nil, nil }
func (repository) GetTask(context.Context, int64) (domain.TaskView, error) {
	return domain.TaskView{}, nil
}
func (repository) EditTask(context.Context, domain.Task, int64) (domain.Task, error) {
	return domain.Task{}, nil
}
func (repository) TransitionTask(context.Context, domain.Task, int64, string) (domain.Task, error) {
	return domain.Task{}, nil
}
func (repository) ClaimTask(context.Context, int64, domain.AgentIdentity, time.Time) (domain.TaskView, error) {
	return domain.TaskView{}, nil
}
func (repository) ClaimNext(context.Context, domain.AgentIdentity, time.Time) (domain.TaskView, error) {
	return domain.TaskView{}, nil
}
func (repository) ReleaseClaim(context.Context, int64, domain.AgentIdentity, string, time.Time) (domain.TaskView, error) {
	return domain.TaskView{}, nil
}
func (repository) UpdateProgress(context.Context, int64, int, string, domain.AgentIdentity, time.Time) (domain.TaskView, error) {
	return domain.TaskView{}, nil
}
func (repository) CompleteClaimedTask(context.Context, int64, string, domain.AgentIdentity, time.Time) (domain.TaskView, error) {
	return domain.TaskView{}, nil
}
func (repository) AskQuestion(context.Context, int64, string, bool, domain.AgentIdentity, time.Time) (domain.Question, error) {
	return domain.Question{}, nil
}
func (repository) AnswerQuestion(context.Context, int64, string, time.Time) (domain.Question, error) {
	return domain.Question{}, nil
}
func (repository) AcknowledgeQuestion(context.Context, int64, domain.AgentIdentity, time.Time) (domain.Question, error) {
	return domain.Question{}, nil
}
func (repository) ListQuestions(context.Context, int64, domain.QuestionFilter) ([]domain.Question, error) {
	return nil, nil
}
func (repository) AddDependency(context.Context, int64, int64, time.Time) (domain.DependencyView, error) {
	return domain.DependencyView{}, nil
}
func (repository) RemoveDependency(context.Context, int64, int64, time.Time) error { return nil }
func (repository) ListDependencies(context.Context, int64) ([]domain.DependencyView, error) {
	return nil, nil
}

func TestLifecycleActionsEditAndHelp(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{makeTask(1, "First", domain.PriorityLow, domain.LifecycleBacklog), makeTask(2, "Second", domain.PriorityHigh, domain.LifecycleReady)}}
	model := load(t, New(context.Background(), service))
	model, _ = update(t, model, key("e"))
	if model.route != routeForm || !model.form.editing || model.form.input.Value() != "First" {
		t.Fatalf("edit form=%+v", model.form)
	}
	model.form.input.SetValue("Edited")
	model.form.priority.SetValue("urgent")
	model.form.focus = 2
	model, cmd := update(t, model, key("enter"))
	model, cmd = update(t, model, cmd())
	model, _ = update(t, model, cmd())
	if model.selectedID != 1 || service.tasks[0].Title != "Edited" {
		t.Fatalf("selection=%d tasks=%+v", model.selectedID, service.tasks)
	}
	model, cmd = update(t, model, key("a"))
	model, cmd = update(t, model, cmd())
	model, _ = update(t, model, cmd())
	if service.tasks[0].Lifecycle != domain.LifecycleReady || model.selectedID != 1 {
		t.Fatalf("ready selection=%d task=%+v", model.selectedID, service.tasks[0])
	}
	if view := model.helpView(); !strings.Contains(view, "mark backlog task ready") || !strings.Contains(view, "cancel backlog/ready") {
		t.Fatalf("help=%q", view)
	}
	model, _ = update(t, model, key("x"))
	if !model.form.cancelling {
		t.Fatal("cancel should open reason form")
	}
	model.form.input.SetValue("superseded")
	model, cmd = update(t, model, key("enter"))
	model, cmd = update(t, model, cmd())
	model, _ = update(t, model, cmd())
	if service.tasks[0].Lifecycle != domain.LifecycleCancelled {
		t.Fatalf("cancelled task=%+v", service.tasks[0])
	}
}

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
	model.form.input.SetValue("Task")
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
			for _, want := range []string{"A recognisable task", "[available]", "URGENT"} {
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

func TestAgentCoordinationRendering(t *testing.T) {
	task := makeTask(8, "Parser", domain.PriorityHigh, domain.LifecycleReady)
	task.Progress, task.Phase = 65, "Error recovery"
	claimed := time.Date(2026, 8, 23, 12, 30, 0, 0, time.UTC)
	claim := &domain.Claim{TaskID: 8, AgentName: "codex", InstanceID: "session-123", ClaimedAt: claimed}
	view := domain.NewTaskView(task, claim, false, false)
	model := New(context.Background(), &fakeService{})
	model.loading, model.tasks, model.selectedID = false, []domain.TaskView{view}, 8
	model.restoreSelection()
	for _, want := range []string{"[working]", "codex", "session-123", "65%", "Error recovery"} {
		if !strings.Contains(model.render(), want) {
			t.Fatalf("list missing %q: %q", want, model.render())
		}
	}
	model.route = routeDetail
	for _, want := range []string{"Operational state: working", "Agent: codex", "Instance: session-123", "Progress: 65%", "Phase: Error recovery"} {
		if !strings.Contains(model.render(), want) {
			t.Fatalf("detail missing %q: %q", want, model.render())
		}
	}
	model.width = 38
	if !strings.Contains(model.render(), "working") {
		t.Fatalf("narrow detail=%q", model.render())
	}
}

func runCmd(t *testing.T, model Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return model
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			model = runCmd(t, model, tea.Cmd(sub))
		}
		return model
	}
	model, next := update(t, model, msg)
	return runCmd(t, model, next)
}

func makeQuestion(id, taskID int64, body string, blocking bool) domain.Question {
	return domain.Question{ID: id, TaskID: taskID, Body: body, Blocking: blocking, AskedBy: domain.AgentIdentity{AgentName: "codex", InstanceID: "session-123"}, AskedAt: time.Date(2026, 8, 23, 12, 15, 0, 0, time.UTC)}
}

func TestWaitingForHumanRendering(t *testing.T) {
	task := makeTask(8, "Parser", domain.PriorityHigh, domain.LifecycleReady)
	claim := &domain.Claim{TaskID: 8, AgentName: "codex", InstanceID: "session-123", ClaimedAt: time.Date(2026, 8, 23, 12, 30, 0, 0, time.UTC)}
	view := domain.NewTaskView(task, claim, true, false)
	model := New(context.Background(), &fakeService{})
	model.loading, model.tasks, model.selectedID = false, []domain.TaskView{view}, 8
	model.width = 120
	model.restoreSelection()
	if !strings.Contains(model.render(), "[waiting_for_human]") {
		t.Fatalf("list missing waiting indicator: %q", model.render())
	}
	model.route = routeDetail
	for _, want := range []string{"Operational state: waiting_for_human", "!! Waiting for human input", "press w"} {
		if !strings.Contains(model.render(), want) {
			t.Fatalf("detail missing %q: %q", want, model.render())
		}
	}
	model.route, model.width = routeList, 38
	if !strings.Contains(model.render(), "waiting_for_human") {
		t.Fatalf("narrow list=%q", model.render())
	}
}

func TestQuestionNavigationAnswerAndSelectionPreserved(t *testing.T) {
	blocking := makeQuestion(1, 5, "Should malformed nodes be preserved?", true)
	info := makeQuestion(2, 5, "FYI note", false)
	service := &fakeService{tasks: []domain.Task{makeTask(5, "Parser", domain.PriorityHigh, domain.LifecycleReady)}, questions: []domain.Question{blocking, info}}
	model := load(t, New(context.Background(), service))
	model, _ = update(t, model, key("enter"))
	model, cmd := update(t, model, key("w"))
	if model.route != routeQuestions || !model.questionsLoading || cmd == nil {
		t.Fatalf("route=%v loading=%v", model.route, model.questionsLoading)
	}
	model = runCmd(t, model, cmd)
	view := model.render()
	for _, want := range []string{"QUESTIONS — TASK #5", "[BLOCKING]", "[info]", "unanswered", "Should malformed nodes be preserved?"} {
		if !strings.Contains(view, want) {
			t.Fatalf("questions view missing %q: %q", want, view)
		}
	}
	model, _ = update(t, model, key("j"))
	if model.questionSelectedID != 2 {
		t.Fatalf("selected question=%d", model.questionSelectedID)
	}
	model, _ = update(t, model, key("k"))
	model, _ = update(t, model, key("enter"))
	if model.route != routeForm || !model.form.answering || model.form.questionID != 1 {
		t.Fatalf("answer form=%+v route=%v", model.form, model.route)
	}
	model.form.input.SetValue("Yes, preserve them")
	model, cmd = update(t, model, key("enter"))
	if cmd == nil || !model.form.saving {
		t.Fatal("expected answer command")
	}
	model = runCmd(t, model, cmd)
	if model.route != routeQuestions || model.questionSelectedID != 1 || !strings.Contains(model.status, "Answered question #1") {
		t.Fatalf("after answer route=%v selected=%d status=%q", model.route, model.questionSelectedID, model.status)
	}
	if service.questions[0].Answer == nil || *service.questions[0].Answer != "Yes, preserve them" {
		t.Fatalf("service question=%+v", service.questions[0])
	}
	view = model.render()
	if !strings.Contains(view, "answered") || !strings.Contains(view, "Answer: Yes, preserve them") {
		t.Fatalf("refreshed questions view=%q", view)
	}
	// Back returns to the detail we came from, then to the list.
	model, _ = update(t, model, key("q"))
	if model.route != routeDetail {
		t.Fatalf("route after back=%v", model.route)
	}
}

func TestQuestionsFromListEscAndAcknowledgedAreReadOnly(t *testing.T) {
	acked := makeQuestion(3, 5, "Old decision", true)
	answer := "resolved"
	when := time.Date(2026, 8, 23, 12, 40, 0, 0, time.UTC)
	acked.Answer, acked.AnsweredAt, acked.AcknowledgedAt = &answer, &when, &when
	service := &fakeService{tasks: []domain.Task{makeTask(5, "Parser", domain.PriorityHigh, domain.LifecycleReady)}, questions: []domain.Question{acked}}
	model := load(t, New(context.Background(), service))
	model, cmd := update(t, model, key("w"))
	model = runCmd(t, model, cmd)
	if model.route != routeQuestions || model.questionsFrom != routeList {
		t.Fatalf("route=%v from=%v", model.route, model.questionsFrom)
	}
	if !strings.Contains(model.render(), "acknowledged") {
		t.Fatalf("view=%q", model.render())
	}
	// Acknowledged questions cannot be reopened for answering.
	model, _ = update(t, model, key("enter"))
	if model.route != routeQuestions {
		t.Fatalf("acknowledged question opened form: %v", model.route)
	}
	// Refresh reloads both questions and tasks.
	model, cmd = update(t, model, key("r"))
	if cmd == nil || !model.questionsLoading {
		t.Fatal("expected refresh command")
	}
	model = runCmd(t, model, cmd)
	model, _ = update(t, model, key("q"))
	if model.route != routeList {
		t.Fatalf("route after back=%v", model.route)
	}
}

func TestAnswerFormEscReturnsToQuestions(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{makeTask(5, "Parser", domain.PriorityHigh, domain.LifecycleReady)}, questions: []domain.Question{makeQuestion(1, 5, "Open question", true)}}
	model := load(t, New(context.Background(), service))
	model, cmd := update(t, model, key("w"))
	model = runCmd(t, model, cmd)
	model, _ = update(t, model, key("enter"))
	if model.route != routeForm || !model.form.answering {
		t.Fatalf("form=%+v", model.form)
	}
	// An empty answer is rejected locally and recoverable.
	model, cmd = update(t, model, key("enter"))
	if cmd != nil || model.form.err == nil {
		t.Fatalf("empty answer err=%v", model.form.err)
	}
	model, _ = update(t, model, key("esc"))
	if model.route != routeQuestions {
		t.Fatalf("route after esc=%v", model.route)
	}
}

func TestPrintableShortcutsAreTextInAnswerForm(t *testing.T) {
	for _, printable := range []string{"e", "a", "d", "x", "Q", "w", "r", "q", "n", "j", "k"} {
		t.Run(printable, func(t *testing.T) {
			service := &fakeService{tasks: []domain.Task{makeTask(5, "Parser", domain.PriorityHigh, domain.LifecycleReady)}, questions: []domain.Question{makeQuestion(1, 5, "Open question", true)}}
			model := load(t, New(context.Background(), service))
			model, cmd := update(t, model, key("w"))
			model = runCmd(t, model, cmd)
			model, _ = update(t, model, key("enter"))
			model, cmd = update(t, model, key(printable))
			assertPrintableStayedInForm(t, model, cmd, printable)
			if service.questions[0].Answer != nil {
				t.Fatalf("shortcut answered the question: %+v", service.questions[0])
			}
		})
	}
}

func TestQuestionLoadErrorIsRecoverable(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{makeTask(5, "Parser", domain.PriorityHigh, domain.LifecycleReady)}, qListErr: errors.New("questions unavailable")}
	model := load(t, New(context.Background(), service))
	model, cmd := update(t, model, key("w"))
	model = runCmd(t, model, cmd)
	if model.route != routeQuestions || model.actionErr == nil || !strings.Contains(model.render(), "questions unavailable") {
		t.Fatalf("route=%v err=%v", model.route, model.actionErr)
	}
	service.qListErr = nil
	model, cmd = update(t, model, key("r"))
	model = runCmd(t, model, cmd)
	if model.actionErr != nil || !strings.Contains(model.render(), "No questions") {
		t.Fatalf("recovered view=%q", model.render())
	}
}

func makeDependency(taskID, dependsOn int64, title string, lifecycle domain.Lifecycle) domain.DependencyView {
	return domain.DependencyView{TaskID: taskID, DependsOnTaskID: dependsOn, Title: title, Lifecycle: lifecycle, CreatedAt: time.Date(2026, 8, 23, 12, 20, 0, 0, time.UTC)}
}

func TestBlockedRendering(t *testing.T) {
	task := makeTask(9, "Backend", domain.PriorityUrgent, domain.LifecycleReady)
	view := domain.NewTaskView(task, nil, false, true)
	model := New(context.Background(), &fakeService{})
	model.loading, model.tasks, model.selectedID = false, []domain.TaskView{view}, 9
	model.width = 120
	model.restoreSelection()
	if !strings.Contains(model.render(), "[blocked]") {
		t.Fatalf("list missing blocked indicator: %q", model.render())
	}
	model.route = routeDetail
	for _, want := range []string{"Operational state: blocked", "!! Blocked by unsatisfied dependencies", "press b"} {
		if !strings.Contains(model.render(), want) {
			t.Fatalf("detail missing %q: %q", want, model.render())
		}
	}
	model.route, model.width = routeList, 38
	if !strings.Contains(model.render(), "blocked") {
		t.Fatalf("narrow list=%q", model.render())
	}
}

func TestDependencyNavigationAddRemoveAndSelection(t *testing.T) {
	service := &fakeService{
		tasks:        []domain.Task{makeTask(2, "Backend", domain.PriorityUrgent, domain.LifecycleReady), makeTask(1, "Schema", domain.PriorityHigh, domain.LifecycleDone)},
		dependencies: []domain.DependencyView{makeDependency(2, 1, "Schema", domain.LifecycleDone), makeDependency(2, 3, "API", domain.LifecycleReady)},
	}
	model := load(t, New(context.Background(), service))
	model, cmd := update(t, model, key("enter"))
	if model.route != routeDetail || cmd == nil {
		t.Fatalf("detail should load dependencies: route=%v", model.route)
	}
	model = runCmd(t, model, cmd)
	// The detail view lists direct dependencies with their satisfaction.
	for _, want := range []string{"Dependencies", "satisfied", "unsatisfied", "Schema", "API"} {
		if !strings.Contains(model.render(), want) {
			t.Fatalf("detail missing %q: %q", want, model.render())
		}
	}
	model, cmd = update(t, model, key("b"))
	if model.route != routeDependencies || cmd == nil {
		t.Fatalf("route=%v", model.route)
	}
	model = runCmd(t, model, cmd)
	if !strings.Contains(model.render(), "DEPENDENCIES — TASK #2") {
		t.Fatalf("dependencies view=%q", model.render())
	}
	model, _ = update(t, model, key("j"))
	if model.depSelectedID != 3 {
		t.Fatalf("selected dependency=%d", model.depSelectedID)
	}
	// Remove the selected edge; selection then falls back safely.
	model, cmd = update(t, model, key("x"))
	model = runCmd(t, model, cmd)
	if len(model.dependencies) != 1 || model.dependencies[0].DependsOnTaskID != 1 {
		t.Fatalf("dependencies after remove=%+v", model.dependencies)
	}
	if !strings.Contains(model.status, "Removed dependency") {
		t.Fatalf("status=%q", model.status)
	}
	// Add a new prerequisite through the form.
	model, _ = update(t, model, key("n"))
	if model.route != routeForm || !model.form.depending {
		t.Fatalf("form=%+v", model.form)
	}
	model.form.input.SetValue("4")
	model, cmd = update(t, model, key("enter"))
	if cmd == nil || !model.form.saving {
		t.Fatal("expected add command")
	}
	model = runCmd(t, model, cmd)
	if model.route != routeDependencies || model.depSelectedID != 4 || len(model.dependencies) != 2 {
		t.Fatalf("after add route=%v selected=%d deps=%+v", model.route, model.depSelectedID, model.dependencies)
	}
	model, _ = update(t, model, key("q"))
	if model.route != routeDetail {
		t.Fatalf("route after back=%v", model.route)
	}
}

func TestDependencyFormValidationAndCycleRecovery(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{makeTask(2, "Backend", domain.PriorityUrgent, domain.LifecycleReady)}}
	model := load(t, New(context.Background(), service))
	model, cmd := update(t, model, key("b"))
	model = runCmd(t, model, cmd)
	model, _ = update(t, model, key("n"))
	// A non-numeric prerequisite is rejected locally and recoverable.
	model.form.input.SetValue("nope")
	model, cmd = update(t, model, key("enter"))
	if cmd != nil || model.form.err == nil {
		t.Fatalf("invalid id err=%v", model.form.err)
	}
	// A cycle conflict surfaces in the form and the model stays usable.
	service.depErr = fmt.Errorf("dependency would create a cycle: %w", domain.ErrConflict)
	model.form.input.SetValue("7")
	model, cmd = update(t, model, key("enter"))
	if cmd == nil {
		t.Fatal("expected add command")
	}
	model, _ = update(t, model, cmd())
	if model.route != routeForm || model.form.err == nil || !strings.Contains(model.render(), "cycle") {
		t.Fatalf("cycle recovery route=%v err=%v", model.route, model.form.err)
	}
	model, _ = update(t, model, key("esc"))
	if model.route != routeDependencies {
		t.Fatalf("route after esc=%v", model.route)
	}
}

func TestPrintableShortcutsAreTextInDependencyForm(t *testing.T) {
	for _, printable := range []string{"e", "a", "d", "x", "Q", "w", "b", "r", "q", "n", "j", "k"} {
		t.Run(printable, func(t *testing.T) {
			service := &fakeService{tasks: []domain.Task{makeTask(2, "Backend", domain.PriorityUrgent, domain.LifecycleReady)}}
			model := load(t, New(context.Background(), service))
			model, cmd := update(t, model, key("b"))
			model = runCmd(t, model, cmd)
			model, _ = update(t, model, key("n"))
			model, cmd = update(t, model, key(printable))
			assertPrintableStayedInForm(t, model, cmd, printable)
			if len(service.dependencies) != 0 {
				t.Fatalf("shortcut mutated dependencies: %+v", service.dependencies)
			}
		})
	}
}

func TestClaimedTaskLifecycleConflictIsRecoverable(t *testing.T) {
	task := makeTask(8, "Parser", domain.PriorityHigh, domain.LifecycleReady)
	claim := &domain.Claim{TaskID: 8, AgentName: "codex", InstanceID: "session-123", ClaimedAt: time.Now().UTC()}
	view := domain.NewTaskView(task, claim, false, false)
	service := &fakeService{tasks: []domain.Task{task}, actionErr: fmt.Errorf("task is actively claimed: %w", domain.ErrConflict)}
	model := New(context.Background(), service)
	model.loading, model.tasks, model.selectedID = false, []domain.TaskView{view}, 8
	model.restoreSelection()
	model, cmd := update(t, model, key("d"))
	if cmd == nil {
		t.Fatal("expected lifecycle command")
	}
	model, _ = update(t, model, cmd())
	if model.route != routeList || model.actionErr == nil || !strings.Contains(model.render(), "actively claimed") || model.tasks[0].Lifecycle != domain.LifecycleReady {
		t.Fatalf("model=%+v view=%q", model, model.render())
	}
	model, cmd = update(t, model, key("j"))
	if cmd != nil || model.route != routeList {
		t.Fatal("model should remain usable")
	}
}
