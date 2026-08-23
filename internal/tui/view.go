package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/alle80/griglia-tui/internal/domain"
)

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	mutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	errorStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
	statusStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("35"))
	panelStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
)

func (m Model) View() tea.View {
	content := m.render()
	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = "Griglia"
	return view
}

func (m Model) render() string {
	var body string
	switch m.route {
	case routeDetail:
		body = m.detailView()
	case routeForm:
		body = m.formView()
	case routeHelp:
		body = m.helpView()
	case routeQuestions:
		body = m.questionsView()
	case routeDependencies:
		body = m.dependenciesView()
	default:
		body = m.listView()
	}
	return fitHeight(body, m.height)
}

func (m Model) header() string {
	count := fmt.Sprintf("tasks: %d", len(m.tasks))
	space := m.width - lipgloss.Width("GRIGLIA") - lipgloss.Width(count) - 4
	if space < 1 {
		space = 1
	}
	return titleStyle.Render("GRIGLIA") + strings.Repeat(" ", space) + mutedStyle.Render(count)
}

func (m Model) listView() string {
	lines := []string{m.header(), mutedStyle.Render(strings.Repeat("─", max(1, m.width-2)))}
	if m.loading {
		lines = append(lines, "Loading tasks…")
	} else if m.err != nil {
		lines = append(lines, errorStyle.Render("Could not load tasks: "+m.err.Error()), "Press r to retry.")
	} else if len(m.tasks) == 0 {
		lines = append(lines, "No tasks yet.", mutedStyle.Render("Press n to create your first task."))
	} else {
		start, end := m.visibleRange()
		for i := start; i < end; i++ {
			lines = append(lines, m.taskRow(m.tasks[i], i == m.selected)...)
		}
		if m.width >= 60 && m.width < 100 {
			lines = append(lines, "")
			// Clip each preview line: unbounded lines would wrap in the
			// terminal and push the footer past the height budget.
			for _, line := range strings.Split(m.preview(m.tasks[m.selected]), "\n") {
				lines = append(lines, truncate(line, max(16, m.width-1)))
			}
		}
		if m.width >= 100 {
			list := strings.Join(lines[2:], "\n")
			body := lipgloss.JoinHorizontal(lipgloss.Top, panelStyle.Width(m.width/2-4).Render(list), "  ", panelStyle.Width(m.width/2-4).Render(m.preview(m.tasks[m.selected])))
			lines = append(lines[:2], body)
		}
	}
	if m.status != "" {
		lines = append(lines, "", statusStyle.Render("✓ "+m.status))
	}
	if m.actionErr != nil {
		lines = append(lines, "", errorStyle.Render(m.actionErr.Error()))
	}
	lines = append(lines, "", mutedStyle.Render("j/k move · enter detail · n new · e edit · w questions · ? help · Q quit"))
	return strings.Join(lines, "\n")
}

func (m Model) visibleRange() (int, int) {
	rows := max(1, m.height-8)
	if m.width < 60 {
		rows = max(1, rows/2)
	}
	start := 0
	if m.selected >= rows {
		start = m.selected - rows + 1
	}
	end := min(len(m.tasks), start+rows)
	return start, end
}

func (m Model) taskRow(task domain.TaskView, selected bool) []string {
	marker := "  "
	if selected {
		marker = "> "
	}
	state := fmt.Sprintf("[%s]", task.Lifecycle)
	if task.OperationalState != nil {
		state = fmt.Sprintf("[%s]", *task.OperationalState)
	}
	priority := strings.ToUpper(string(task.Priority))
	if m.width < 60 {
		first := truncate(fmt.Sprintf("%s#%d %s", marker, task.ID, task.Title), max(12, m.width-1))
		second := fmt.Sprintf("    %s · %s · %d%%", state, priority, task.Progress)
		if selected {
			return []string{selectedStyle.Render(first), selectedStyle.Render(second)}
		}
		return []string{first, mutedStyle.Render(second)}
	}
	// Pad by display cells, not runes: fmt's %-*s would misalign and
	// overflow the row when the title contains wide characters.
	target := max(16, m.width-36)
	left := truncate(fmt.Sprintf("%s#%-4d %s", marker, task.ID, task.Title), target)
	row := left + strings.Repeat(" ", max(0, target-lipgloss.Width(left))) + fmt.Sprintf("  %-19s %s", state, priority)
	if selected {
		row = selectedStyle.Render(row)
	}
	return []string{row}
}

func (m Model) preview(task domain.TaskView) string {
	state := string(task.Lifecycle)
	if task.OperationalState != nil {
		state = string(*task.OperationalState)
	}
	owner := ""
	if task.ActiveClaim != nil {
		owner = fmt.Sprintf("\n%s · %s", task.ActiveClaim.AgentName, task.ActiveClaim.InstanceID)
	}
	phase := ""
	if task.Phase != "" {
		phase = "\n" + task.Phase
	}
	return fmt.Sprintf("#%d — %s\n%s · %s · %d%%%s%s\n%s", task.ID, task.Title, state, task.Priority, task.Progress, owner, phase, empty(task.Description))
}

// wrapLines wraps text to the given display width — Unicode- and
// wide-character-aware — preserving explicit line breaks and blank lines.
func wrapLines(text string, width int) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.Split(ansi.Wrap(text, width, ""), "\n")
}

// detailWrapWidth is the display width detail text wraps to.
func (m Model) detailWrapWidth() int { return max(1, m.width-2) }

// detailLines is the scrollable body of the detail view: everything between
// the pinned heading and the pinned error/footer, with free-form text wrapped
// to the terminal width. Nothing is ever truncated here; content beyond the
// viewport is reachable by scrolling.
func (m Model) detailLines(task domain.TaskView) []string {
	state := "—"
	if task.OperationalState != nil {
		state = string(*task.OperationalState)
	}
	lines := []string{"", task.Title, "", "Lifecycle: " + string(task.Lifecycle), "Operational state: " + state, "Priority: " + string(task.Priority), fmt.Sprintf("Progress: %d%%", task.Progress)}
	if task.OperationalState != nil && *task.OperationalState == domain.OperationalWaitingForHuman {
		lines = append(lines, "!! Waiting for human input — press w to view and answer questions")
	}
	if task.OperationalState != nil && *task.OperationalState == domain.OperationalBlocked {
		lines = append(lines, "!! Blocked by unsatisfied dependencies — press b to inspect")
	}
	if task.ActiveClaim != nil {
		lines = append(lines, "Agent: "+task.ActiveClaim.AgentName, "Instance: "+task.ActiveClaim.InstanceID)
	}
	if m.dependenciesTaskID == task.ID && len(m.dependencies) > 0 {
		lines = append(lines, "", "Dependencies")
		for _, dependency := range m.dependencies {
			lines = append(lines, dependencyRow(dependency))
		}
	}
	if task.Phase != "" {
		lines = append(lines, "Phase: "+task.Phase)
	}
	lines = append(lines, "Created: "+task.CreatedAt.Local().Format(time.RFC822), "Updated: "+task.UpdatedAt.Local().Format(time.RFC822), "", "Description", empty(task.Description))
	if task.CompletionSummary != "" {
		lines = append(lines, "", "Completion summary", task.CompletionSummary)
	}
	// Wrapping the joined body wraps every row uniformly, so no line —
	// description, title, metadata, or dependency row — can overflow.
	return wrapLines(strings.Join(lines, "\n"), m.detailWrapWidth())
}

// detailHint is the pinned footer, wrapped to the terminal width.
func (m Model) detailHint(scrollable bool) []string {
	hint := "e edit · a ready · d done · x cancel · w questions · b dependencies · q back · ? help"
	if scrollable {
		hint = "j/k scroll · " + hint
	}
	return wrapLines(hint, m.detailWrapWidth())
}

// detailErrorRows counts the pinned error block: a separator plus the wrapped
// error text, zero when no error is showing.
func (m Model) detailErrorRows() int {
	if m.actionErr == nil {
		return 0
	}
	return 1 + len(wrapLines(m.actionErr.Error(), m.detailWrapWidth()))
}

// detailViewportFor sizes the scrollable window for a body of contentLen
// rows: terminal height minus the pinned heading, error block, and footer.
// The footer grows a "j/k scroll" prefix when content overflows, which can
// only shrink the viewport further, so the second pass is final.
func (m Model) detailViewportFor(contentLen int) int {
	base := m.height - 2 - m.detailErrorRows()
	height := max(1, base-len(m.detailHint(false)))
	if contentLen > height {
		height = max(1, base-len(m.detailHint(true)))
	}
	return height
}

func (m Model) detailViewportHeight() int {
	if len(m.tasks) == 0 {
		return 1
	}
	return m.detailViewportFor(len(m.detailLines(m.tasks[m.selected])))
}

func (m Model) detailMaxScroll() int {
	if len(m.tasks) == 0 {
		return 0
	}
	return max(0, len(m.detailLines(m.tasks[m.selected]))-m.detailViewportHeight())
}

func (m Model) detailView() string {
	if len(m.tasks) == 0 {
		return "No selected task.\n\nq back · Q quit"
	}
	task := m.tasks[m.selected]
	content := m.detailLines(task)
	height := m.detailViewportFor(len(content))
	scrollable := len(content) > height
	// The scroll offset survives refreshes and resizes and is only clamped
	// here: when content shrinks or the window grows, the position moves up
	// just enough to stay within bounds instead of resetting to the top.
	scroll := min(m.detailScroll, max(0, len(content)-height))
	end := min(len(content), scroll+height)
	heading := titleStyle.Render(fmt.Sprintf("TASK #%d", task.ID))
	if scrollable {
		position := fmt.Sprintf("%d-%d/%d", scroll+1, end, len(content))
		space := m.width - lipgloss.Width(heading) - lipgloss.Width(position) - 2
		if space < 1 {
			space = 1
		}
		heading += strings.Repeat(" ", space) + mutedStyle.Render(position)
	}
	lines := append([]string{heading}, content[scroll:end]...)
	if m.actionErr != nil {
		lines = append(lines, "")
		for _, line := range wrapLines(m.actionErr.Error(), m.detailWrapWidth()) {
			lines = append(lines, errorStyle.Render(line))
		}
	}
	lines = append(lines, "")
	for _, line := range m.detailHint(scrollable) {
		lines = append(lines, mutedStyle.Render(line))
	}
	return strings.Join(lines, "\n")
}

func dependencyRow(d domain.DependencyView) string {
	label := "unsatisfied"
	if d.Satisfied() {
		label = "satisfied"
	}
	return fmt.Sprintf("#%-4d %-9s %-12s %s", d.DependsOnTaskID, d.Lifecycle, label, d.Title)
}

func (m Model) dependenciesView() string {
	lines := []string{titleStyle.Render(fmt.Sprintf("DEPENDENCIES — TASK #%d", m.dependenciesTaskID)), ""}
	switch {
	case m.dependenciesLoad:
		lines = append(lines, "Loading dependencies…")
	case len(m.dependencies) == 0:
		lines = append(lines, "No dependencies for this task.", mutedStyle.Render("Press n to add a prerequisite."))
	default:
		for i, dependency := range m.dependencies {
			marker := "  "
			if i == m.depSelected {
				marker = "> "
			}
			row := truncate(marker+dependencyRow(dependency), max(16, m.width-2))
			if i == m.depSelected {
				row = selectedStyle.Render(row)
			}
			lines = append(lines, row)
		}
	}
	if m.status != "" {
		lines = append(lines, "", statusStyle.Render("✓ "+m.status))
	}
	if m.actionErr != nil {
		lines = append(lines, "", errorStyle.Render(m.actionErr.Error()))
	}
	lines = append(lines, "", mutedStyle.Render("j/k move · n add · x remove · r refresh · q back · Q quit"))
	return strings.Join(lines, "\n")
}

func (m Model) helpView() string {
	return strings.Join([]string{titleStyle.Render("HELP"), "", "j / ↓       select next task", "k / ↑       select previous task", "enter       open task detail", "            (in detail j/k scroll, pgup/pgdn page)", "n           create a task", "e           edit selected task", "a           mark backlog task ready", "d           complete ready task", "x           cancel backlog/ready task", "w           view and answer task questions", "b           inspect and edit task dependencies", "r           reload tasks", "?           open or close help", "q / esc     return to the list", "Q / ctrl+c  quit", "", mutedStyle.Render("Lifecycle actions are validated; errors are recoverable.")}, "\n")
}

func questionKind(blocking bool) string {
	if blocking {
		return "[BLOCKING]"
	}
	return "[info]"
}

func questionState(q domain.Question) string {
	switch {
	case q.Acknowledged():
		return "acknowledged"
	case q.Answered():
		return "answered"
	default:
		return "unanswered"
	}
}

func (m Model) questionsView() string {
	lines := []string{titleStyle.Render(fmt.Sprintf("QUESTIONS — TASK #%d", m.questionsTaskID)), ""}
	switch {
	case m.questionsLoading:
		lines = append(lines, "Loading questions…")
	case len(m.questions) == 0:
		lines = append(lines, "No questions for this task.")
	default:
		for i, question := range m.questions {
			marker := "  "
			if i == m.questionSelected {
				marker = "> "
			}
			row := truncate(fmt.Sprintf("%s#%-4d %-10s %-12s %s", marker, question.ID, questionKind(question.Blocking), questionState(question), question.Body), max(16, m.width-2))
			if i == m.questionSelected {
				row = selectedStyle.Render(row)
			}
			lines = append(lines, row)
		}
		selected := m.questions[m.questionSelected]
		lines = append(lines, "", mutedStyle.Render(strings.Repeat("─", max(1, m.width-2))), "Question: "+selected.Body)
		if selected.Answer != nil {
			lines = append(lines, "Answer: "+*selected.Answer)
		} else {
			lines = append(lines, "Answer: —")
		}
	}
	if m.status != "" {
		lines = append(lines, "", statusStyle.Render("✓ "+m.status))
	}
	if m.actionErr != nil {
		lines = append(lines, "", errorStyle.Render(m.actionErr.Error()))
	}
	lines = append(lines, "", mutedStyle.Render("j/k move · enter answer · r refresh · q back · Q quit"))
	return strings.Join(lines, "\n")
}

func (m Model) formView() string {
	if m.form.depending {
		lines := []string{titleStyle.Render(fmt.Sprintf("ADD DEPENDENCY — TASK #%d", m.form.taskID)), "", selectedStyle.Render("Prerequisite task ID"), m.form.input.View(), ""}
		if m.form.err != nil {
			lines = append(lines, errorStyle.Render(m.form.err.Error()), "")
		}
		if m.form.saving {
			lines = append(lines, "Saving…", "")
		}
		lines = append(lines, mutedStyle.Render("enter add dependency · esc back · Ctrl-C quit"))
		return strings.Join(lines, "\n")
	}
	if m.form.answering {
		lines := []string{titleStyle.Render(fmt.Sprintf("ANSWER QUESTION #%d", m.form.questionID)), "", m.form.questionBody, "", selectedStyle.Render("Answer"), m.form.input.View(), ""}
		if m.form.err != nil {
			lines = append(lines, errorStyle.Render(m.form.err.Error()), "")
		}
		if m.form.saving {
			lines = append(lines, "Saving…", "")
		}
		lines = append(lines, mutedStyle.Render("enter save answer · esc back · Ctrl-C quit"))
		return strings.Join(lines, "\n")
	}
	if m.form.cancelling {
		lines := []string{titleStyle.Render("CANCEL TASK"), "", selectedStyle.Render("Reason (optional)"), m.form.input.View(), ""}
		if m.form.err != nil {
			lines = append(lines, errorStyle.Render(m.form.err.Error()), "")
		}
		if m.form.saving {
			lines = append(lines, "Saving…", "")
		}
		lines = append(lines, mutedStyle.Render("enter cancel task · esc back · Ctrl-C quit"))
		return strings.Join(lines, "\n")
	}
	heading := "NEW TASK"
	if m.form.editing {
		heading = "EDIT TASK"
	}
	label := func(field int, text string) string {
		if field == m.form.focus {
			return selectedStyle.Render(text)
		}
		return text
	}
	lines := []string{titleStyle.Render(heading), "", label(focusTitle, "Title"), m.form.input.View(), "", label(focusDescription, "Description")}
	lines = append(lines, strings.Split(m.form.description.View(), "\n")...)
	lines = append(lines, "", label(focusPriority, "Priority"), m.form.priority.View(), "")
	if m.form.err != nil {
		lines = append(lines, errorStyle.Render(m.form.err.Error()), "")
	}
	if m.form.saving {
		lines = append(lines, "Saving…", "")
	}
	lines = append(lines, mutedStyle.Render("tab/shift+tab field · enter newline/save · esc cancel · Ctrl-C quit"))
	return strings.Join(lines, "\n")
}

func fitHeight(value string, height int) string {
	if height <= 0 {
		return value
	}
	lines := strings.Split(value, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

func truncate(value string, width int) string {
	if lipgloss.Width(value) <= width {
		return value
	}
	runes := []rune(value)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

func empty(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}
