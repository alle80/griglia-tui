package tui

// Terminal-robustness hardening: extreme sizes, wide Unicode, large lists,
// external refresh, and error recovery. These tests drive the real Update
// and View paths with synthetic messages; rendering must never panic and
// truncated rows must respect the terminal width.

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/alle80/griglia-tui/internal/domain"
)

func renderAllRoutes(t *testing.T, model Model) {
	t.Helper()
	for _, route := range []route{routeList, routeDetail, routeForm, routeHelp, routeQuestions, routeDependencies} {
		model.route = route
		_ = model.render()
	}
}

func TestTinyAndZeroTerminalSizesDoNotPanic(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{
		makeTask(1, "A task with a title long enough to require truncation everywhere", domain.PriorityUrgent, domain.LifecycleReady),
		makeTask(2, "Second", domain.PriorityLow, domain.LifecycleBacklog),
	}}
	model := load(t, New(t.Context(), service))
	for _, size := range []tea.WindowSizeMsg{{Width: 0, Height: 0}, {Width: 1, Height: 1}, {Width: 5, Height: 2}, {Width: 12, Height: 4}, {Width: -1, Height: -1}} {
		model, _ = update(t, model, size)
		renderAllRoutes(t, model)
	}
}

func TestWideUnicodeTitlesAreTruncatedWithinWidth(t *testing.T) {
	wide := strings.Repeat("日本語タイトル🚀", 12)
	service := &fakeService{tasks: []domain.Task{makeTask(1, wide, domain.PriorityNormal, domain.LifecycleReady)}}
	model := load(t, New(t.Context(), service))
	for _, width := range []int{24, 40, 59, 80} {
		model, _ = update(t, model, tea.WindowSizeMsg{Width: width, Height: 24})
		for i, line := range strings.Split(model.render(), "\n") {
			// Task rows are truncated to the terminal; informational footer
			// and preview lines wrap and are the terminal's responsibility.
			if strings.Contains(line, "#1") {
				if lineWidth := lipgloss.Width(line); lineWidth > width {
					t.Fatalf("width=%d line %d overflows (%d): %q", width, i, lineWidth, line)
				}
			}
		}
	}
	if truncated := truncate(wide, 10); lipgloss.Width(truncated) > 10 {
		t.Fatalf("truncate produced width %d: %q", lipgloss.Width(truncated), truncated)
	}
}

func TestLargeListKeepsSelectionVisibleAndBounded(t *testing.T) {
	service := &fakeService{}
	for i := 1; i <= 200; i++ {
		service.tasks = append(service.tasks, makeTask(int64(i), fmt.Sprintf("Task %03d", i), domain.PriorityNormal, domain.LifecycleReady))
	}
	model := load(t, New(t.Context(), service))
	model, _ = update(t, model, tea.WindowSizeMsg{Width: 50, Height: 20})
	for i := 0; i < 199; i++ {
		model, _ = update(t, model, key("j"))
	}
	if model.selectedID != 200 {
		t.Fatalf("selectedID=%d", model.selectedID)
	}
	rendered := model.render()
	if !strings.Contains(rendered, "Task 200") {
		t.Fatalf("selected row not visible:\n%s", rendered)
	}
	if lines := strings.Count(rendered, "\n") + 1; lines > 20 {
		t.Fatalf("rendered %d lines for height 20", lines)
	}
}

func TestExternalChangesAppearOnManualRefresh(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{makeTask(1, "Original", domain.PriorityNormal, domain.LifecycleReady)}}
	model := load(t, New(t.Context(), service))

	// Another process adds a task and mutates the existing one.
	service.tasks[0].Title = "Renamed externally"
	service.tasks = append(service.tasks, makeTask(2, "Added externally", domain.PriorityUrgent, domain.LifecycleReady))

	model, cmd := update(t, model, key("r"))
	if !model.loading || cmd == nil {
		t.Fatalf("refresh must reload: loading=%v", model.loading)
	}
	model, _ = update(t, model, cmd())
	if model.selectedID != 1 {
		t.Fatalf("selection must survive refresh by ID, got %d", model.selectedID)
	}
	rendered := model.render()
	if !strings.Contains(rendered, "Renamed externally") || !strings.Contains(rendered, "Added externally") {
		t.Fatalf("external changes missing:\n%s", rendered)
	}
}

func TestInitialLoadErrorIsRecoverableViaRefresh(t *testing.T) {
	service := &fakeService{listErr: fmt.Errorf("disk detached")}
	model := load(t, New(t.Context(), service))
	rendered := model.render()
	if !strings.Contains(rendered, "Could not load tasks") || !strings.Contains(rendered, "disk detached") {
		t.Fatalf("storage error must be visible:\n%s", rendered)
	}

	service.listErr = nil
	service.tasks = []domain.Task{makeTask(1, "Recovered", domain.PriorityNormal, domain.LifecycleBacklog)}
	model, cmd := update(t, model, key("r"))
	model, _ = update(t, model, cmd())
	if model.err != nil || !strings.Contains(model.render(), "Recovered") {
		t.Fatalf("refresh must recover, err=%v", model.err)
	}
}

func TestFormFooterAdvertisesOnlyActiveKeys(t *testing.T) {
	model := load(t, New(t.Context(), &fakeService{tasks: []domain.Task{makeTask(1, "T", domain.PriorityNormal, domain.LifecycleBacklog)}}))
	model, _ = update(t, model, key("n"))
	if model.route != routeForm {
		t.Fatalf("route=%v", model.route)
	}
	rendered := model.render()
	if strings.Contains(rendered, "Q quit") {
		t.Fatalf("form footer advertises Q while Q is typeable text:\n%s", rendered)
	}
	// Q must reach the focused input as text, not quit the program.
	model, cmd := update(t, model, key("Q"))
	if cmd != nil && fmt.Sprintf("%T", cmd()) == "tea.QuitMsg" {
		t.Fatal("Q must not quit inside a form")
	}
	if got := model.form.input.Value(); got != "Q" {
		t.Fatalf("title input=%q", got)
	}
}
