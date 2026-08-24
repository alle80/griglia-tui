package app

// Workspace allocation and removal (docs/WORKSPACES.md): the service
// orchestrates the two-phase allocation — reserve the row in one SQLite
// transaction, run Git outside it, then record ready or failed — and the
// symmetric removal. Ownership stays derived from the live claims table;
// nothing here stores or implies it.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/alle80/griglia-tui/internal/domain"
)

// GitRunner is the port for local Git side effects. It is deliberately small
// and mockable; the real implementation (internal/git) shells out to the git
// CLI and never performs network operations.
type GitRunner interface {
	CommonDir(ctx context.Context, repoDir string) (string, error)
	ResolveCommit(ctx context.Context, repoDir, ref string) (string, error)
	BranchExists(ctx context.Context, repoDir, branch string) (bool, error)
	AddWorktree(ctx context.Context, repoDir, path, branch, commit string) error
	RemoveWorktree(ctx context.Context, repoDir, path string, force bool) error
	PruneWorktrees(ctx context.Context, repoDir string) error
	WorktreeRegistered(ctx context.Context, repoDir, path string) (bool, error)
	WorktreeDirty(ctx context.Context, worktreePath string) (bool, error)
	DeleteBranch(ctx context.Context, repoDir, branch string) error
}

// WorkspaceStore is the persistence port for workspace orchestration,
// satisfied by *sqlite.Store. Reservation performs the transactional
// ownership check; the service never holds a database transaction across Git
// operations.
type WorkspaceStore interface {
	GetTask(context.Context, int64) (domain.TaskView, error)
	ReserveWorkspace(ctx context.Context, taskID int64, path, branch, baseCommit string, identity domain.AgentIdentity, now time.Time) (domain.Workspace, error)
	MarkWorkspaceReady(ctx context.Context, workspaceID int64, now time.Time) (domain.Workspace, error)
	MarkWorkspaceFailed(ctx context.Context, workspaceID int64, message string, now time.Time) (domain.Workspace, error)
	RemoveWorkspace(ctx context.Context, workspaceID int64, now time.Time) (domain.Workspace, error)
	LiveWorkspaceForTask(ctx context.Context, taskID int64) (*domain.Workspace, error)
	WorkspacesForTask(ctx context.Context, taskID int64) ([]domain.Workspace, error)
}

// WorkspaceInfo bundles a workspace with the path facts an external launcher
// needs to derive sandbox permissions and pin GRIGLIA_PROJECT
// (docs/WORKSPACES.md §9). Every path is absolute.
type WorkspaceInfo struct {
	Workspace    domain.Workspace
	ProjectRoot  string
	Database     string
	GitCommonDir string
}

type WorkspaceService struct {
	store       WorkspaceStore
	git         GitRunner
	projectRoot string
	database    string
	now         func() time.Time
}

// NewWorkspaceService wires the ports for one project. projectRoot is the
// main checkout containing .griglia, database the authoritative database
// file; both must be absolute.
func NewWorkspaceService(store WorkspaceStore, git GitRunner, projectRoot, database string) *WorkspaceService {
	return &WorkspaceService{store: store, git: git, projectRoot: filepath.Clean(projectRoot), database: filepath.Clean(database), now: time.Now}
}

// WorkspacePathFor computes the deterministic worktree location for a task:
// <project-parent>/.griglia-worktrees/<project-name>/task-<id> — outside both
// the main checkout and .griglia, so working copies never mix with
// coordination state and repository tooling never wanders into another
// agent's worktree.
func WorkspacePathFor(projectRoot string, taskID int64) string {
	root := filepath.Clean(projectRoot)
	return filepath.Join(filepath.Dir(root), ".griglia-worktrees", filepath.Base(root), fmt.Sprintf("task-%d", taskID))
}

// CreateWorkspace allocates the task's worktree and branch for the active
// claim owner. A ready workspace is returned as-is (idempotent reuse, with
// its persisted branch — a later title edit never renames it); an in-flight
// allocation is a conflict rather than a second racing Git operation. The
// reservation transaction is the race arbiter: two concurrent creates can
// never both insert a live row for the same task, path, or branch.
func (s *WorkspaceService) CreateWorkspace(ctx context.Context, taskID int64, identity domain.AgentIdentity, baseRef string) (WorkspaceInfo, error) {
	if err := validateIdentity(identity); err != nil {
		return WorkspaceInfo{}, err
	}
	commonDir, err := s.git.CommonDir(ctx, s.projectRoot)
	if err != nil {
		return WorkspaceInfo{}, fmt.Errorf("project root is not usable as a Git repository: %w", err)
	}
	view, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	if view.Lifecycle != domain.LifecycleReady || !claimOwnedBy(view.ActiveClaim, identity) {
		return WorkspaceInfo{}, fmt.Errorf("only the active claim owner of a ready task can create its workspace: %w", domain.ErrConflict)
	}
	live, err := s.store.LiveWorkspaceForTask(ctx, taskID)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	if live != nil {
		if live.State == domain.WorkspaceReady {
			return s.info(*live, commonDir), nil
		}
		return WorkspaceInfo{}, fmt.Errorf("a workspace allocation is already in flight for this task: %w", domain.ErrConflict)
	}
	if baseRef == "" {
		baseRef = "HEAD"
	}
	baseCommit, err := s.git.ResolveCommit(ctx, s.projectRoot, baseRef)
	if err != nil {
		return WorkspaceInfo{}, fmt.Errorf("cannot resolve base ref %q to a commit (%v): %w", baseRef, err, domain.ErrInvalid)
	}
	branch := domain.WorkspaceBranchName(taskID, view.Title)
	if exists, branchErr := s.git.BranchExists(ctx, s.projectRoot, branch); branchErr != nil {
		return WorkspaceInfo{}, branchErr
	} else if exists {
		// Never adopt a branch griglia has no record of creating — a live
		// workspace on this branch would have been returned above.
		return WorkspaceInfo{}, fmt.Errorf("branch %q already exists and is not managed by griglia: %w", branch, domain.ErrConflict)
	}
	path := WorkspacePathFor(s.projectRoot, taskID)
	if err = s.prepareWorkspacePath(ctx, taskID, path); err != nil {
		return WorkspaceInfo{}, err
	}
	w, err := s.store.ReserveWorkspace(ctx, taskID, path, branch, baseCommit, identity, s.now().UTC())
	if err != nil {
		return WorkspaceInfo{}, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		err = s.git.AddWorktree(ctx, s.projectRoot, path, branch, baseCommit)
	}
	if err != nil {
		if _, markErr := s.store.MarkWorkspaceFailed(ctx, w.ID, err.Error(), s.now().UTC()); markErr != nil {
			return WorkspaceInfo{}, errors.Join(err, markErr)
		}
		return WorkspaceInfo{}, fmt.Errorf("worktree creation failed: %w", err)
	}
	ready, err := s.store.MarkWorkspaceReady(ctx, w.ID, s.now().UTC())
	if err != nil {
		return WorkspaceInfo{}, err
	}
	return s.info(ready, commonDir), nil
}

// prepareWorkspacePath clears leftovers of this task's own earlier attempts
// ("retry first attempts to clean any half-created directory/registration")
// and refuses anything griglia has no record of: a path is cleaned only when
// a persisted workspace row for this exact task recorded this exact path.
func (s *WorkspaceService) prepareWorkspacePath(ctx context.Context, taskID int64, path string) error {
	registered, err := s.git.WorktreeRegistered(ctx, s.projectRoot, path)
	if err != nil {
		return err
	}
	exists, err := pathExists(path)
	if err != nil {
		return err
	}
	if exists {
		recorded, recErr := s.pathRecordedForTask(ctx, taskID, path)
		if recErr != nil {
			return recErr
		}
		if !recorded {
			return fmt.Errorf("workspace path %q already exists and is not managed by griglia: %w", path, domain.ErrConflict)
		}
		if registered {
			return s.git.RemoveWorktree(ctx, s.projectRoot, path, true)
		}
		if err = os.RemoveAll(path); err != nil {
			return err
		}
		return s.git.PruneWorktrees(ctx, s.projectRoot)
	}
	if registered {
		// Registration without a directory is stale metadata; prune is the
		// non-destructive repair and touches nothing that still exists.
		return s.git.PruneWorktrees(ctx, s.projectRoot)
	}
	return nil
}

// pathRecordedForTask reports whether a no-longer-live row (failed or
// removed) recorded the path — the only evidence that makes a leftover
// cleanable. A live row at the path means another allocation is in flight or
// completed since our earlier read, which is a conflict, never a cleanup
// target: force-removing it would destroy a concurrent creator's worktree.
func (s *WorkspaceService) pathRecordedForTask(ctx context.Context, taskID int64, path string) (bool, error) {
	history, err := s.store.WorkspacesForTask(ctx, taskID)
	if err != nil {
		return false, err
	}
	recorded := false
	for _, w := range history {
		if w.Path != path {
			continue
		}
		if w.State.Live() {
			return false, fmt.Errorf("a workspace allocation is already in flight for this task: %w", domain.ErrConflict)
		}
		recorded = true
	}
	return recorded, nil
}

// RemoveWorkspaceOptions selects who is removing and how destructive the
// removal may be. A nil Identity is the human-side removal of an idle
// workspace; Force is the explicit human override that both discards
// uncommitted work and bypasses the in-use ownership check.
type RemoveWorkspaceOptions struct {
	Identity     *domain.AgentIdentity
	Force        bool
	DeleteBranch bool
}

// RemoveWorkspace prunes the task's managed worktree and marks the row
// removed. The branch is kept by default (it usually outlives the task for
// PR review); DeleteBranch removes it only for workspaces that reached ready,
// because a failed allocation never proved the branch is griglia's. A missing
// directory is tolerated as the documented recovery: git metadata is pruned
// and the row still transitions to removed.
func (s *WorkspaceService) RemoveWorkspace(ctx context.Context, taskID int64, opts RemoveWorkspaceOptions) (domain.Workspace, error) {
	if opts.Identity != nil {
		if err := validateIdentity(*opts.Identity); err != nil {
			return domain.Workspace{}, err
		}
	}
	view, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return domain.Workspace{}, err
	}
	w, err := s.removalTarget(ctx, taskID)
	if err != nil {
		return domain.Workspace{}, err
	}
	if view.ActiveClaim != nil && !opts.Force && (opts.Identity == nil || !claimOwnedBy(view.ActiveClaim, *opts.Identity)) {
		return domain.Workspace{}, fmt.Errorf("workspace is in use by the task's active claim; removal requires the owning identity or force: %w", domain.ErrConflict)
	}
	registered, err := s.git.WorktreeRegistered(ctx, s.projectRoot, w.Path)
	if err != nil {
		return domain.Workspace{}, err
	}
	exists, err := pathExists(w.Path)
	if err != nil {
		return domain.Workspace{}, err
	}
	switch {
	case registered && exists:
		if !opts.Force {
			dirty, dirtyErr := s.git.WorktreeDirty(ctx, w.Path)
			if dirtyErr != nil {
				return domain.Workspace{}, dirtyErr
			}
			if dirty {
				return domain.Workspace{}, fmt.Errorf("worktree has uncommitted changes; removal without force would destroy them: %w", domain.ErrConflict)
			}
		}
		if err = s.git.RemoveWorktree(ctx, s.projectRoot, w.Path, opts.Force); err != nil {
			return domain.Workspace{}, err
		}
	case exists:
		// The directory is no longer a registered worktree, so Git cannot
		// vouch for its contents; only an explicit force may delete it.
		if !opts.Force {
			return domain.Workspace{}, fmt.Errorf("workspace directory exists but is not a registered worktree; repair with 'git worktree repair' or pass force: %w", domain.ErrConflict)
		}
		if err = os.RemoveAll(w.Path); err != nil {
			return domain.Workspace{}, err
		}
	}
	if err = s.git.PruneWorktrees(ctx, s.projectRoot); err != nil {
		return domain.Workspace{}, err
	}
	if opts.DeleteBranch && w.State == domain.WorkspaceReady {
		if exists, branchErr := s.git.BranchExists(ctx, s.projectRoot, w.Branch); branchErr != nil {
			return domain.Workspace{}, branchErr
		} else if exists {
			if err = s.git.DeleteBranch(ctx, s.projectRoot, w.Branch); err != nil {
				return domain.Workspace{}, err
			}
		}
	}
	return s.store.RemoveWorkspace(ctx, w.ID, s.now().UTC())
}

// removalTarget resolves which row a by-task removal addresses: the live
// workspace (ready, or allocating as the stuck-allocation repair), else the
// most recent failed row.
func (s *WorkspaceService) removalTarget(ctx context.Context, taskID int64) (domain.Workspace, error) {
	live, err := s.store.LiveWorkspaceForTask(ctx, taskID)
	if err != nil {
		return domain.Workspace{}, err
	}
	if live != nil {
		return *live, nil
	}
	history, err := s.store.WorkspacesForTask(ctx, taskID)
	if err != nil {
		return domain.Workspace{}, err
	}
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].State == domain.WorkspaceFailed {
			return history[i], nil
		}
	}
	return domain.Workspace{}, fmt.Errorf("task has no workspace to remove: %w", domain.ErrNotFound)
}

func (s *WorkspaceService) info(w domain.Workspace, commonDir string) WorkspaceInfo {
	return WorkspaceInfo{Workspace: w, ProjectRoot: s.projectRoot, Database: s.database, GitCommonDir: commonDir}
}

func claimOwnedBy(claim *domain.Claim, identity domain.AgentIdentity) bool {
	return claim != nil && claim.AgentName == identity.AgentName && claim.InstanceID == identity.InstanceID
}

func pathExists(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
