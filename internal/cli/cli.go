package cli

import (
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
	if err := root.Execute(); err != nil {
		ce := mapError(err)
		if s.json {
			_ = writeJSON(stdout, nil, &errorDTO{Code: ce.kind, Message: ce.message})
		} else {
			fmt.Fprintln(stderr, "Error:", ce.message)
		}
		return ce.code
	}
	return 0
}

func (s *state) root() *cobra.Command {
	root := &cobra.Command{Use: "griglia", Short: "A local, transactional todo list", SilenceErrors: true, SilenceUsage: true}
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
	return &cobra.Command{Use: "version", Short: "Print the version", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		if s.json {
			return writeJSON(s.out, map[string]any{"version": s.opts.Version}, nil)
		}
		_, err := fmt.Fprintln(s.out, "griglia", s.opts.Version)
		return err
	}}
}

func (s *state) initCommand() *cobra.Command {
	var name string
	cmd := &cobra.Command{Use: "init", Short: "Initialize a Griglia project", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
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
		if _, err = store.DB().Exec(`INSERT INTO projects(id,name,created_at) VALUES(?,?,?)`, id, name, time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")); err != nil {
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
	cmd.AddCommand(s.addCommand(), s.listCommand(), s.showCommand())
	return cmd
}

func (s *state) addCommand() *cobra.Command {
	var description, priority, lifecycle string
	cmd := &cobra.Command{Use: "add TITLE", Short: "Add a task", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
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
			return writeJSON(s.out, map[string]any{"task": toTaskDTO(t)}, nil)
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
	return &cobra.Command{Use: "list", Short: "List tasks", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
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
	return &cobra.Command{Use: "show ID", Short: "Show a task", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
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
}

func toTaskDTO(t domain.Task) taskDTO {
	d := taskDTO{ID: t.ID, UID: t.UID, Title: t.Title, Description: t.Description, Lifecycle: t.Lifecycle, Priority: t.Priority, Progress: t.Progress, Phase: t.Phase, CompletionSummary: t.CompletionSummary, CreatedAt: formatJSONTime(t.CreatedAt), UpdatedAt: formatJSONTime(t.UpdatedAt), Version: t.Version}
	if t.Lifecycle == domain.LifecycleReady {
		v := "available"
		d.OperationalState = &v
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
	// Cobra reports command/argument usage errors as ordinary errors. They are
	// part of the public CLI contract and must not become internal failures.
	for _, marker := range []string{"unknown command", "requires ", "accepts ", "unknown shorthand flag"} {
		if strings.Contains(err.Error(), marker) {
			return &commandError{2, "invalid_input", err.Error()}
		}
	}
	return &commandError{1, "internal_error", "internal error"}
}
