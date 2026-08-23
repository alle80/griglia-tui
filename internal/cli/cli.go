package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alle80/griglia-tui/internal/app"
	"github.com/alle80/griglia-tui/internal/domain"
	grsqlite "github.com/alle80/griglia-tui/internal/sqlite"
	"github.com/alle80/griglia-tui/internal/tui"
	"github.com/spf13/cobra"
)

const protocolVersion = "1"

type Options struct {
	Version    string
	WorkingDir string
}
type state struct {
	out, errOut io.Writer
	opts        Options
	project     string
	json        bool
}

type commandError struct {
	code          int
	kind, message string
}

func (e *commandError) Error() string { return e.message }

func Run(args []string, stdout, stderr io.Writer, opts Options) int {
	s := &state{out: stdout, errOut: stderr, opts: opts}
	root := s.root()
	root.SetArgs(args)
	// Find does not parse flags. Capture the presentation mode before command
	// resolution so an unknown command still receives the requested envelope.
	for _, arg := range args {
		if arg == "--json" || arg == "--json=true" {
			s.json = true
		} else if arg == "--json=false" {
			s.json = false
		}
	}
	if _, _, err := root.Find(args); err != nil {
		ce := &commandError{2, "invalid_input", err.Error()}
		s.writeError(ce)
		return ce.code
	}
	if err := root.Execute(); err != nil {
		ce := mapError(err)
		s.writeError(ce)
		return ce.code
	}
	return 0
}

func (s *state) writeError(ce *commandError) {
	if s.json {
		_ = writeJSON(s.out, nil, &errorDTO{Code: ce.kind, Message: ce.message})
	} else {
		fmt.Fprintln(s.errOut, "Error:", ce.message)
	}
}

func exactArgs(count int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != count {
			return &commandError{2, "invalid_input", fmt.Sprintf("%s accepts %d arg(s), received %d", cmd.CommandPath(), count, len(args))}
		}
		return nil
	}
}

func noArgs(cmd *cobra.Command, args []string) error { return exactArgs(0)(cmd, args) }

func (s *state) root() *cobra.Command {
	root := &cobra.Command{Use: "griglia", Short: "A local, transactional todo list", Args: noArgs, SilenceErrors: true, SilenceUsage: true, RunE: func(cmd *cobra.Command, _ []string) error {
		if s.json {
			return &commandError{2, "invalid_input", "--json requires a non-interactive command"}
		}
		service, closeFn, err := s.service()
		if err != nil {
			return err
		}
		defer closeFn()
		return tui.Run(cmd.Context(), service)
	}}
	root.SetOut(s.out)
	root.SetErr(s.errOut)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &commandError{2, "invalid_input", err.Error()}
	})
	root.PersistentFlags().StringVar(&s.project, "project", "", "project root or database path")
	root.PersistentFlags().BoolVar(&s.json, "json", false, "emit the JSON protocol")
	root.AddCommand(s.initCommand(), s.versionCommand(), s.taskCommand())
	return root
}

func (s *state) versionCommand() *cobra.Command {
	return &cobra.Command{Use: "version", Short: "Print the version", Args: noArgs, RunE: func(*cobra.Command, []string) error {
		if s.json {
			return writeJSON(s.out, map[string]any{"version": s.opts.Version}, nil)
		}
		_, err := fmt.Fprintln(s.out, "griglia", s.opts.Version)
		return err
	}}
}

func (s *state) initCommand() *cobra.Command {
	var name string
	cmd := &cobra.Command{Use: "init", Short: "Initialize a Griglia project", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		root := s.project
		if root == "" {
			root = os.Getenv("GRIGLIA_PROJECT")
		}
		if root == "" {
			root = s.cwd()
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			return err
		}
		if filepath.Base(abs) == "griglia.db" {
			return &commandError{2, "invalid_input", "--project must name a directory when used with init"}
		}
		dir := filepath.Join(abs, ".griglia")
		dbPath := filepath.Join(dir, "griglia.db")
		if _, err = os.Stat(dbPath); err == nil {
			return &commandError{5, "conflict", "project is already initialized"}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err = os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("griglia.db*\n"), 0o644); err != nil {
			return err
		}
		store, err := grsqlite.Open(dbPath)
		if err != nil {
			return err
		}
		defer store.Close()
		if name == "" {
			name = filepath.Base(abs)
		}
		id, err := domain.NewUUID()
		if err != nil {
			return fmt.Errorf("generate project UUID: %w", err)
		}
		if err = store.CreateProject(cmd.Context(), domain.Project{ID: id, Name: name, CreatedAt: time.Now().UTC()}); err != nil {
			return err
		}
		if s.json {
			return writeJSON(s.out, map[string]any{"project": map[string]any{"name": name, "database": dbPath}}, nil)
		}
		_, err = fmt.Fprintf(s.out, "Initialized Griglia project %q in %s\n", name, dir)
		return err
	}}
	cmd.Flags().StringVar(&name, "name", "", "project display name")
	return cmd
}

func (s *state) taskCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "task", Short: "Manage tasks"}
	cmd.AddCommand(s.addCommand(), s.listCommand(), s.showCommand(), s.editCommand(), s.readyCommand(), s.doneCommand(), s.cancelCommand(), s.claimCommand(), s.claimNextCommand(), s.releaseCommand(), s.progressCommand())
	return cmd
}

func taskID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 1 {
		return 0, &commandError{2, "invalid_input", "task ID must be a positive integer"}
	}
	return id, nil
}

func (s *state) editCommand() *cobra.Command {
	var title, description, priority string
	cmd := &cobra.Command{Use: "edit ID", Short: "Edit a task", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := taskID(args[0])
		if err != nil {
			return err
		}
		var in app.EditTaskInput
		if cmd.Flags().Changed("title") {
			in.Title = &title
		}
		if cmd.Flags().Changed("description") {
			in.Description = &description
		}
		if cmd.Flags().Changed("priority") {
			p, parseErr := domain.ParsePriority(priority)
			if parseErr != nil {
				return &commandError{2, "invalid_input", "priority must be low, normal, high, or urgent"}
			}
			in.Priority = &p
		}
		service, closeFn, err := s.service()
		if err != nil {
			return err
		}
		defer closeFn()
		t, err := service.EditTask(cmd.Context(), id, in)
		if err != nil {
			return err
		}
		return s.writeTaskMutation("Edited", t)
	}}
	cmd.Flags().StringVar(&title, "title", "", "task title")
	cmd.Flags().StringVar(&description, "description", "", "task description")
	cmd.Flags().StringVar(&priority, "priority", "", "task priority")
	return cmd
}

func (s *state) readyCommand() *cobra.Command {
	return s.transitionCommand("ready", "Mark a task ready", "Ready", func(ctx context.Context, service *app.Service, id int64) (domain.Task, error) {
		return service.MarkReady(ctx, id)
	})
}
func (s *state) doneCommand() *cobra.Command {
	var agent, instance, comment string
	cmd := &cobra.Command{Use: "done ID", Short: "Complete a task", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := taskID(args[0])
		if err != nil {
			return err
		}
		service, closeFn, err := s.service()
		if err != nil {
			return err
		}
		defer closeFn()
		if agent != "" || instance != "" || comment != "" {
			view, completeErr := service.CompleteClaimedTask(cmd.Context(), id, comment, domain.AgentIdentity{AgentName: agent, InstanceID: instance})
			if completeErr != nil {
				return completeErr
			}
			return s.writeTaskViewMutation("Completed", view)
		}
		t, completeErr := service.CompleteTask(cmd.Context(), id)
		if completeErr != nil {
			return completeErr
		}
		return s.writeTaskMutation("Completed", t)
	}}
	cmd.Flags().StringVar(&agent, "agent", "", "agent name")
	cmd.Flags().StringVar(&instance, "instance", "", "agent instance ID")
	cmd.Flags().StringVar(&comment, "comment", "", "completion summary")
	return cmd
}

func identityFlags(cmd *cobra.Command, agent, instance *string) {
	cmd.Flags().StringVar(agent, "agent", "", "agent name")
	cmd.Flags().StringVar(instance, "instance", "", "agent instance ID")
}

func (s *state) claimCommand() *cobra.Command {
	var agent, instance string
	cmd := &cobra.Command{Use: "claim ID", Short: "Claim a ready task", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := taskID(args[0])
		if err != nil {
			return err
		}
		service, closeFn, err := s.service()
		if err != nil {
			return err
		}
		defer closeFn()
		view, err := service.ClaimTask(cmd.Context(), id, domain.AgentIdentity{AgentName: agent, InstanceID: instance})
		if err != nil {
			return err
		}
		return s.writeTaskViewMutation("Claimed", view)
	}}
	identityFlags(cmd, &agent, &instance)
	return cmd
}

func (s *state) claimNextCommand() *cobra.Command {
	var agent, instance string
	cmd := &cobra.Command{Use: "claim-next", Short: "Claim the next eligible task", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		service, closeFn, err := s.service()
		if err != nil {
			return err
		}
		defer closeFn()
		view, err := service.ClaimNext(cmd.Context(), domain.AgentIdentity{AgentName: agent, InstanceID: instance})
		if err != nil {
			return err
		}
		return s.writeTaskViewMutation("Claimed", view)
	}}
	identityFlags(cmd, &agent, &instance)
	return cmd
}

func (s *state) releaseCommand() *cobra.Command {
	var agent, instance, reason string
	cmd := &cobra.Command{Use: "release ID", Short: "Release an owned task", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := taskID(args[0])
		if err != nil {
			return err
		}
		service, closeFn, err := s.service()
		if err != nil {
			return err
		}
		defer closeFn()
		view, err := service.ReleaseClaim(cmd.Context(), id, domain.AgentIdentity{AgentName: agent, InstanceID: instance}, reason)
		if err != nil {
			return err
		}
		return s.writeTaskViewMutation("Released", view)
	}}
	identityFlags(cmd, &agent, &instance)
	cmd.Flags().StringVar(&reason, "reason", "", "release reason")
	return cmd
}

func (s *state) progressCommand() *cobra.Command {
	var agent, instance, message string
	cmd := &cobra.Command{Use: "progress ID PERCENT", Short: "Update progress on an owned task", Args: exactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := taskID(args[0])
		if err != nil {
			return err
		}
		percent, err := strconv.Atoi(args[1])
		if err != nil {
			return &commandError{2, "invalid_input", "progress must be an integer between 0 and 100"}
		}
		service, closeFn, err := s.service()
		if err != nil {
			return err
		}
		defer closeFn()
		view, err := service.UpdateProgress(cmd.Context(), id, percent, message, domain.AgentIdentity{AgentName: agent, InstanceID: instance})
		if err != nil {
			return err
		}
		return s.writeTaskViewMutation("Updated", view)
	}}
	identityFlags(cmd, &agent, &instance)
	cmd.Flags().StringVar(&message, "message", "", "current phase or status")
	return cmd
}

func (s *state) cancelCommand() *cobra.Command {
	var reason string
	cmd := &cobra.Command{Use: "cancel ID", Short: "Cancel a task", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := taskID(args[0])
		if err != nil {
			return err
		}
		service, closeFn, err := s.service()
		if err != nil {
			return err
		}
		defer closeFn()
		t, err := service.CancelTask(cmd.Context(), id, reason)
		if err != nil {
			return err
		}
		return s.writeTaskMutation("Cancelled", t)
	}}
	cmd.Flags().StringVar(&reason, "reason", "", "cancellation reason")
	return cmd
}

func (s *state) transitionCommand(use, short, verb string, run func(context.Context, *app.Service, int64) (domain.Task, error)) *cobra.Command {
	return &cobra.Command{Use: use + " ID", Short: short, Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := taskID(args[0])
		if err != nil {
			return err
		}
		service, closeFn, err := s.service()
		if err != nil {
			return err
		}
		defer closeFn()
		t, err := run(cmd.Context(), service, id)
		if err != nil {
			return err
		}
		return s.writeTaskMutation(verb, t)
	}}
}

func (s *state) writeTaskMutation(verb string, t domain.Task) error {
	if s.json {
		return writeJSON(s.out, map[string]any{"task": toTaskDTO(domain.NewTaskView(t, nil))}, nil)
	}
	_, err := fmt.Fprintf(s.out, "%s task #%d: %s\n", verb, t.ID, t.Title)
	return err
}

func (s *state) writeTaskViewMutation(verb string, view domain.TaskView) error {
	if s.json {
		dto := toTaskDTO(view)
		return writeJSON(s.out, map[string]any{"task": dto, "claim": dto.ActiveClaim}, nil)
	}
	_, err := fmt.Fprintf(s.out, "%s task #%d: %s\n", verb, view.ID, view.Title)
	return err
}

func (s *state) addCommand() *cobra.Command {
	var description, priority, lifecycle string
	cmd := &cobra.Command{Use: "add TITLE", Short: "Add a task", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		p, err := domain.ParsePriority(priority)
		if err != nil {
			return &commandError{2, "invalid_input", "priority must be low, normal, high, or urgent"}
		}
		l, err := domain.ParseLifecycle(lifecycle)
		if err != nil {
			return &commandError{2, "invalid_input", "lifecycle must be backlog, ready, done, or cancelled"}
		}
		service, closeFn, err := s.service()
		if err != nil {
			return err
		}
		defer closeFn()
		t, err := service.AddTask(cmd.Context(), app.AddTaskInput{Title: args[0], Description: description, Priority: p, Lifecycle: l})
		if err != nil {
			return err
		}
		if s.json {
			return writeJSON(s.out, map[string]any{"task": toTaskDTO(domain.NewTaskView(t, nil))}, nil)
		}
		_, err = fmt.Fprintf(s.out, "Added task #%d: %s\n", t.ID, t.Title)
		return err
	}}
	cmd.Flags().StringVar(&description, "description", "", "task description")
	cmd.Flags().StringVar(&priority, "priority", string(domain.PriorityNormal), "task priority")
	cmd.Flags().StringVar(&lifecycle, "lifecycle", string(domain.LifecycleBacklog), "task lifecycle")
	return cmd
}

func (s *state) listCommand() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List tasks", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		service, closeFn, err := s.service()
		if err != nil {
			return err
		}
		defer closeFn()
		tasks, err := service.ListTasks(cmd.Context())
		if err != nil {
			return err
		}
		if s.json {
			dto := make([]taskDTO, 0, len(tasks))
			for _, t := range tasks {
				dto = append(dto, toTaskDTO(t))
			}
			return writeJSON(s.out, map[string]any{"tasks": dto}, nil)
		}
		if len(tasks) == 0 {
			_, err = fmt.Fprintln(s.out, "No tasks.")
			return err
		}
		for _, t := range tasks {
			if _, err = fmt.Fprintf(s.out, "#%-4d %-9s %-7s %s\n", t.ID, t.Lifecycle, t.Priority, t.Title); err != nil {
				return err
			}
		}
		return nil
	}}
}

func (s *state) showCommand() *cobra.Command {
	return &cobra.Command{Use: "show ID", Short: "Show a task", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil || id < 1 {
			return &commandError{2, "invalid_input", "task ID must be a positive integer"}
		}
		service, closeFn, err := s.service()
		if err != nil {
			return err
		}
		defer closeFn()
		t, err := service.GetTask(cmd.Context(), id)
		if err != nil {
			return err
		}
		if s.json {
			return writeJSON(s.out, map[string]any{"task": toTaskDTO(t)}, nil)
		}
		_, err = fmt.Fprintf(s.out, "Task #%d\nTitle: %s\nLifecycle: %s\nPriority: %s\nProgress: %d%%\nDescription: %s\n", t.ID, t.Title, t.Lifecycle, t.Priority, t.Progress, emptyDash(t.Description))
		return err
	}}
}

func (s *state) service() (*app.Service, func(), error) {
	path, err := discoverProject(s.cwd(), s.project)
	if err != nil {
		return nil, func() {}, err
	}
	store, err := grsqlite.Open(path)
	if err != nil {
		return nil, func() {}, err
	}
	return app.New(store), func() { _ = store.Close() }, nil
}
func (s *state) cwd() string {
	if s.opts.WorkingDir != "" {
		return s.opts.WorkingDir
	}
	v, err := os.Getwd()
	if err != nil {
		return "."
	}
	return v
}

type envelope struct {
	ProtocolVersion string    `json:"protocol_version"`
	Ok              bool      `json:"ok"`
	Data            any       `json:"data"`
	Error           *errorDTO `json:"error"`
}
type errorDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type taskDTO struct {
	ID                int64            `json:"id"`
	UID               string           `json:"uid"`
	Title             string           `json:"title"`
	Description       string           `json:"description"`
	Lifecycle         domain.Lifecycle `json:"lifecycle"`
	OperationalState  *string          `json:"operational_state"`
	Priority          domain.Priority  `json:"priority"`
	Progress          int              `json:"progress"`
	Phase             string           `json:"phase"`
	CompletionSummary string           `json:"completion_summary"`
	CreatedAt         string           `json:"created_at"`
	UpdatedAt         string           `json:"updated_at"`
	CompletedAt       *string          `json:"completed_at"`
	CancelledAt       *string          `json:"cancelled_at"`
	Version           int64            `json:"version"`
	ActiveClaim       *claimDTO        `json:"active_claim"`
}

type claimDTO struct {
	AgentName  string `json:"agent_name"`
	InstanceID string `json:"instance_id"`
	ClaimedAt  string `json:"claimed_at"`
}

func toTaskDTO(view domain.TaskView) taskDTO {
	t := view.Task
	d := taskDTO{ID: t.ID, UID: t.UID, Title: t.Title, Description: t.Description, Lifecycle: t.Lifecycle, Priority: t.Priority, Progress: t.Progress, Phase: t.Phase, CompletionSummary: t.CompletionSummary, CreatedAt: formatJSONTime(t.CreatedAt), UpdatedAt: formatJSONTime(t.UpdatedAt), Version: t.Version}
	if view.OperationalState != nil {
		value := string(*view.OperationalState)
		d.OperationalState = &value
	}
	if view.ActiveClaim != nil {
		d.ActiveClaim = &claimDTO{AgentName: view.ActiveClaim.AgentName, InstanceID: view.ActiveClaim.InstanceID, ClaimedAt: formatJSONTime(view.ActiveClaim.ClaimedAt)}
	}
	if t.CompletedAt != nil {
		v := formatJSONTime(*t.CompletedAt)
		d.CompletedAt = &v
	}
	if t.CancelledAt != nil {
		v := formatJSONTime(*t.CancelledAt)
		d.CancelledAt = &v
	}
	return d
}
func writeJSON(w io.Writer, data any, e *errorDTO) error {
	return json.NewEncoder(w).Encode(envelope{ProtocolVersion: protocolVersion, Ok: e == nil, Data: data, Error: e})
}
func formatJSONTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000Z") }
func emptyDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "—"
	}
	return v
}

func mapError(err error) *commandError {
	var ce *commandError
	if errors.As(err, &ce) {
		return ce
	}
	if errors.Is(err, errProjectNotFound) {
		return &commandError{3, "project_not_initialized", "no .griglia/griglia.db found"}
	}
	if errors.Is(err, domain.ErrNotFound) {
		return &commandError{4, "not_found", "task not found"}
	}
	if errors.Is(err, domain.ErrInvalid) {
		return &commandError{2, "invalid_input", err.Error()}
	}
	if errors.Is(err, domain.ErrConflict) {
		return &commandError{5, "conflict", err.Error()}
	}
	if errors.Is(err, domain.ErrNoEligible) {
		return &commandError{4, "no_eligible_task", "no eligible task"}
	}
	return &commandError{1, "internal_error", "internal error"}
}
