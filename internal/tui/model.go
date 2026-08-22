package tui

import (
	"context"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/alle80/griglia-tui/internal/app"
	"github.com/alle80/griglia-tui/internal/domain"
)

type Service interface {
	ListTasks(context.Context) ([]domain.Task, error)
	AddTask(context.Context, app.AddTaskInput) (domain.Task, error)
}

type route uint8

const (
	routeList route = iota
	routeDetail
	routeForm
	routeHelp
)

type tasksLoadedMsg struct {
	tasks []domain.Task
	err   error
}

type taskCreatedMsg struct {
	task domain.Task
	err  error
}

type Model struct {
	ctx        context.Context
	service    Service
	route      route
	previous   route
	tasks      []domain.Task
	selected   int
	selectedID int64
	loading    bool
	err        error
	status     string
	width      int
	height     int
	form       formModel
}

type formModel struct {
	inputs [3]textinput.Model
	focus  int
	err    error
	saving bool
}

func New(ctx context.Context, service Service) Model {
	if ctx == nil {
		ctx = context.Background()
	}
	model := Model{ctx: ctx, service: service, route: routeList, loading: true, width: 80, height: 24, form: newForm()}
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

func (m Model) Init() tea.Cmd { return m.loadTasks() }

func (m Model) loadTasks() tea.Cmd {
	return func() tea.Msg {
		tasks, err := m.service.ListTasks(m.ctx)
		return tasksLoadedMsg{tasks: tasks, err: err}
	}
}

func (m Model) createTask(input app.AddTaskInput) tea.Cmd {
	return func() tea.Msg {
		task, err := m.service.AddTask(m.ctx, input)
		return taskCreatedMsg{task: task, err: err}
	}
}

func Run(ctx context.Context, service Service) error {
	_, err := tea.NewProgram(New(ctx, service)).Run()
	return err
}
