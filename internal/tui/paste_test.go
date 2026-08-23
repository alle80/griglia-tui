package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/alle80/griglia-tui/internal/domain"
)

func paste(content string) tea.PasteMsg {
	return tea.PasteMsg{Content: content}
}

// Pasted text containing every global shortcut character must be inserted
// verbatim instead of executing actions or quitting.
const shortcutSoup = "qQeadxwbnr?jk"

func assertPastedIntoForm(t *testing.T, model Model, cmd tea.Cmd, want string) {
	t.Helper()
	if cmd != nil && reflect.TypeOf(cmd()) == reflect.TypeOf(tea.Quit()) {
		t.Fatal("paste returned the quit command")
	}
	if model.route != routeForm {
		t.Fatalf("paste changed route to %v", model.route)
	}
	if got := focusedValue(model.form); got != want {
		t.Fatalf("input value=%q, want %q", got, want)
	}
}

func TestPasteReachesEveryFieldOfCreateForm(t *testing.T) {
	model := load(t, New(context.Background(), &fakeService{}))
	model, _ = update(t, model, key("n"))
	model, cmd := update(t, model, paste("Pasted title"))
	assertPastedIntoForm(t, model, cmd, "Pasted title")
	model, _ = update(t, model, key("tab"))
	model, cmd = update(t, model, paste("Pasted description"))
	assertPastedIntoForm(t, model, cmd, "Pasted description")
	model, _ = update(t, model, key("tab"))
	model.form.priority.SetValue("")
	model, cmd = update(t, model, paste("high"))
	assertPastedIntoForm(t, model, cmd, "high")
	if model.form.input.Value() != "Pasted title" || model.form.description.Value() != "Pasted description" {
		t.Fatalf("earlier fields lost their pasted values: %q %q", model.form.input.Value(), model.form.description.Value())
	}
}

func TestPasteWithShortcutCharactersInEveryTextEntryRoute(t *testing.T) {
	newService := func() *fakeService {
		return &fakeService{
			tasks:     []domain.Task{makeTask(1, "Original", domain.PriorityNormal, domain.LifecycleBacklog)},
			questions: []domain.Question{makeQuestion(1, 1, "Open question", true)},
		}
	}
	openers := map[string]func(t *testing.T, model Model) Model{
		"create": func(t *testing.T, model Model) Model {
			model, _ = update(t, model, key("n"))
			return model
		},
		"edit": func(t *testing.T, model Model) Model {
			model, _ = update(t, model, key("e"))
			model.form.input.SetValue("")
			return model
		},
		"cancel-reason": func(t *testing.T, model Model) Model {
			model, _ = update(t, model, key("x"))
			return model
		},
		"answer": func(t *testing.T, model Model) Model {
			model, cmd := update(t, model, key("w"))
			model = runCmd(t, model, cmd)
			model, _ = update(t, model, key("enter"))
			return model
		},
		"dependency": func(t *testing.T, model Model) Model {
			model, cmd := update(t, model, key("b"))
			model = runCmd(t, model, cmd)
			model, _ = update(t, model, key("n"))
			return model
		},
	}
	for name, open := range openers {
		t.Run(name, func(t *testing.T) {
			service := newService()
			model := open(t, load(t, New(context.Background(), service)))
			if model.route != routeForm {
				t.Fatalf("route=%v", model.route)
			}
			model, cmd := update(t, model, paste(shortcutSoup))
			assertPastedIntoForm(t, model, cmd, shortcutSoup)
			if task := service.tasks[0]; task.Title != "Original" || task.Lifecycle != domain.LifecycleBacklog {
				t.Fatalf("paste triggered a task action: %+v", task)
			}
			if service.questions[0].Answer != nil {
				t.Fatalf("paste answered a question: %+v", service.questions[0])
			}
			if len(service.dependencies) != 0 {
				t.Fatalf("paste mutated dependencies: %+v", service.dependencies)
			}
		})
	}
}

func TestMultilinePasteCollapsesToSpacesInSingleLineFields(t *testing.T) {
	model := load(t, New(context.Background(), &fakeService{}))
	model, _ = update(t, model, key("n"))
	model, cmd := update(t, model, paste("line one\nline two"))
	assertPastedIntoForm(t, model, cmd, "line one line two")
	model.form.input.SetValue("")
	model, cmd = update(t, model, paste("crlf one\r\ncrlf two\ttabbed"))
	assertPastedIntoForm(t, model, cmd, "crlf one crlf two tabbed")
}

func TestPasteOutsideFormsIsDiscarded(t *testing.T) {
	service := &fakeService{
		tasks:     []domain.Task{makeTask(1, "Original", domain.PriorityNormal, domain.LifecycleBacklog)},
		questions: []domain.Question{makeQuestion(1, 1, "Open question", true)},
	}
	model := load(t, New(context.Background(), service))
	routes := map[string]func(t *testing.T, model Model) Model{
		"list": func(t *testing.T, model Model) Model { return model },
		"detail": func(t *testing.T, model Model) Model {
			model, cmd := update(t, model, key("enter"))
			return runCmd(t, model, cmd)
		},
		"help": func(t *testing.T, model Model) Model {
			model, _ = update(t, model, key("?"))
			return model
		},
		"questions": func(t *testing.T, model Model) Model {
			model, cmd := update(t, model, key("w"))
			return runCmd(t, model, cmd)
		},
		"dependencies": func(t *testing.T, model Model) Model {
			model, cmd := update(t, model, key("b"))
			return runCmd(t, model, cmd)
		},
	}
	for name, open := range routes {
		t.Run(name, func(t *testing.T) {
			model := open(t, model)
			before := model.route
			model, cmd := update(t, model, paste(shortcutSoup))
			if cmd != nil {
				t.Fatal("paste outside a form should produce no command")
			}
			if model.route != before {
				t.Fatalf("paste changed route from %v to %v", before, model.route)
			}
			if task := service.tasks[0]; task.Title != "Original" || task.Lifecycle != domain.LifecycleBacklog {
				t.Fatalf("paste triggered a task action: %+v", task)
			}
		})
	}
}

func TestPasteWhileSavingIsIgnored(t *testing.T) {
	model := load(t, New(context.Background(), &fakeService{}))
	model, _ = update(t, model, key("n"))
	model.form.input.SetValue("Task")
	model.form.saving = true
	model, cmd := update(t, model, paste("late paste"))
	if cmd != nil || model.form.input.Value() != "Task" {
		t.Fatalf("paste while saving mutated the form: %q", model.form.input.Value())
	}
}

func TestPasteClearsRecoverableFormError(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{makeTask(2, "Backend", domain.PriorityUrgent, domain.LifecycleReady)}}
	model := load(t, New(context.Background(), service))
	model, cmd := update(t, model, key("b"))
	model = runCmd(t, model, cmd)
	model, _ = update(t, model, key("n"))
	model.form.input.SetValue("nope")
	model, _ = update(t, model, key("enter"))
	if model.form.err == nil {
		t.Fatal("expected validation error")
	}
	model.form.input.SetValue("")
	model, cmd = update(t, model, paste("42"))
	if model.form.err != nil {
		t.Fatal("paste should clear the recoverable error")
	}
	assertPastedIntoForm(t, model, cmd, "42")
	model, cmd = update(t, model, key("enter"))
	if cmd == nil || !model.form.saving {
		t.Fatal("expected add command after pasting a valid ID")
	}
	model = runCmd(t, model, cmd)
	if len(service.dependencies) != 1 || service.dependencies[0].DependsOnTaskID != 42 {
		t.Fatalf("dependencies=%+v", service.dependencies)
	}
}

func TestCtrlCKeyStillForceQuitsEverywhere(t *testing.T) {
	ctrlC := tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl})
	model := load(t, New(context.Background(), &fakeService{}))
	_, cmd := update(t, model, ctrlC)
	if cmd == nil || reflect.TypeOf(cmd()) != reflect.TypeOf(tea.Quit()) {
		t.Fatal("ctrl+c should quit from the list")
	}
	model, _ = update(t, model, key("n"))
	_, cmd = update(t, model, ctrlC)
	if cmd == nil || reflect.TypeOf(cmd()) != reflect.TypeOf(tea.Quit()) {
		t.Fatal("ctrl+c should quit from a form")
	}
}

func TestPasteCharLimitIsRespected(t *testing.T) {
	model := load(t, New(context.Background(), &fakeService{}))
	model, _ = update(t, model, key("n"))
	model, _ = update(t, model, paste(strings.Repeat("x", domain.MaxTitleLength+50)))
	if got := len(model.form.input.Value()); got != domain.MaxTitleLength {
		t.Fatalf("title length=%d, want %d", got, domain.MaxTitleLength)
	}
}
