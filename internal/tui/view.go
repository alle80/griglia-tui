package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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
			lines = append(lines, "", m.preview(m.tasks[m.selected]))
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
	lines = append(lines, "", mutedStyle.Render("j/k move · enter detail · n new · ? help · r reload · Q quit"))
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

func (m Model) taskRow(task domain.Task, selected bool) []string {
	marker := "  "
	if selected {
		marker = "> "
	}
	state := fmt.Sprintf("[%s]", task.Lifecycle)
	priority := strings.ToUpper(string(task.Priority))
	if m.width < 60 {
		first := truncate(fmt.Sprintf("%s#%d %s", marker, task.ID, task.Title), max(12, m.width-1))
		second := fmt.Sprintf("    %s · %s · %d%%", state, priority, task.Progress)
		if selected {
			return []string{selectedStyle.Render(first), selectedStyle.Render(second)}
		}
		return []string{first, mutedStyle.Render(second)}
	}
	left := truncate(fmt.Sprintf("%s#%-4d %s", marker, task.ID, task.Title), max(16, m.width-28))
	row := fmt.Sprintf("%-*s  %-11s %s", max(16, m.width-28), left, state, priority)
	if selected {
		row = selectedStyle.Render(row)
	}
	return []string{row}
}

func (m Model) preview(task domain.Task) string {
	return fmt.Sprintf("#%d — %s\n%s · %s · %d%%\n%s", task.ID, task.Title, task.Lifecycle, task.Priority, task.Progress, empty(task.Description))
}

func (m Model) detailView() string {
	if len(m.tasks) == 0 {
		return "No selected task.\n\nq back · Q quit"
	}
	task := m.tasks[m.selected]
	lines := []string{titleStyle.Render(fmt.Sprintf("TASK #%d", task.ID)), "", task.Title, "", "Lifecycle: " + string(task.Lifecycle), "Priority: " + string(task.Priority), fmt.Sprintf("Progress: %d%%", task.Progress)}
	if task.Phase != "" {
		lines = append(lines, "Phase: "+task.Phase)
	}
	lines = append(lines, "Created: "+task.CreatedAt.Local().Format(time.RFC822), "Updated: "+task.UpdatedAt.Local().Format(time.RFC822), "", "Description", empty(task.Description))
	if task.CompletionSummary != "" {
		lines = append(lines, "", "Completion summary", task.CompletionSummary)
	}
	lines = append(lines, "", mutedStyle.Render("q back · ? help · Q quit"))
	return strings.Join(lines, "\n")
}

func (m Model) helpView() string {
	return strings.Join([]string{titleStyle.Render("HELP"), "", "j / ↓       select next task", "k / ↑       select previous task", "enter       open task detail", "n           create a task", "r           reload tasks", "?           open or close help", "q / esc     return to the list", "Q / ctrl+c  quit", "", mutedStyle.Render("Press q or ? to close help.")}, "\n")
}

func (m Model) formView() string {
	labels := []string{"Title", "Description", "Priority"}
	lines := []string{titleStyle.Render("NEW TASK"), ""}
	for i, input := range m.form.inputs {
		label := labels[i]
		if i == m.form.focus {
			label = selectedStyle.Render(label)
		}
		lines = append(lines, label, input.View(), "")
	}
	if m.form.err != nil {
		lines = append(lines, errorStyle.Render(m.form.err.Error()), "")
	}
	if m.form.saving {
		lines = append(lines, "Saving…", "")
	}
	lines = append(lines, mutedStyle.Render("tab/↑/↓ field · enter next/save · esc cancel · Q quit"))
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
