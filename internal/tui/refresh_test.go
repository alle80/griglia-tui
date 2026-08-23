package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/alle80/griglia-tui/internal/domain"
)

// loadCounting is load with a counting tick stub, for tests that assert on
// tick scheduling itself.
func loadCounting(t *testing.T, model Model, ticks *int) Model {
	t.Helper()
	model.tick = func() tea.Cmd { *ticks++; return nil }
	return runCmd(t, model, model.Init())
}

func TestTickSchedulingIsASingleLoop(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{makeTask(1, "One", domain.PriorityNormal, domain.LifecycleReady)}}
	ticks := 0
	model := loadCounting(t, New(context.Background(), service), &ticks)
	if ticks != 1 {
		t.Fatalf("Init scheduled %d ticks, want 1", ticks)
	}
	// Each handled tick schedules exactly one successor, whether or not it
	// starts a refresh (the first starts one; the rest are suppressed by the
	// in-flight guard because the refresh result is deliberately not
	// delivered).
	for i := 0; i < 5; i++ {
		model, _ = update(t, model, tickMsg{})
	}
	if ticks != 6 {
		t.Fatalf("5 ticks scheduled %d successors, want exactly 5", ticks-1)
	}
}

func TestAutoRefreshReflectsExternalAgentChanges(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{makeTask(1, "Parser", domain.PriorityHigh, domain.LifecycleReady)}}
	model := load(t, New(context.Background(), service))
	model.width = 120
	// Another process claims the task, reports progress, and pauses on a
	// blocking question.
	service.tasks[0].Progress, service.tasks[0].Phase = 65, "Error recovery"
	service.claims = map[int64]*domain.Claim{1: {TaskID: 1, AgentName: "codex", InstanceID: "session-9", ClaimedAt: time.Date(2026, 8, 23, 12, 30, 0, 0, time.UTC)}}
	service.waiting = map[int64]bool{1: true}
	model, cmd := update(t, model, tickMsg{})
	if !model.refreshing || cmd == nil {
		t.Fatalf("tick should start a background refresh: refreshing=%v", model.refreshing)
	}
	model = runCmd(t, model, cmd)
	if model.refreshing {
		t.Fatal("refresh completion should clear the in-flight flag")
	}
	view := model.render()
	for _, want := range []string{"[waiting_for_human]", "codex", "65%"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q after auto-refresh: %q", want, view)
		}
	}
	// The claim is released and the task completed externally.
	service.claims, service.waiting = nil, nil
	service.tasks[0].Lifecycle, service.tasks[0].Progress = domain.LifecycleDone, 100
	model, cmd = update(t, model, tickMsg{})
	model = runCmd(t, model, cmd)
	if !strings.Contains(model.render(), "[done]") {
		t.Fatalf("view missing completion after auto-refresh: %q", model.render())
	}
}

func TestAutoRefreshPreservesSelectionByID(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{makeTask(1, "First", domain.PriorityNormal, domain.LifecycleReady), makeTask(2, "Second", domain.PriorityHigh, domain.LifecycleReady)}}
	model := load(t, New(context.Background(), service))
	model, _ = update(t, model, key("j"))
	if model.selectedID != 2 {
		t.Fatalf("selected ID=%d", model.selectedID)
	}
	// An external writer reorders the list and inserts a new task above the
	// selection.
	service.tasks = []domain.Task{makeTask(3, "Inserted", domain.PriorityUrgent, domain.LifecycleReady), service.tasks[1], service.tasks[0]}
	model, cmd := update(t, model, tickMsg{})
	model = runCmd(t, model, cmd)
	if model.selectedID != 2 || model.selected != 1 {
		t.Fatalf("selection after refresh id=%d index=%d, want id=2 index=1", model.selectedID, model.selected)
	}
}

func TestAutoRefreshDoesNotDisturbActiveForms(t *testing.T) {
	openForm := map[string]func(t *testing.T, service *fakeService) Model{
		"create": func(t *testing.T, service *fakeService) Model {
			model := load(t, New(context.Background(), service))
			model, _ = update(t, model, key("n"))
			return model
		},
		"edit": func(t *testing.T, service *fakeService) Model {
			model := load(t, New(context.Background(), service))
			model, _ = update(t, model, key("e"))
			return model
		},
		"cancel": func(t *testing.T, service *fakeService) Model {
			model := load(t, New(context.Background(), service))
			model, _ = update(t, model, key("x"))
			return model
		},
		"answer": func(t *testing.T, service *fakeService) Model {
			service.questions = []domain.Question{makeQuestion(1, 1, "Open question", true)}
			model := load(t, New(context.Background(), service))
			model, cmd := update(t, model, key("w"))
			model = runCmd(t, model, cmd)
			model, _ = update(t, model, key("enter"))
			return model
		},
		"depend": func(t *testing.T, service *fakeService) Model {
			model := load(t, New(context.Background(), service))
			model, cmd := update(t, model, key("b"))
			model = runCmd(t, model, cmd)
			model, _ = update(t, model, key("n"))
			return model
		},
	}
	for name, open := range openForm {
		t.Run(name, func(t *testing.T) {
			service := &fakeService{tasks: []domain.Task{makeTask(1, "Parser", domain.PriorityHigh, domain.LifecycleReady)}}
			model := open(t, service)
			if model.route != routeForm {
				t.Fatalf("route=%v", model.route)
			}
			model, cmd := update(t, model, key("d"))
			model = runCmd(t, model, cmd)
			typed := model.form.inputs[model.form.focus].Value()
			focus := model.form.focus
			listCalls := service.listCalls
			for i := 0; i < 3; i++ {
				model, cmd = update(t, model, tickMsg{})
				model = runCmd(t, model, cmd)
			}
			if model.route != routeForm {
				t.Fatalf("tick closed the form: route=%v", model.route)
			}
			if model.refreshing || service.listCalls != listCalls {
				t.Fatalf("tick refreshed behind a form: refreshing=%v calls=%d→%d", model.refreshing, listCalls, service.listCalls)
			}
			if model.form.focus != focus || model.form.inputs[model.form.focus].Value() != typed {
				t.Fatalf("tick disturbed input: focus=%d value=%q", model.form.focus, model.form.inputs[model.form.focus].Value())
			}
		})
	}
}

func TestAutoRefreshDoesNotOverlapRequests(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{makeTask(1, "One", domain.PriorityNormal, domain.LifecycleReady)}}
	model := load(t, New(context.Background(), service))
	baseline := service.listCalls
	model, first := update(t, model, tickMsg{})
	if first == nil || !model.refreshing {
		t.Fatal("first tick should start a refresh")
	}
	// While the first refresh result is pending, further ticks must not issue
	// new requests.
	for i := 0; i < 3; i++ {
		var cmd tea.Cmd
		model, cmd = update(t, model, tickMsg{})
		model = runCmd(t, model, cmd)
	}
	if service.listCalls != baseline {
		t.Fatalf("overlapping refreshes issued: calls=%d→%d", baseline, service.listCalls)
	}
	// Delivering the pending result re-arms the loop.
	model = runCmd(t, model, first)
	if service.listCalls != baseline+1 || model.refreshing {
		t.Fatalf("pending refresh delivery: calls=%d refreshing=%v", service.listCalls, model.refreshing)
	}
	model, cmd := update(t, model, tickMsg{})
	model = runCmd(t, model, cmd)
	if service.listCalls != baseline+2 {
		t.Fatalf("loop did not re-arm: calls=%d", service.listCalls)
	}
	if model.refreshing {
		t.Fatal("refresh flag stuck")
	}
}

func TestAutoRefreshSkipsWhileForegroundLoadInFlight(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{makeTask(1, "One", domain.PriorityNormal, domain.LifecycleReady)}}
	model := load(t, New(context.Background(), service))
	// A manual refresh is in flight: the tick must not add a second request,
	// and a straggling background result must not clobber the newer data.
	model, manual := update(t, model, key("r"))
	baseline := service.listCalls
	model, cmd := update(t, model, tickMsg{})
	model = runCmd(t, model, cmd)
	if service.listCalls != baseline || model.refreshing {
		t.Fatalf("tick refreshed during manual load: calls=%d→%d", baseline, service.listCalls)
	}
	stale := []domain.TaskView{domain.NewTaskView(makeTask(9, "Stale", domain.PriorityLow, domain.LifecycleBacklog), nil, false, false)}
	model, _ = update(t, model, tasksLoadedMsg{tasks: stale, background: true})
	if len(model.tasks) != 1 || model.tasks[0].ID != 1 {
		t.Fatalf("stale background result applied during manual load: %+v", model.tasks)
	}
	model = runCmd(t, model, manual)
	if model.loading || model.tasks[0].ID != 1 {
		t.Fatalf("manual refresh did not complete: %+v", model.tasks)
	}
}

func TestAutoRefreshErrorKeepsCurrentStateAndRetries(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{makeTask(1, "Survivor", domain.PriorityNormal, domain.LifecycleReady)}}
	model := load(t, New(context.Background(), service))
	service.listErr = errors.New("database locked")
	model, cmd := update(t, model, tickMsg{})
	model = runCmd(t, model, cmd)
	if model.err != nil || model.refreshing {
		t.Fatalf("background error leaked: err=%v refreshing=%v", model.err, model.refreshing)
	}
	view := model.render()
	if !strings.Contains(view, "Survivor") || strings.Contains(view, "database locked") {
		t.Fatalf("transient refresh error destroyed the view: %q", view)
	}
	// The next tick retries and picks up new data once storage recovers.
	service.listErr = nil
	service.tasks[0].Title = "Recovered"
	model, cmd = update(t, model, tickMsg{})
	model = runCmd(t, model, cmd)
	if !strings.Contains(model.render(), "Recovered") {
		t.Fatalf("refresh did not recover: %q", model.render())
	}
}

func TestAutoRefreshUpdatesQuestionsView(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{makeTask(5, "Parser", domain.PriorityHigh, domain.LifecycleReady)}, questions: []domain.Question{makeQuestion(1, 5, "Keep going?", true), makeQuestion(2, 5, "FYI", false)}}
	model := load(t, New(context.Background(), service))
	model, cmd := update(t, model, key("w"))
	model = runCmd(t, model, cmd)
	model, _ = update(t, model, key("j"))
	if model.route != routeQuestions || model.questionSelectedID != 2 {
		t.Fatalf("route=%v selected=%d", model.route, model.questionSelectedID)
	}
	// The owning agent acknowledges an answer and asks a new question.
	answer, when := "yes", time.Date(2026, 8, 23, 13, 0, 0, 0, time.UTC)
	service.questions[0].Answer, service.questions[0].AnsweredAt, service.questions[0].AcknowledgedAt = &answer, &when, &when
	service.questions = append(service.questions, makeQuestion(3, 5, "New question", true))
	model, cmd = update(t, model, tickMsg{})
	model = runCmd(t, model, cmd)
	view := model.render()
	for _, want := range []string{"acknowledged", "New question"} {
		if !strings.Contains(view, want) {
			t.Fatalf("questions view missing %q after auto-refresh: %q", want, view)
		}
	}
	if model.questionSelectedID != 2 {
		t.Fatalf("question selection lost: %d", model.questionSelectedID)
	}
}

func TestAutoRefreshUpdatesDependenciesInDetailAndDependenciesViews(t *testing.T) {
	service := &fakeService{
		tasks:        []domain.Task{makeTask(2, "Backend", domain.PriorityUrgent, domain.LifecycleReady)},
		blocked:      map[int64]bool{2: true},
		dependencies: []domain.DependencyView{makeDependency(2, 1, "Schema", domain.LifecycleReady)},
	}
	model := load(t, New(context.Background(), service))
	model, cmd := update(t, model, key("enter"))
	model = runCmd(t, model, cmd)
	if model.route != routeDetail || !strings.Contains(model.render(), "unsatisfied") {
		t.Fatalf("detail view=%q", model.render())
	}
	// The prerequisite completes externally: the task unblocks.
	service.dependencies[0].Lifecycle = domain.LifecycleDone
	service.blocked = nil
	model, cmd = update(t, model, tickMsg{})
	model = runCmd(t, model, cmd)
	view := model.render()
	if !strings.Contains(view, "satisfied") || strings.Contains(view, "unsatisfied") || strings.Contains(view, "blocked") {
		t.Fatalf("detail after auto-refresh: %q", view)
	}
	model, cmd = update(t, model, key("b"))
	model = runCmd(t, model, cmd)
	service.dependencies = append(service.dependencies, makeDependency(2, 3, "API", domain.LifecycleReady))
	model, cmd = update(t, model, tickMsg{})
	model = runCmd(t, model, cmd)
	if len(model.dependencies) != 2 || !strings.Contains(model.render(), "API") {
		t.Fatalf("dependencies view after auto-refresh: %q", model.render())
	}
}

func TestDetailAutoRefreshTargetsDisplayedTaskNotDependenciesTaskID(t *testing.T) {
	// dependenciesTaskID can lag behind the detail view (zero, or pointing at
	// a task inspected earlier); the automatic refresh must request the
	// dependencies of the task actually on screen.
	for name, staleID := range map[string]int64{"zero": 0, "other-task": 7} {
		t.Run(name, func(t *testing.T) {
			service := &fakeService{
				tasks:        []domain.Task{makeTask(1, "Displayed", domain.PriorityHigh, domain.LifecycleReady), makeTask(7, "Other", domain.PriorityLow, domain.LifecycleReady)},
				dependencies: []domain.DependencyView{makeDependency(1, 4, "Mine", domain.LifecycleDone), makeDependency(7, 9, "Theirs", domain.LifecycleReady)},
			}
			model := load(t, New(context.Background(), service))
			model.route = routeDetail
			model.dependenciesTaskID = staleID
			model.dependencies = nil
			service.depListCalls = nil
			model, cmd := update(t, model, tickMsg{})
			model = runCmd(t, model, cmd)
			if len(service.depListCalls) != 1 || service.depListCalls[0] != 1 {
				t.Fatalf("dependency requests=%v, want exactly [1]", service.depListCalls)
			}
			if model.dependenciesTaskID != 1 {
				t.Fatalf("dependenciesTaskID=%d, want 1", model.dependenciesTaskID)
			}
			view := model.render()
			if !strings.Contains(view, "Mine") || strings.Contains(view, "Theirs") {
				t.Fatalf("detail shows wrong dependencies: %q", view)
			}
		})
	}
}

func TestStaleBackgroundResultsForOtherTasksAreIgnored(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{makeTask(5, "Parser", domain.PriorityHigh, domain.LifecycleReady)}, questions: []domain.Question{makeQuestion(1, 5, "Q", true)}}
	model := load(t, New(context.Background(), service))
	model, cmd := update(t, model, key("w"))
	model = runCmd(t, model, cmd)
	model, _ = update(t, model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	// A background result for a task the user has navigated away from must
	// not overwrite the questions of the current task.
	model, _ = update(t, model, questionsLoadedMsg{taskID: 99, questions: nil, background: true})
	if len(model.questions) != 1 {
		t.Fatalf("stale questions applied: %+v", model.questions)
	}
	model, _ = update(t, model, dependenciesLoadedMsg{taskID: 99, dependencies: []domain.DependencyView{makeDependency(99, 1, "X", domain.LifecycleReady)}, background: true})
	if len(model.dependencies) != 0 {
		t.Fatalf("stale dependencies applied: %+v", model.dependencies)
	}
}
