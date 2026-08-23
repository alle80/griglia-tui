package tui

// Multiline description editing: the create/edit forms use a textarea for the
// description, which must preserve explicit newlines, keep pasted line
// breaks, wrap and scroll within the terminal, and never leak shortcut
// characters to global actions or lose content to the auto-refresh loop.

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/alle80/griglia-tui/internal/domain"
)

// typeString sends value to the model rune by rune, with "\n" delivered as
// the enter key, exactly like interactive typing.
func typeString(t *testing.T, model Model, value string) Model {
	t.Helper()
	for _, r := range value {
		if r == '\n' {
			model, _ = update(t, model, key("enter"))
			continue
		}
		model, _ = update(t, model, tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
	}
	return model
}

// openCreateFormAtDescription opens the create form and moves focus to the
// description textarea.
func openCreateFormAtDescription(t *testing.T, service *fakeService) Model {
	t.Helper()
	model := load(t, New(context.Background(), service))
	model, _ = update(t, model, key("n"))
	model, _ = update(t, model, key("tab"))
	if model.route != routeForm || model.form.focus != focusDescription {
		t.Fatalf("route=%v focus=%d", model.route, model.form.focus)
	}
	return model
}

func TestTypedMultilineDescriptionPreservesNewlinesAndBlankLines(t *testing.T) {
	service := &fakeService{}
	model := openCreateFormAtDescription(t, service)
	model = typeString(t, model, "first paragraph\n\n- item one\n- item two")
	if got := model.form.description.Value(); got != "first paragraph\n\n- item one\n- item two" {
		t.Fatalf("description=%q", got)
	}
	// Enter inside the textarea must insert a newline, never advance focus or
	// submit the form.
	if model.form.focus != focusDescription || model.form.saving || len(service.added) != 0 {
		t.Fatalf("enter left the textarea: focus=%d saving=%v adds=%d", model.form.focus, model.form.saving, len(service.added))
	}
}

func TestMultilinePasteKeepsLineBreaksInDescription(t *testing.T) {
	structured := "# Plan\n\n1. first step\n2. second step\n\nNotes: agents must keep newlines."
	for name, pasted := range map[string]string{
		"unix":    structured,
		"windows": strings.ReplaceAll(structured, "\n", "\r\n"),
	} {
		t.Run(name, func(t *testing.T) {
			model := openCreateFormAtDescription(t, &fakeService{})
			model, cmd := update(t, model, paste(pasted))
			if cmd != nil && reflect.TypeOf(cmd()) == reflect.TypeOf(tea.Quit()) {
				t.Fatal("paste returned the quit command")
			}
			if got := model.form.description.Value(); got != structured {
				t.Fatalf("description=%q, want %q", got, structured)
			}
		})
	}
}

func TestMultilineDescriptionIsCreatedAndEditedIntact(t *testing.T) {
	service := &fakeService{}
	model := load(t, New(context.Background(), service))
	model, _ = update(t, model, key("n"))
	model.form.input.SetValue("Structured task")
	model, _ = update(t, model, key("tab"))
	model, _ = update(t, model, paste("intro\n\n- a\n- b"))
	model, _ = update(t, model, key("tab"))
	model, cmd := update(t, model, key("enter"))
	if cmd == nil || !model.form.saving {
		t.Fatal("expected create command")
	}
	model = runCmd(t, model, cmd)
	if len(service.added) != 1 || service.added[0].Description != "intro\n\n- a\n- b" {
		t.Fatalf("added=%+v", service.added)
	}

	// Editing a task with a multiline description loads it verbatim and a
	// save without changes round-trips it.
	service.tasks[0].Description = "intro\n\n- a\n- b"
	model, cmd = update(t, model, key("r"))
	model = runCmd(t, model, cmd)
	model, _ = update(t, model, key("e"))
	if got := model.form.description.Value(); got != "intro\n\n- a\n- b" {
		t.Fatalf("edit form description=%q", got)
	}
	model.form.focus = focusPriority
	model, cmd = update(t, model, key("enter"))
	model = runCmd(t, model, cmd)
	if got := service.tasks[0].Description; got != "intro\n\n- a\n- b" {
		t.Fatalf("saved description=%q", got)
	}
}

func TestSingleLineDescriptionsStillEditNormally(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{makeTask(1, "Legacy", domain.PriorityNormal, domain.LifecycleBacklog)}}
	model := load(t, New(context.Background(), service))
	model, _ = update(t, model, key("e"))
	if got := model.form.description.Value(); got != "Description for Legacy" {
		t.Fatalf("description=%q", got)
	}
	model.form.focus = focusPriority
	model, cmd := update(t, model, key("enter"))
	model = runCmd(t, model, cmd)
	if service.tasks[0].Description != "Description for Legacy" {
		t.Fatalf("description mutated: %q", service.tasks[0].Description)
	}
}

func TestWideUnicodeDescriptionSurvivesTypingPasteAndNarrowRendering(t *testing.T) {
	content := "日本語の説明🚀\nsecond régime línea\nтретья строка"
	model := openCreateFormAtDescription(t, &fakeService{})
	model, _ = update(t, model, paste(content))
	model = typeString(t, model, "\n漢字")
	if got := model.form.description.Value(); got != content+"\n漢字" {
		t.Fatalf("description=%q", got)
	}
	for _, width := range []int{24, 40, 80} {
		model, _ = update(t, model, tea.WindowSizeMsg{Width: width, Height: 20})
		_ = model.render()
		if got := model.form.description.Value(); got != content+"\n漢字" {
			t.Fatalf("width %d changed the value: %q", width, got)
		}
	}
}

func TestFormFocusNavigationBetweenFields(t *testing.T) {
	model := load(t, New(context.Background(), &fakeService{}))
	model, _ = update(t, model, key("n"))
	// Tab cycles Title → Description → Priority → Title.
	for _, want := range []int{focusDescription, focusPriority, focusTitle} {
		model, _ = update(t, model, key("tab"))
		if model.form.focus != want {
			t.Fatalf("focus=%d, want %d", model.form.focus, want)
		}
	}
	// Shift-Tab cycles backwards, wrapping through priority.
	shiftTab := tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift})
	for _, want := range []int{focusPriority, focusDescription, focusTitle} {
		model, _ = update(t, model, shiftTab)
		if model.form.focus != want {
			t.Fatalf("shift+tab focus=%d, want %d", model.form.focus, want)
		}
	}
	// Enter advances from the title, down moves from title to description,
	// and up/down inside the description move the cursor instead of focus.
	model, _ = update(t, model, key("enter"))
	if model.form.focus != focusDescription {
		t.Fatalf("enter on title focus=%d", model.form.focus)
	}
	model = typeString(t, model, "one\ntwo")
	if model.form.description.Line() != 1 {
		t.Fatalf("cursor line=%d", model.form.description.Line())
	}
	model, _ = update(t, model, key("up"))
	if model.form.focus != focusDescription || model.form.description.Line() != 0 {
		t.Fatalf("up left the textarea: focus=%d line=%d", model.form.focus, model.form.description.Line())
	}
	model, _ = update(t, model, key("down"))
	if model.form.focus != focusDescription || model.form.description.Line() != 1 {
		t.Fatalf("down left the textarea: focus=%d line=%d", model.form.focus, model.form.description.Line())
	}
	// Tab leaves the textarea without inserting a tab character.
	model, _ = update(t, model, key("tab"))
	if model.form.focus != focusPriority || model.form.description.Value() != "one\ntwo" {
		t.Fatalf("tab from textarea: focus=%d value=%q", model.form.focus, model.form.description.Value())
	}
}

func TestPrintableShortcutsAreTextInDescriptionTextarea(t *testing.T) {
	for _, printable := range []string{"e", "a", "d", "x", "Q", "w", "b", "r", "q", "n", "j", "k", "?"} {
		t.Run(printable, func(t *testing.T) {
			service := &fakeService{tasks: []domain.Task{makeTask(1, "Original", domain.PriorityNormal, domain.LifecycleBacklog)}}
			model := load(t, New(context.Background(), service))
			model, _ = update(t, model, key("e"))
			model, _ = update(t, model, key("tab"))
			model.form.description.SetValue("")
			model, cmd := update(t, model, key(printable))
			if cmd != nil && reflect.TypeOf(cmd()) == reflect.TypeOf(tea.Quit()) {
				t.Fatalf("%q returned the quit command", printable)
			}
			if model.route != routeForm || model.form.focus != focusDescription {
				t.Fatalf("%q changed route/focus: %v %d", printable, model.route, model.form.focus)
			}
			if got := model.form.description.Value(); got != printable {
				t.Fatalf("description=%q, want %q", got, printable)
			}
			if task := service.tasks[0]; task.Title != "Original" || task.Lifecycle != domain.LifecycleBacklog {
				t.Fatalf("shortcut triggered a task action: %+v", task)
			}
		})
	}
}

func TestPasteShortcutSoupIntoDescriptionIsInert(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{makeTask(1, "Original", domain.PriorityNormal, domain.LifecycleBacklog)}}
	model := load(t, New(context.Background(), service))
	model, _ = update(t, model, key("e"))
	model, _ = update(t, model, key("tab"))
	model.form.description.SetValue("")
	model, cmd := update(t, model, paste(shortcutSoup+"\n"+shortcutSoup))
	if cmd != nil && reflect.TypeOf(cmd()) == reflect.TypeOf(tea.Quit()) {
		t.Fatal("paste returned the quit command")
	}
	if got := model.form.description.Value(); got != shortcutSoup+"\n"+shortcutSoup {
		t.Fatalf("description=%q", got)
	}
	if task := service.tasks[0]; task.Title != "Original" || task.Lifecycle != domain.LifecycleBacklog {
		t.Fatalf("paste triggered a task action: %+v", task)
	}
}

func TestDescriptionHeightTracksTerminalAndScrollsInternally(t *testing.T) {
	model := load(t, New(context.Background(), &fakeService{}))
	model, _ = update(t, model, tea.WindowSizeMsg{Width: 80, Height: 24})
	model, _ = update(t, model, key("n"))
	model, _ = update(t, model, key("tab"))
	if got := model.form.description.Height(); got != 11 {
		t.Fatalf("height=%d, want 11 at 24 rows", got)
	}
	// Forty lines never fit: the widget scrolls to keep the cursor visible
	// while the form itself keeps its footer within the terminal height.
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = strings.Repeat("x", 10)
	}
	model, _ = update(t, model, paste(strings.Join(lines, "\n")))
	if model.form.description.LineCount() != 40 {
		t.Fatalf("line count=%d", model.form.description.LineCount())
	}
	if model.form.description.ScrollYOffset() == 0 {
		t.Fatal("textarea did not scroll to the cursor")
	}
	rendered := strings.Split(model.render(), "\n")
	if len(rendered) > 24 {
		t.Fatalf("form renders %d lines for height 24", len(rendered))
	}
	if !strings.Contains(model.render(), "Priority") || !strings.Contains(model.render(), "esc cancel") {
		t.Fatalf("scrolling description pushed fields off screen:\n%s", model.render())
	}
	// Scrolling back to the top is possible from inside the widget.
	model.form.description.MoveToBegin()
	if model.form.description.ScrollYOffset() != 0 {
		t.Fatalf("offset=%d after moving to begin", model.form.description.ScrollYOffset())
	}
}

func TestDescriptionReflowsOnResize(t *testing.T) {
	content := "a long paragraph that certainly wraps somewhere\nshort\nthird line"
	model := openCreateFormAtDescription(t, &fakeService{})
	model, _ = update(t, model, paste(content))
	small, tall := tea.WindowSizeMsg{Width: 30, Height: 16}, tea.WindowSizeMsg{Width: 100, Height: 40}
	model, _ = update(t, model, small)
	if got := model.form.description.Height(); got != 3 {
		t.Fatalf("height=%d, want minimum 3 at 16 rows", got)
	}
	if got := model.form.description.Width(); got > 22 {
		t.Fatalf("width=%d for a 30-column terminal", got)
	}
	_ = model.render()
	model, _ = update(t, model, tall)
	if got := model.form.description.Height(); got != 12 {
		t.Fatalf("height=%d, want capped 12 at 40 rows", got)
	}
	_ = model.render()
	if got := model.form.description.Value(); got != content {
		t.Fatalf("resize changed the value: %q", got)
	}
}

func TestAutoRefreshDoesNotDisturbDescriptionTextarea(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{makeTask(1, "Parser", domain.PriorityHigh, domain.LifecycleReady)}}
	model := load(t, New(context.Background(), service))
	model, _ = update(t, model, key("e"))
	model, _ = update(t, model, key("tab"))
	model.form.description.SetValue("")
	model, _ = update(t, model, paste("keep\n\nthis exact\ncontent"))
	model, _ = update(t, model, key("up"))
	line, offset := model.form.description.Line(), model.form.description.ScrollYOffset()
	listCalls := service.listCalls
	for i := 0; i < 3; i++ {
		var cmd tea.Cmd
		model, cmd = update(t, model, tickMsg{})
		model = runCmd(t, model, cmd)
	}
	if model.route != routeForm || service.listCalls != listCalls {
		t.Fatalf("tick refreshed behind the form: route=%v calls=%d→%d", model.route, listCalls, service.listCalls)
	}
	if got := model.form.description.Value(); got != "keep\n\nthis exact\ncontent" {
		t.Fatalf("tick disturbed the description: %q", got)
	}
	if model.form.focus != focusDescription || model.form.description.Line() != line || model.form.description.ScrollYOffset() != offset {
		t.Fatalf("tick moved focus/cursor: focus=%d line=%d offset=%d", model.form.focus, model.form.description.Line(), model.form.description.ScrollYOffset())
	}
}
