package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/alle80/griglia-tui/internal/app"
	"github.com/alle80/griglia-tui/internal/domain"
)

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.sizeForm()
		return m, nil
	case tasksLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.tasks = msg.tasks
		m.restoreSelection()
		return m, nil
	case taskMutatedMsg:
		m.form.saving = false
		if msg.err != nil {
			if m.route == routeForm {
				m.form.err = msg.err
			} else {
				m.actionErr = msg.err
			}
			return m, nil
		}
		m.selectedID, m.status, m.actionErr = msg.task.ID, fmt.Sprintf("%s task #%d", msg.action, msg.task.ID), nil
		m.route, m.form, m.loading = routeList, newForm(), true
		return m, m.loadTasks()
	case taskCreatedMsg:
		m.form.saving = false
		if msg.err != nil {
			m.form.err = msg.err
			return m, nil
		}
		m.selectedID = msg.task.ID
		m.status = fmt.Sprintf("Created task #%d", msg.task.ID)
		m.route = routeList
		m.form = newForm()
		m.loading = true
		return m, m.loadTasks()
	case questionsLoadedMsg:
		if msg.taskID != m.questionsTaskID {
			return m, nil
		}
		m.questionsLoading = false
		if msg.err != nil {
			m.actionErr = msg.err
			return m, nil
		}
		m.actionErr = nil
		m.questions = msg.questions
		m.restoreQuestionSelection()
		return m, nil
	case questionAnsweredMsg:
		m.form.saving = false
		if msg.err != nil {
			if m.route == routeForm {
				m.form.err = msg.err
			} else {
				m.actionErr = msg.err
			}
			return m, nil
		}
		m.questionSelectedID, m.status, m.actionErr = msg.question.ID, fmt.Sprintf("Answered question #%d", msg.question.ID), nil
		m.route, m.form = routeQuestions, newForm()
		m.questionsLoading, m.loading = true, true
		return m, tea.Batch(m.loadQuestions(m.questionsTaskID), m.loadTasks())
	case dependenciesLoadedMsg:
		if msg.taskID != m.dependenciesTaskID {
			return m, nil
		}
		m.dependenciesLoad = false
		if msg.err != nil {
			m.actionErr = msg.err
			return m, nil
		}
		m.actionErr = nil
		m.dependencies = msg.dependencies
		m.restoreDependencySelection()
		return m, nil
	case dependencyMutatedMsg:
		m.form.saving = false
		if msg.err != nil {
			if m.route == routeForm {
				m.form.err = msg.err
			} else {
				m.actionErr = msg.err
			}
			return m, nil
		}
		m.depSelectedID, m.status, m.actionErr = msg.dependsOnTaskID, fmt.Sprintf("%s #%d for task #%d", msg.action, msg.dependsOnTaskID, msg.taskID), nil
		m.route, m.form = routeDependencies, newForm()
		m.dependenciesLoad, m.loading = true, true
		return m, tea.Batch(m.loadDependencies(m.dependenciesTaskID), m.loadTasks())
	case tea.KeyPressMsg:
		if matches(msg, keys.ForceQuit) || (m.route != routeForm && matches(msg, keys.Quit)) {
			return m, tea.Quit
		}
		if m.route != routeForm && matches(msg, keys.Help) {
			if m.route == routeHelp {
				m.route = m.previous
			} else {
				m.previous, m.route = m.route, routeHelp
			}
			return m, nil
		}
		return m.updateRoute(msg)
	}
	return m, nil
}

func (m Model) updateRoute(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.route {
	case routeList:
		return m.updateList(msg)
	case routeDetail:
		if matches(msg, keys.Back) {
			m.route = routeList
			return m, nil
		}
		return m.updateTaskAction(msg)
	case routeHelp:
		if matches(msg, keys.Back) {
			if m.route == routeHelp {
				m.route = m.previous
			} else {
				m.route = routeList
			}
		}
		return m, nil
	case routeForm:
		return m.updateForm(msg)
	case routeQuestions:
		return m.updateQuestions(msg)
	case routeDependencies:
		return m.updateDependencies(msg)
	default:
		return m, nil
	}
}

func (m Model) updateDependencies(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case matches(msg, keys.Back):
		m.route, m.actionErr = m.dependenciesFrom, nil
		return m, nil
	case matches(msg, keys.Down) && len(m.dependencies) > 0:
		if m.depSelected < len(m.dependencies)-1 {
			m.depSelected++
		}
		m.depSelectedID = m.dependencies[m.depSelected].DependsOnTaskID
	case matches(msg, keys.Up) && len(m.dependencies) > 0:
		if m.depSelected > 0 {
			m.depSelected--
		}
		m.depSelectedID = m.dependencies[m.depSelected].DependsOnTaskID
	case matches(msg, keys.New):
		m.form = newDependForm(domain.Task{ID: m.dependenciesTaskID})
		m.sizeForm()
		m.route = routeForm
	case matches(msg, keys.Cancel) && len(m.dependencies) > 0:
		return m, m.removeDependency(m.dependenciesTaskID, m.dependencies[m.depSelected].DependsOnTaskID)
	case matches(msg, keys.Refresh):
		m.dependenciesLoad, m.loading, m.actionErr = true, true, nil
		return m, tea.Batch(m.loadDependencies(m.dependenciesTaskID), m.loadTasks())
	}
	return m, nil
}

func (m Model) updateQuestions(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case matches(msg, keys.Back):
		m.route, m.actionErr = m.questionsFrom, nil
		return m, nil
	case matches(msg, keys.Down) && len(m.questions) > 0:
		if m.questionSelected < len(m.questions)-1 {
			m.questionSelected++
		}
		m.questionSelectedID = m.questions[m.questionSelected].ID
	case matches(msg, keys.Up) && len(m.questions) > 0:
		if m.questionSelected > 0 {
			m.questionSelected--
		}
		m.questionSelectedID = m.questions[m.questionSelected].ID
	case matches(msg, keys.Open) && len(m.questions) > 0:
		question := m.questions[m.questionSelected]
		if question.Acknowledged() {
			return m, nil
		}
		m.form = newAnswerForm(question)
		m.sizeForm()
		m.route = routeForm
	case matches(msg, keys.Refresh):
		m.questionsLoading, m.loading, m.actionErr = true, true, nil
		return m, tea.Batch(m.loadQuestions(m.questionsTaskID), m.loadTasks())
	}
	return m, nil
}

func (m Model) updateList(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case matches(msg, keys.Down) && len(m.tasks) > 0:
		if m.selected < len(m.tasks)-1 {
			m.selected++
		}
		m.selectedID = m.tasks[m.selected].ID
	case matches(msg, keys.Up) && len(m.tasks) > 0:
		if m.selected > 0 {
			m.selected--
		}
		m.selectedID = m.tasks[m.selected].ID
	case matches(msg, keys.Open) && len(m.tasks) > 0:
		m.route = routeDetail
		m.dependenciesTaskID, m.dependencies, m.dependenciesLoad = m.tasks[m.selected].ID, nil, true
		return m, m.loadDependencies(m.dependenciesTaskID)
	case matches(msg, keys.New):
		m.form = newForm()
		m.sizeForm()
		m.route = routeForm
	case matches(msg, keys.Refresh):
		m.loading, m.err = true, nil
		return m, m.loadTasks()
	default:
		return m.updateTaskAction(msg)
	}
	return m, nil
}

func (m Model) updateTaskAction(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if len(m.tasks) == 0 {
		return m, nil
	}
	task := m.tasks[m.selected]
	switch {
	case matches(msg, keys.Edit):
		m.form = newEditForm(task.Task)
		m.sizeForm()
		m.route = routeForm
	case matches(msg, keys.Ready):
		return m, m.mutateTask("Ready", task.ID)
	case matches(msg, keys.Done):
		return m, m.mutateTask("Completed", task.ID)
	case matches(msg, keys.Cancel):
		m.form = newCancelForm(task.Task)
		m.sizeForm()
		m.route = routeForm
	case matches(msg, keys.Questions):
		m.questionsFrom, m.route = m.route, routeQuestions
		m.questionsTaskID, m.questions = task.ID, nil
		m.questionSelected, m.questionSelectedID = 0, 0
		m.questionsLoading, m.actionErr = true, nil
		return m, m.loadQuestions(task.ID)
	case matches(msg, keys.Depends):
		m.dependenciesFrom, m.route = m.route, routeDependencies
		m.dependenciesTaskID, m.dependencies = task.ID, nil
		m.depSelected, m.depSelectedID = 0, 0
		m.dependenciesLoad, m.actionErr = true, nil
		return m, m.loadDependencies(task.ID)
	}
	return m, nil
}

func (m Model) updateForm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "esc" {
		switch {
		case m.form.answering:
			m.route = routeQuestions
		case m.form.depending:
			m.route = routeDependencies
		default:
			m.route = routeList
		}
		m.form = newForm()
		return m, nil
	}
	if m.form.saving {
		return m, nil
	}
	if m.form.depending {
		if msg.String() == "enter" {
			dependsOn, err := strconv.ParseInt(strings.TrimSpace(m.form.inputs[0].Value()), 10, 64)
			if err != nil || dependsOn < 1 {
				m.form.err = fmt.Errorf("prerequisite must be a positive task ID")
				return m, nil
			}
			m.form.err, m.form.saving = nil, true
			return m, m.addDependency(m.form.taskID, dependsOn)
		}
		var cmd tea.Cmd
		m.form.inputs[0], cmd = m.form.inputs[0].Update(msg)
		if m.form.err != nil {
			m.form.err = nil
		}
		return m, cmd
	}
	if m.form.answering {
		if msg.String() == "enter" {
			if strings.TrimSpace(m.form.inputs[0].Value()) == "" {
				m.form.err = fmt.Errorf("answer must not be empty")
				return m, nil
			}
			m.form.err, m.form.saving = nil, true
			return m, m.answerQuestion(m.form.questionID, m.form.inputs[0].Value())
		}
		var cmd tea.Cmd
		m.form.inputs[0], cmd = m.form.inputs[0].Update(msg)
		if m.form.err != nil {
			m.form.err = nil
		}
		return m, cmd
	}
	if m.form.cancelling {
		if msg.String() == "enter" {
			m.form.err, m.form.saving = nil, true
			return m, m.cancelTask(m.form.taskID, m.form.inputs[0].Value())
		}
		var cmd tea.Cmd
		m.form.inputs[0], cmd = m.form.inputs[0].Update(msg)
		if m.form.err != nil {
			m.form.err = nil
		}
		return m, cmd
	}
	switch msg.String() {
	case "tab", "down":
		m.form.moveFocus(1)
		return m, nil
	case "shift+tab", "up":
		m.form.moveFocus(-1)
		return m, nil
	case "enter":
		if m.form.focus < len(m.form.inputs)-1 {
			m.form.moveFocus(1)
			return m, nil
		}
		priority, err := domain.ParsePriority(m.form.inputs[2].Value())
		if err != nil {
			m.form.err = fmt.Errorf("priority must be low, normal, high, or urgent")
			return m, nil
		}
		m.form.err = nil
		m.form.saving = true
		if m.form.editing {
			title, description := m.form.inputs[0].Value(), m.form.inputs[1].Value()
			return m, m.editTask(m.form.taskID, app.EditTaskInput{Title: &title, Description: &description, Priority: &priority})
		}
		return m, m.createTask(app.AddTaskInput{Title: m.form.inputs[0].Value(), Description: m.form.inputs[1].Value(), Priority: priority, Lifecycle: domain.LifecycleBacklog})
	}
	var cmd tea.Cmd
	m.form.inputs[m.form.focus], cmd = m.form.inputs[m.form.focus].Update(msg)
	if m.form.err != nil {
		m.form.err = nil
	}
	return m, cmd
}

func (f *formModel) moveFocus(delta int) {
	f.inputs[f.focus].Blur()
	f.focus = (f.focus + delta + len(f.inputs)) % len(f.inputs)
	f.inputs[f.focus].Focus()
}

func (m *Model) restoreQuestionSelection() {
	if len(m.questions) == 0 {
		m.questionSelected, m.questionSelectedID = 0, 0
		return
	}
	for i, question := range m.questions {
		if question.ID == m.questionSelectedID {
			m.questionSelected = i
			return
		}
	}
	if m.questionSelected >= len(m.questions) {
		m.questionSelected = len(m.questions) - 1
	}
	m.questionSelectedID = m.questions[m.questionSelected].ID
}

func (m *Model) restoreDependencySelection() {
	if len(m.dependencies) == 0 {
		m.depSelected, m.depSelectedID = 0, 0
		return
	}
	for i, dependency := range m.dependencies {
		if dependency.DependsOnTaskID == m.depSelectedID {
			m.depSelected = i
			return
		}
	}
	if m.depSelected >= len(m.dependencies) {
		m.depSelected = len(m.dependencies) - 1
	}
	m.depSelectedID = m.dependencies[m.depSelected].DependsOnTaskID
}

func (m *Model) restoreSelection() {
	if len(m.tasks) == 0 {
		m.selected, m.selectedID = 0, 0
		return
	}
	for i, task := range m.tasks {
		if task.ID == m.selectedID {
			m.selected = i
			return
		}
	}
	if m.selected >= len(m.tasks) {
		m.selected = len(m.tasks) - 1
	}
	m.selectedID = m.tasks[m.selected].ID
}

func (m *Model) sizeForm() {
	width := m.width - 8
	if width < 16 {
		width = 16
	}
	if width > 72 {
		width = 72
	}
	for i := range m.form.inputs {
		m.form.inputs[i].SetWidth(width)
	}
}
