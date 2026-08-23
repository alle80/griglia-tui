package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/alle80/griglia-tui/internal/domain"
)

const longDescription = "Improve long-description rendering in the task detail view so that every word remains readable. " +
	"The content must wrap to the available width, preserve paragraph structure, respect wide characters, " +
	"reflow on resize, and scroll when it does not fit vertically. Nothing may be truncated or silently discarded."

// openDetail loads the model and enters the detail view of the first task.
func openDetail(t *testing.T, service *fakeService, width, height int) Model {
	t.Helper()
	model := load(t, New(context.Background(), service))
	model, _ = update(t, model, tea.WindowSizeMsg{Width: width, Height: height})
	model, cmd := update(t, model, key("enter"))
	model = runCmd(t, model, cmd)
	if model.route != routeDetail {
		t.Fatalf("route=%v", model.route)
	}
	return model
}

func longTask(description string) domain.Task {
	task := makeTask(1, "Wrapped", domain.PriorityNormal, domain.LifecycleReady)
	task.Description = description
	return task
}

// scrollToBottom returns the concatenation of every line ever visible while
// scrolling from the current position to the bottom.
func scrollToBottom(t *testing.T, model Model) (Model, string) {
	t.Helper()
	seen := model.render()
	for i := 0; i < 500; i++ {
		before := model.detailScroll
		model, _ = update(t, model, key("j"))
		if model.detailScroll == before {
			return model, seen
		}
		seen += "\n" + model.render()
	}
	t.Fatal("scrolling did not reach the bottom")
	return model, seen
}

func TestDetailWrapsDescriptionToWidthWithoutLosingContent(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{longTask(longDescription)}}
	model := openDetail(t, service, 60, 40)
	for _, line := range strings.Split(model.render(), "\n") {
		if lipgloss.Width(line) > 60 {
			t.Fatalf("line exceeds width 60: %q (%d)", line, lipgloss.Width(line))
		}
	}
	body := strings.Join(model.detailLines(model.tasks[0]), "\n")
	for _, word := range strings.Fields(longDescription) {
		if !strings.Contains(body, word) {
			t.Fatalf("wrapped detail lost %q", word)
		}
	}
}

func TestDetailPreservesExplicitLineBreaksAndParagraphs(t *testing.T) {
	description := "First paragraph line.\n\nSecond paragraph after a blank line.\nThird line.\r\nWindows line."
	service := &fakeService{tasks: []domain.Task{longTask(description)}}
	model := openDetail(t, service, 80, 40)
	body := model.detailLines(model.tasks[0])
	joined := strings.Join(body, "\n")
	for _, want := range []string{"First paragraph line.", "Second paragraph after a blank line.", "Third line.", "Windows line."} {
		if !strings.Contains(joined, want) {
			t.Fatalf("detail missing %q: %q", want, joined)
		}
	}
	if !strings.Contains(joined, "First paragraph line.\n\nSecond paragraph") {
		t.Fatalf("blank line between paragraphs lost: %q", joined)
	}
	if strings.Contains(joined, "\r") {
		t.Fatalf("carriage return leaked into rendering: %q", joined)
	}
}

func TestDetailWrapsWideUnicodeWithinDisplayWidth(t *testing.T) {
	description := strings.Repeat("漢字幅広テスト", 20)
	service := &fakeService{tasks: []domain.Task{longTask(description)}}
	model := openDetail(t, service, 30, 40)
	var rejoined strings.Builder
	for _, line := range model.detailLines(model.tasks[0]) {
		if lipgloss.Width(line) > 28 {
			t.Fatalf("wide-character line exceeds wrap width 28: %q (%d)", line, lipgloss.Width(line))
		}
		rejoined.WriteString(line)
	}
	if !strings.Contains(rejoined.String(), description) {
		t.Fatal("wide-character description content was altered by wrapping")
	}
}

func TestDetailReflowsOnResize(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{longTask(longDescription)}}
	model := openDetail(t, service, 100, 40)
	wideLines := len(model.detailLines(model.tasks[0]))
	model, _ = update(t, model, tea.WindowSizeMsg{Width: 40, Height: 40})
	narrowLines := len(model.detailLines(model.tasks[0]))
	if narrowLines <= wideLines {
		t.Fatalf("narrow rewrap should produce more lines: %d vs %d", narrowLines, wideLines)
	}
	for _, line := range strings.Split(model.render(), "\n") {
		if lipgloss.Width(line) > 40 {
			t.Fatalf("line exceeds resized width 40: %q", line)
		}
	}
}

func TestDetailScrollingRevealsAllContentAndClampsAtBoundaries(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{longTask(longDescription)}}
	model := openDetail(t, service, 40, 12)
	if model.detailMaxScroll() == 0 {
		t.Fatal("long description should overflow a 12-row terminal")
	}
	// Top boundary: k does not move above the top.
	model, _ = update(t, model, key("k"))
	if model.detailScroll != 0 {
		t.Fatalf("scrolled above top: %d", model.detailScroll)
	}
	if !strings.Contains(model.render(), "j/k scroll") || !strings.Contains(model.render(), fmt.Sprintf("/%d", len(model.detailLines(model.tasks[0])))) {
		t.Fatalf("missing scroll affordances: %q", model.render())
	}
	model, _ = update(t, model, key("j"))
	if model.detailScroll != 1 {
		t.Fatalf("j did not scroll: %d", model.detailScroll)
	}
	// Every word of the description becomes visible on the way down, and the
	// bottom boundary clamps.
	model, seen := scrollToBottom(t, model)
	for _, word := range strings.Fields(longDescription) {
		if !strings.Contains(seen, word) {
			t.Fatalf("word never became visible while scrolling: %q", word)
		}
	}
	if model.detailScroll != model.detailMaxScroll() {
		t.Fatalf("bottom scroll=%d max=%d", model.detailScroll, model.detailMaxScroll())
	}
	model, _ = update(t, model, key("j"))
	if model.detailScroll != model.detailMaxScroll() {
		t.Fatalf("scrolled past bottom: %d", model.detailScroll)
	}
	// Page keys jump by a viewport and clamp at the top.
	model, _ = update(t, model, tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}))
	if want := model.detailMaxScroll() - model.detailViewportHeight(); model.detailScroll != max(0, want) {
		t.Fatalf("pgup scroll=%d want=%d", model.detailScroll, max(0, want))
	}
	for i := 0; i < 100; i++ {
		model, _ = update(t, model, tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}))
	}
	if model.detailScroll != 0 {
		t.Fatalf("pgup did not clamp at top: %d", model.detailScroll)
	}
	model, _ = update(t, model, tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown}))
	if model.detailScroll != min(model.detailViewportHeight(), model.detailMaxScroll()) {
		t.Fatalf("pgdown scroll=%d", model.detailScroll)
	}
}

func TestDetailScrollKeysDoNotLeakIntoListOrActions(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{longTask(longDescription), makeTask(2, "Other", domain.PriorityLow, domain.LifecycleReady)}}
	model := openDetail(t, service, 40, 12)
	model, _ = update(t, model, key("j"))
	if model.selected != 0 || model.selectedID != 1 {
		t.Fatalf("detail scroll moved the list selection: %d/%d", model.selected, model.selectedID)
	}
	if service.tasks[0].Lifecycle != domain.LifecycleReady {
		t.Fatalf("scroll key mutated the task: %+v", service.tasks[0])
	}
	// Back in the list, j moves the selection again.
	model, _ = update(t, model, key("q"))
	model, _ = update(t, model, key("j"))
	if model.selectedID != 2 {
		t.Fatalf("list selection after leaving detail: %d", model.selectedID)
	}
}

func TestDetailHeightStaysBoundedInSmallTerminals(t *testing.T) {
	for _, size := range []struct{ w, h int }{{120, 30}, {80, 24}, {38, 10}, {20, 6}, {5, 3}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			service := &fakeService{tasks: []domain.Task{longTask(longDescription)}}
			model := openDetail(t, service, size.w, size.h)
			for i := 0; i < 3; i++ {
				if rows := len(strings.Split(model.render(), "\n")); rows > size.h {
					t.Fatalf("render has %d rows for height %d", rows, size.h)
				}
				model, _ = update(t, model, key("j"))
			}
		})
	}
}

func TestDetailScrollSurvivesAutoRefresh(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{longTask(longDescription)}}
	model := openDetail(t, service, 40, 12)
	for i := 0; i < 3; i++ {
		model, _ = update(t, model, key("j"))
	}
	before := model.render()
	if model.detailScroll != 3 {
		t.Fatalf("scroll=%d", model.detailScroll)
	}
	model, cmd := update(t, model, tickMsg{})
	model = runCmd(t, model, cmd)
	if model.detailScroll != 3 || model.render() != before {
		t.Fatalf("auto-refresh moved the scroll position: %d\n%q", model.detailScroll, model.render())
	}
}

func TestDetailScrollClampsWhenRefreshedContentShrinks(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{longTask(longDescription)}}
	model := openDetail(t, service, 40, 12)
	model, _ = scrollToBottom(t, model)
	// An external edit replaces the description with a short one.
	service.tasks[0].Description = "Short now."
	model, cmd := update(t, model, tickMsg{})
	model = runCmd(t, model, cmd)
	view := model.render()
	if !strings.Contains(view, "Short now.") {
		t.Fatalf("refreshed description missing: %q", view)
	}
	if rows := len(strings.Split(view, "\n")); rows > 12 {
		t.Fatalf("clamped view has %d rows", rows)
	}
	// The very next k keypress moves within the new bounds — no dead keys
	// from a stale offset.
	model, _ = update(t, model, key("k"))
	if model.detailScroll > model.detailMaxScroll() {
		t.Fatalf("stale scroll offset: %d > %d", model.detailScroll, model.detailMaxScroll())
	}
	model, _ = update(t, model, key("q"))
	model, cmd = update(t, model, key("enter"))
	model = runCmd(t, model, cmd)
	if model.detailScroll != 0 {
		t.Fatalf("reopening detail should reset scroll: %d", model.detailScroll)
	}
}

func TestDetailErrorStaysPinnedWhileScrolled(t *testing.T) {
	service := &fakeService{tasks: []domain.Task{longTask(longDescription)}, actionErr: errors.New("task is actively claimed")}
	model := openDetail(t, service, 40, 14)
	model, cmd := update(t, model, key("d"))
	model = runCmd(t, model, cmd)
	if model.route != routeDetail {
		// The failed lifecycle action returns to the list; reopen detail.
		model, cmd = update(t, model, key("enter"))
		model = runCmd(t, model, cmd)
	}
	model, _ = update(t, model, key("j"))
	view := model.render()
	if !strings.Contains(view, "actively claimed") {
		t.Fatalf("pinned error missing while scrolled: %q", view)
	}
	if rows := len(strings.Split(view, "\n")); rows > 14 {
		t.Fatalf("view with pinned error has %d rows", rows)
	}
}
