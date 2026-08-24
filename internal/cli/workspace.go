package cli

// The workspace command group (docs/WORKSPACES.md §10): create, show, list,
// and remove per-task Git worktrees. Reads serve the derived read model —
// persisted resource state plus usage/active_claim derived from the claims
// table at read time — and every payload carries the absolute launcher facts
// (path, project_root, database, git_common_dir) so external launchers never
// hardcode layout.

import (
	"fmt"
	"path/filepath"

	"github.com/alle80/griglia-tui/internal/app"
	"github.com/alle80/griglia-tui/internal/domain"
	gitrunner "github.com/alle80/griglia-tui/internal/git"
	grsqlite "github.com/alle80/griglia-tui/internal/sqlite"
	"github.com/spf13/cobra"
)

func (s *state) workspaceCommand() *cobra.Command {
	// Without RunE, cobra treats an unknown subcommand as a request for help
	// on stdout with exit 0, which would corrupt the JSON protocol stream.
	cmd := &cobra.Command{Use: "workspace", Short: "Manage per-task Git worktrees", RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return &commandError{2, "invalid_input", fmt.Sprintf("unknown command %q for %q", args[0], cmd.CommandPath())}
		}
		if s.json {
			return &commandError{2, "invalid_input", "workspace requires a subcommand"}
		}
		return cmd.Help()
	}}
	cmd.AddCommand(s.workspaceCreateCommand(), s.workspaceListCommand(), s.workspaceShowCommand(), s.workspaceRemoveCommand())
	return cmd
}

// workspaceService resolves the authoritative project (--project, then
// GRIGLIA_PROJECT, then upward discovery) and wires the workspace service
// against it. The project root is the directory containing .griglia, derived
// from the discovered database path, so agents in worktrees outside the main
// checkout operate on the main project's board.
func (s *state) workspaceService() (*app.WorkspaceService, func(), error) {
	dbPath, err := discoverProject(s.cwd(), s.project)
	if err != nil {
		return nil, func() {}, err
	}
	store, err := grsqlite.Open(dbPath)
	if err != nil {
		return nil, func() {}, err
	}
	root := filepath.Dir(dbPath)
	if filepath.Base(root) == ".griglia" {
		root = filepath.Dir(root)
	}
	return app.NewWorkspaceService(store, gitrunner.Runner{}, root, dbPath), func() { _ = store.Close() }, nil
}

func (s *state) workspaceCreateCommand() *cobra.Command {
	var agent, instance, base string
	cmd := &cobra.Command{Use: "create TASK_ID", Short: "Allocate the task's worktree and branch", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := taskID(args[0])
		if err != nil {
			return err
		}
		service, closeFn, err := s.workspaceService()
		if err != nil {
			return err
		}
		defer closeFn()
		info, err := service.CreateWorkspace(cmd.Context(), id, domain.AgentIdentity{AgentName: agent, InstanceID: instance}, base)
		if err != nil {
			return err
		}
		if s.json {
			return writeJSON(s.out, map[string]any{"workspace": toWorkspaceDTO(info)}, nil)
		}
		_, err = fmt.Fprintf(s.out, "Workspace for task #%d is ready: %s (branch %s, base %s)\n", info.Workspace.TaskID, info.Workspace.Path, info.Workspace.Branch, info.Workspace.BaseCommit)
		return err
	}}
	identityFlags(cmd, &agent, &instance)
	cmd.Flags().StringVar(&base, "base", "", "base ref for the workspace branch (default HEAD)")
	return cmd
}

func (s *state) workspaceShowCommand() *cobra.Command {
	return &cobra.Command{Use: "show TASK_ID", Short: "Show a task's workspace", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := taskID(args[0])
		if err != nil {
			return err
		}
		service, closeFn, err := s.workspaceService()
		if err != nil {
			return err
		}
		defer closeFn()
		info, err := service.ShowWorkspace(cmd.Context(), id)
		if err != nil {
			return err
		}
		if s.json {
			return writeJSON(s.out, map[string]any{"workspace": toWorkspaceDTO(info)}, nil)
		}
		w := info.Workspace
		usage := string(info.Usage())
		if info.ActiveClaim != nil {
			usage = fmt.Sprintf("%s (%s/%s)", usage, info.ActiveClaim.AgentName, info.ActiveClaim.InstanceID)
		}
		_, err = fmt.Fprintf(s.out, "Workspace for task #%d\nState: %s\nUsage: %s\nPath: %s\nBranch: %s\nBase: %s\nProject: %s\n", w.TaskID, w.State, usage, w.Path, w.Branch, emptyDash(w.BaseCommit), info.ProjectRoot)
		if err == nil && w.Error != "" {
			_, err = fmt.Fprintf(s.out, "Error: %s\n", w.Error)
		}
		return err
	}}
}

func (s *state) workspaceListCommand() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List workspaces", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		service, closeFn, err := s.workspaceService()
		if err != nil {
			return err
		}
		defer closeFn()
		infos, err := service.ListWorkspaces(cmd.Context())
		if err != nil {
			return err
		}
		if s.json {
			dto := make([]workspaceDTO, 0, len(infos))
			for _, info := range infos {
				dto = append(dto, toWorkspaceDTO(info))
			}
			return writeJSON(s.out, map[string]any{"workspaces": dto}, nil)
		}
		if len(infos) == 0 {
			_, err = fmt.Fprintln(s.out, "No workspaces.")
			return err
		}
		for _, info := range infos {
			w := info.Workspace
			if _, err = fmt.Fprintf(s.out, "#%-4d %-10s %-6s %s  %s\n", w.TaskID, w.State, info.Usage(), w.Branch, w.Path); err != nil {
				return err
			}
		}
		return nil
	}}
}

func (s *state) workspaceRemoveCommand() *cobra.Command {
	var agent, instance string
	var force, deleteBranch bool
	cmd := &cobra.Command{Use: "remove TASK_ID", Short: "Remove a task's worktree", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := taskID(args[0])
		if err != nil {
			return err
		}
		service, closeFn, err := s.workspaceService()
		if err != nil {
			return err
		}
		defer closeFn()
		opts := app.RemoveWorkspaceOptions{Force: force, DeleteBranch: deleteBranch}
		if agent != "" || instance != "" {
			opts.Identity = &domain.AgentIdentity{AgentName: agent, InstanceID: instance}
		}
		info, err := service.RemoveWorkspace(cmd.Context(), id, opts)
		if err != nil {
			return err
		}
		if s.json {
			return writeJSON(s.out, map[string]any{"workspace": toWorkspaceDTO(info)}, nil)
		}
		_, err = fmt.Fprintf(s.out, "Removed workspace for task #%d: %s\n", info.Workspace.TaskID, info.Workspace.Path)
		return err
	}}
	identityFlags(cmd, &agent, &instance)
	cmd.Flags().BoolVar(&force, "force", false, "discard uncommitted changes and bypass the in-use ownership check")
	cmd.Flags().BoolVar(&deleteBranch, "delete-branch", false, "also delete the workspace branch")
	return cmd
}

type workspaceDTO struct {
	TaskID       int64       `json:"task_id"`
	State        string      `json:"state"`
	Usage        string      `json:"usage"`
	ActiveClaim  *claimDTO   `json:"active_claim"`
	Path         string      `json:"path"`
	Branch       string      `json:"branch"`
	BaseCommit   string      `json:"base_commit"`
	CreatedBy    identityDTO `json:"created_by"`
	ProjectRoot  string      `json:"project_root"`
	Database     string      `json:"database"`
	GitCommonDir string      `json:"git_common_dir"`
	CreatedAt    string      `json:"created_at"`
	UpdatedAt    string      `json:"updated_at"`
	Error        string      `json:"error"`
}

func toWorkspaceDTO(info app.WorkspaceInfo) workspaceDTO {
	w := info.Workspace
	d := workspaceDTO{
		TaskID: w.TaskID, State: string(w.State), Usage: string(info.Usage()),
		Path: w.Path, Branch: w.Branch, BaseCommit: w.BaseCommit,
		CreatedBy:   identityDTO{AgentName: w.CreatedBy.AgentName, InstanceID: w.CreatedBy.InstanceID},
		ProjectRoot: info.ProjectRoot, Database: info.Database, GitCommonDir: info.GitCommonDir,
		CreatedAt: formatJSONTime(w.CreatedAt), UpdatedAt: formatJSONTime(w.UpdatedAt), Error: w.Error,
	}
	if info.ActiveClaim != nil {
		d.ActiveClaim = &claimDTO{AgentName: info.ActiveClaim.AgentName, InstanceID: info.ActiveClaim.InstanceID, ClaimedAt: formatJSONTime(info.ActiveClaim.ClaimedAt)}
	}
	return d
}
