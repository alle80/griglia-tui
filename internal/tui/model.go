package tui

import (
	"context"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/alle80/griglia-tui/internal/app"
	"github.com/alle80/griglia-tui/internal/domain"
)

// refreshInterval is the cadence of the automatic background refresh that
// keeps the TUI in sync with changes made by external agent processes.
const refreshInterval = time.Second

type Service interface {
	ListTasks(context.Context) ([]domain.TaskView, error)
	AddTask(context.Context, app.AddTaskInput) (domain.Task, error)
	EditTask(context.Context, int64, app.EditTaskInput) (domain.Task, error)
	MarkReady(context.Context, int64) (domain.Task, error)
	CompleteTask(context.Context, int64) (domain.Task, error)
	CancelTask(context.Context, int64, string) (domain.Task, error)
	ListQuestions(context.Context, int64, domain.QuestionFilter) ([]domain.Question, error)
	AnswerQuestion(context.Context, int64, string) (domain.Question, error)
	ListDependencies(context.Context, int64) ([]domain.DependencyView, error)
	AddDependency(context.Context, int64, int64) (domain.DependencyView, error)
	RemoveDependency(context.Context, int64, int64) error
}

type route uint8

const (
	routeList route = iota
	routeDetail
	routeForm
	routeHelp
	routeQuestions
	routeDependencies
)

// tickMsg drives the auto-refresh loop. Exactly one tick is ever in flight:
// Init schedules the first and each handled tick schedules the next.
type tickMsg struct{}

// The background flag marks results produced by the auto-refresh loop. They
// update data in place without touching loading indicators, routes, or focus,
// and they are discarded whenever a foreground load of the same data is in
// flight or the refresh failed, so a transient storage error can never
// destroy the visible state.
type tasksLoadedMsg struct {
	tasks      []domain.TaskView
	background bool
	err        error
}

type taskCreatedMsg struct {
	task domain.Task
	err  error
}

type taskMutatedMsg struct {
	task   domain.Task
	action string
	err    error
}

type questionsLoadedMsg struct {
	taskID     int64
	questions  []domain.Question
	background bool
	err        error
}

type questionAnsweredMsg struct {
	question domain.Question
	err      error
}

type dependenciesLoadedMsg struct {
	taskID       int64
	dependencies []domain.DependencyView
	background   bool
	err          error
}

type dependencyMutatedMsg struct {
	taskID          int64
	dependsOnTaskID int64
	action          string
	err             error
}

type Model struct {
	ctx                context.Context
	service            Service
	route              route
	previous           route
	tasks              []domain.TaskView
	selected           int
	selectedID         int64
	loading            bool
	err                error
	status             string
	actionErr          error
	width              int
	height             int
	form               formModel
	questions          []domain.Question
	questionSelected   int
	questionSelectedID int64
	questionsTaskID    int64
	questionsFrom      route
	questionsLoading   bool
	dependencies       []domain.DependencyView
	depSelected        int
	depSelectedID      int64
	dependenciesTaskID int64
	dependenciesFrom   route
	dependenciesLoad   bool
	refreshing         bool
	tick               func() tea.Cmd
}

type formModel struct {
	inputs       [3]textinput.Model
	focus        int
	err          error
	saving       bool
	editing      bool
	cancelling   bool
	answering    bool
	depending    bool
	taskID       int64
	questionID   int64
	questionBody string
}

func newEditForm(task domain.Task) formModel {
	f := newForm()
	f.editing, f.taskID = true, task.ID
	f.inputs[0].SetValue(task.Title)
	f.inputs[1].SetValue(task.Description)
	f.inputs[2].SetValue(string(task.Priority))
	return f
}

func newCancelForm(task domain.Task) formModel {
	f := newForm()
	f.cancelling, f.taskID = true, task.ID
	f.inputs[0].Placeholder = "Optional cancellation reason"
	f.inputs[0].CharLimit = 0
	return f
}

func newDependForm(task domain.Task) formModel {
	f := newForm()
	f.depending, f.taskID = true, task.ID
	f.inputs[0].Placeholder = "Prerequisite task ID"
	f.inputs[0].CharLimit = 0
	return f
}

func newAnswerForm(question domain.Question) formModel {
	f := newForm()
	f.answering, f.questionID, f.questionBody = true, question.ID, question.Body
	f.inputs[0].Placeholder = "Answer"
	f.inputs[0].CharLimit = domain.MaxQuestionTextLength
	if question.Answer != nil {
		f.inputs[0].SetValue(*question.Answer)
	}
	return f
}

func New(ctx context.Context, service Service) Model {
	if ctx == nil {
		ctx = context.Background()
	}
	model := Model{ctx: ctx, service: service, route: routeList, loading: true, width: 80, height: 24, form: newForm()}
	// The tick command is a field so tests can substitute a deterministic
	// stub; production always schedules the next wall-clock tick.
	model.tick = func() tea.Cmd {
		return tea.Tick(refreshInterval, func(time.Time) tea.Msg { return tickMsg{} })
	}
	model.sizeForm()
	return model
}

func newForm() formModel {
	title := textinput.New()
	title.Placeholder = "Task title"
	title.CharLimit = domain.MaxTitleLength
	title.Prompt = "> "
	description := textinput.New()
	description.Placeholder = "Optional description"
	description.Prompt = "> "
	priority := textinput.New()
	priority.Placeholder = "low, normal, high, or urgent"
	priority.Prompt = "> "
	priority.SetValue(string(domain.PriorityNormal))
	f := formModel{inputs: [3]textinput.Model{title, description, priority}}
	f.inputs[0].Focus()
	return f
}

func (m Model) Init() tea.Cmd { return tea.Batch(m.loadTasks(), m.tick()) }

func (m Model) loadTasks() tea.Cmd {
	return func() tea.Msg {
		tasks, err := m.service.ListTasks(m.ctx)
		return tasksLoadedMsg{tasks: tasks, err: err}
	}
}

// autoRefresh starts one background refresh of the data behind the current
// view. It is skipped while a form is open (never disturb typing), while a
// previous background refresh is still in flight, and while a foreground
// load is already running — so refresh requests can never pile up.
func (m Model) autoRefresh() (Model, tea.Cmd) {
	if m.route == routeForm || m.refreshing || m.loading {
		return m, nil
	}
	m.refreshing = true
	commands := []tea.Cmd{m.refreshTasks()}
	switch m.route {
	case routeQuestions:
		commands = append(commands, m.refreshQuestions(m.questionsTaskID))
	case routeDetail:
		// The detail route displays the selected task, which
		// dependenciesTaskID is not guaranteed to match. Retarget it so the
		// result is not dropped as stale, and shed rows of the old target.
		if len(m.tasks) > 0 {
			id := m.tasks[m.selected].ID
			if id != m.dependenciesTaskID {
				m.dependenciesTaskID, m.dependencies = id, nil
			}
			commands = append(commands, m.refreshDependencies(id))
		}
	case routeDependencies:
		commands = append(commands, m.refreshDependencies(m.dependenciesTaskID))
	}
	return m, tea.Batch(commands...)
}

func (m Model) refreshTasks() tea.Cmd {
	return func() tea.Msg {
		tasks, err := m.service.ListTasks(m.ctx)
		return tasksLoadedMsg{tasks: tasks, background: true, err: err}
	}
}

func (m Model) refreshQuestions(taskID int64) tea.Cmd {
	return func() tea.Msg {
		questions, err := m.service.ListQuestions(m.ctx, taskID, domain.QuestionsAll)
		return questionsLoadedMsg{taskID: taskID, questions: questions, background: true, err: err}
	}
}

func (m Model) refreshDependencies(taskID int64) tea.Cmd {
	return func() tea.Msg {
		dependencies, err := m.service.ListDependencies(m.ctx, taskID)
		return dependenciesLoadedMsg{taskID: taskID, dependencies: dependencies, background: true, err: err}
	}
}

func (m Model) createTask(input app.AddTaskInput) tea.Cmd {
	return func() tea.Msg {
		task, err := m.service.AddTask(m.ctx, input)
		return taskCreatedMsg{task: task, err: err}
	}
}

func (m Model) editTask(id int64, input app.EditTaskInput) tea.Cmd {
	return func() tea.Msg {
		task, err := m.service.EditTask(m.ctx, id, input)
		return taskMutatedMsg{task: task, action: "Edited", err: err}
	}
}

func (m Model) mutateTask(action string, id int64) tea.Cmd {
	return func() tea.Msg {
		var task domain.Task
		var err error
		switch action {
		case "Ready":
			task, err = m.service.MarkReady(m.ctx, id)
		case "Completed":
			task, err = m.service.CompleteTask(m.ctx, id)
		case "Cancelled":
			task, err = m.service.CancelTask(m.ctx, id, "")
		}
		return taskMutatedMsg{task: task, action: action, err: err}
	}
}

func (m Model) cancelTask(id int64, reason string) tea.Cmd {
	return func() tea.Msg {
		task, err := m.service.CancelTask(m.ctx, id, reason)
		return taskMutatedMsg{task: task, action: "Cancelled", err: err}
	}
}

func (m Model) loadQuestions(taskID int64) tea.Cmd {
	return func() tea.Msg {
		questions, err := m.service.ListQuestions(m.ctx, taskID, domain.QuestionsAll)
		return questionsLoadedMsg{taskID: taskID, questions: questions, err: err}
	}
}

func (m Model) answerQuestion(questionID int64, answer string) tea.Cmd {
	return func() tea.Msg {
		question, err := m.service.AnswerQuestion(m.ctx, questionID, answer)
		return questionAnsweredMsg{question: question, err: err}
	}
}

func (m Model) loadDependencies(taskID int64) tea.Cmd {
	return func() tea.Msg {
		dependencies, err := m.service.ListDependencies(m.ctx, taskID)
		return dependenciesLoadedMsg{taskID: taskID, dependencies: dependencies, err: err}
	}
}

func (m Model) addDependency(taskID, dependsOnTaskID int64) tea.Cmd {
	return func() tea.Msg {
		dependency, err := m.service.AddDependency(m.ctx, taskID, dependsOnTaskID)
		return dependencyMutatedMsg{taskID: taskID, dependsOnTaskID: dependency.DependsOnTaskID, action: "Added dependency", err: err}
	}
}

func (m Model) removeDependency(taskID, dependsOnTaskID int64) tea.Cmd {
	return func() tea.Msg {
		err := m.service.RemoveDependency(m.ctx, taskID, dependsOnTaskID)
		return dependencyMutatedMsg{taskID: taskID, dependsOnTaskID: dependsOnTaskID, action: "Removed dependency", err: err}
	}
}

func Run(ctx context.Context, service Service) error {
	_, err := tea.NewProgram(New(ctx, service)).Run()
	return err
}
