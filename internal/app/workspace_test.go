package app

// Workspace orchestration tests run against the real sqlite store and real
// temporary Git repositories (docs/WORKSPACES.md): the invariants under test
// — two-phase allocation, conservative recovery, claim-derived authorization
// — live exactly at the seam between the database and Git. The stub runner
// is used only to inject Git failures after a successful reservation.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	gitrunner "github.com/alle80/griglia-tui/internal/git"

	"github.com/alle80/griglia-tui/internal/domain"
	"github.com/alle80/griglia-tui/internal/sqlite"
)

type stubRunner struct {
	GitRunner
	addErr error
}

func (s *stubRunner) AddWorktree(ctx context.Context, repoDir, path, branch, commit string) error {
	if s.addErr != nil {
		return s.addErr
	}
	return s.GitRunner.AddWorktree(ctx, repoDir, path, branch, commit)
}

type wsFixture struct {
	t        *testing.T
	store    *sqlite.Store
	svc      *WorkspaceService
	stub     *stubRunner
	parent   string
	root     string
	dbPath   string
	identity domain.AgentIdentity
}

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newWorkspaceFixture(t *testing.T, withCommit bool) *wsFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, root, "init", "--quiet")
	if withCommit {
		if err := os.WriteFile(filepath.Join(root, "main.txt"), []byte("v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitCmd(t, root, "add", ".")
		gitCmd(t, root, "commit", "--quiet", "-m", "initial")
	}
	dbPath := filepath.Join(root, ".griglia", "griglia.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	stub := &stubRunner{GitRunner: gitrunner.Runner{}}
	return &wsFixture{
		t: t, store: store, stub: stub,
		svc:    NewWorkspaceService(store, stub, root, dbPath),
		parent: parent, root: root, dbPath: dbPath,
		identity: domain.AgentIdentity{AgentName: "claude", InstanceID: "ws-test-1"},
	}
}

func (f *wsFixture) claimedTask(title string) domain.Task {
	f.t.Helper()
	now := time.Now().UTC()
	task, err := f.store.CreateTask(context.Background(), domain.Task{UID: title, Title: title, Lifecycle: domain.LifecycleReady, Priority: domain.PriorityNormal, CreatedAt: now, UpdatedAt: now, Version: 1})
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err = f.store.ClaimTask(context.Background(), task.ID, f.identity, now); err != nil {
		f.t.Fatal(err)
	}
	return task
}

func TestCreateWorkspaceSuccess(t *testing.T) {
	f := newWorkspaceFixture(t, true)
	ctx := context.Background()
	task := f.claimedTask("Implement the Feature")
	head := gitCmd(t, f.root, "rev-parse", "HEAD")

	info, err := f.svc.CreateWorkspace(ctx, task.ID, f.identity, "")
	if err != nil {
		t.Fatal(err)
	}
	w := info.Workspace
	wantPath := filepath.Join(f.parent, ".griglia-worktrees", "proj", fmt.Sprintf("task-%d", task.ID))
	if w.Path != wantPath {
		t.Fatalf("path=%q want %q", w.Path, wantPath)
	}
	wantBranch := fmt.Sprintf("griglia/task-%d-implement-the-feature", task.ID)
	if w.Branch != wantBranch {
		t.Fatalf("branch=%q want %q", w.Branch, wantBranch)
	}
	if w.State != domain.WorkspaceReady || w.BaseCommit != head {
		t.Fatalf("state=%s base=%q want ready/%q", w.State, w.BaseCommit, head)
	}
	if got := gitCmd(t, w.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != wantBranch {
		t.Fatalf("checked-out branch=%q", got)
	}
	if info.ProjectRoot != f.root || info.Database != f.dbPath || info.GitCommonDir != filepath.Join(f.root, ".git") {
		t.Fatalf("launcher facts=%+v", info)
	}
	if _, err = os.Stat(filepath.Join(w.Path, ".griglia")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree must not contain a nested .griglia: %v", err)
	}
	live, err := f.store.LiveWorkspaceForTask(ctx, task.ID)
	if err != nil || live == nil || live.State != domain.WorkspaceReady {
		t.Fatalf("live=%+v err=%v", live, err)
	}
}

func TestCreateWorkspaceExplicitBase(t *testing.T) {
	f := newWorkspaceFixture(t, true)
	first := gitCmd(t, f.root, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(f.root, "main.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, f.root, "commit", "--quiet", "-am", "second")
	task := f.claimedTask("explicit base")

	info, err := f.svc.CreateWorkspace(context.Background(), task.ID, f.identity, first)
	if err != nil {
		t.Fatal(err)
	}
	if info.Workspace.BaseCommit != first {
		t.Fatalf("base=%q want %q", info.Workspace.BaseCommit, first)
	}
	content, err := os.ReadFile(filepath.Join(info.Workspace.Path, "main.txt"))
	if err != nil || string(content) != "v1\n" {
		t.Fatalf("worktree content=%q err=%v", content, err)
	}
}

func TestCreateWorkspaceDirtyMainCheckoutAllowed(t *testing.T) {
	f := newWorkspaceFixture(t, true)
	if err := os.WriteFile(filepath.Join(f.root, "main.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "untracked.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	task := f.claimedTask("dirty main")

	info, err := f.svc.CreateWorkspace(context.Background(), task.ID, f.identity, "")
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(info.Workspace.Path, "main.txt"))
	if err != nil || string(content) != "v1\n" {
		t.Fatalf("uncommitted change leaked into worktree: %q err=%v", content, err)
	}
	if _, err = os.Stat(filepath.Join(info.Workspace.Path, "untracked.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("untracked file leaked into worktree: %v", err)
	}
}

func TestCreateWorkspaceUnbornRepositoryFails(t *testing.T) {
	f := newWorkspaceFixture(t, false)
	task := f.claimedTask("no commits")

	_, err := f.svc.CreateWorkspace(context.Background(), task.ID, f.identity, "")
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("err=%v want ErrInvalid", err)
	}
	history, err := f.store.WorkspacesForTask(context.Background(), task.ID)
	if err != nil || len(history) != 0 {
		t.Fatalf("no row should be reserved before the base resolves: %+v err=%v", history, err)
	}
}

func TestCreateWorkspaceRequiresClaimOwner(t *testing.T) {
	f := newWorkspaceFixture(t, true)
	ctx := context.Background()
	task := f.claimedTask("owned elsewhere")

	other := domain.AgentIdentity{AgentName: "codex", InstanceID: "other-1"}
	if _, err := f.svc.CreateWorkspace(ctx, task.ID, other, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("non-owner err=%v want ErrConflict", err)
	}
	now := time.Now().UTC()
	unclaimed, err := f.store.CreateTask(ctx, domain.Task{UID: "unclaimed", Title: "unclaimed", Lifecycle: domain.LifecycleReady, Priority: domain.PriorityNormal, CreatedAt: now, UpdatedAt: now, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.CreateWorkspace(ctx, unclaimed.ID, f.identity, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("unclaimed err=%v want ErrConflict", err)
	}
}

func TestCreateWorkspaceForeignBranchConflict(t *testing.T) {
	f := newWorkspaceFixture(t, true)
	task := f.claimedTask("branch clash")
	gitCmd(t, f.root, "branch", fmt.Sprintf("griglia/task-%d-branch-clash", task.ID))

	_, err := f.svc.CreateWorkspace(context.Background(), task.ID, f.identity, "")
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err=%v want ErrConflict", err)
	}
	live, err := f.store.LiveWorkspaceForTask(context.Background(), task.ID)
	if err != nil || live != nil {
		t.Fatalf("foreign branch must not reserve a row: %+v err=%v", live, err)
	}
}

func TestCreateWorkspaceForeignPathConflict(t *testing.T) {
	f := newWorkspaceFixture(t, true)
	task := f.claimedTask("path clash")
	path := WorkspacePathFor(f.root, task.ID)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "somebody-elses.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := f.svc.CreateWorkspace(context.Background(), task.ID, f.identity, "")
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err=%v want ErrConflict", err)
	}
	if _, err = os.Stat(filepath.Join(path, "somebody-elses.txt")); err != nil {
		t.Fatalf("foreign path content must be untouched: %v", err)
	}
}

func TestCreateWorkspaceGitFailureThenRetry(t *testing.T) {
	f := newWorkspaceFixture(t, true)
	ctx := context.Background()
	task := f.claimedTask("flaky allocation")

	f.stub.addErr = errors.New("simulated git failure")
	if _, err := f.svc.CreateWorkspace(ctx, task.ID, f.identity, ""); err == nil || !strings.Contains(err.Error(), "simulated git failure") {
		t.Fatalf("err=%v", err)
	}
	history, err := f.store.WorkspacesForTask(ctx, task.ID)
	if err != nil || len(history) != 1 || history[0].State != domain.WorkspaceFailed || history[0].Error != "simulated git failure" {
		t.Fatalf("failed row=%+v err=%v", history, err)
	}

	// Simulate a half-created directory left behind by the failed attempt:
	// the retry must clean it because the failed row records this exact path.
	if err = os.MkdirAll(filepath.Join(history[0].Path, "partial"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.stub.addErr = nil
	info, err := f.svc.CreateWorkspace(ctx, task.ID, f.identity, "")
	if err != nil {
		t.Fatal(err)
	}
	if info.Workspace.State != domain.WorkspaceReady || info.Workspace.ID == history[0].ID {
		t.Fatalf("retry workspace=%+v", info.Workspace)
	}
	if _, err = os.Stat(filepath.Join(info.Workspace.Path, "partial")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("leftover directory content survived the retry: %v", err)
	}
	if history, err = f.store.WorkspacesForTask(ctx, task.ID); err != nil || len(history) != 2 {
		t.Fatalf("history after retry=%+v err=%v", history, err)
	}
}

func TestCreateWorkspaceIdempotentReuse(t *testing.T) {
	f := newWorkspaceFixture(t, true)
	ctx := context.Background()
	task := f.claimedTask("reused")

	first, err := f.svc.CreateWorkspace(ctx, task.ID, f.identity, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.svc.CreateWorkspace(ctx, task.ID, f.identity, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.Workspace.ID != first.Workspace.ID {
		t.Fatalf("reuse returned a different workspace: %d vs %d", second.Workspace.ID, first.Workspace.ID)
	}
	worktrees := strings.Count(gitCmd(t, f.root, "worktree", "list", "--porcelain"), "worktree ")
	if worktrees != 2 { // main checkout + exactly one workspace
		t.Fatalf("worktrees=%d", worktrees)
	}
}

func TestCreateWorkspaceInFlightAllocationConflicts(t *testing.T) {
	f := newWorkspaceFixture(t, true)
	ctx := context.Background()
	task := f.claimedTask("in flight")
	if _, err := f.store.ReserveWorkspace(ctx, task.ID, "/elsewhere/task-x", "griglia/task-x", "c", f.identity, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	if _, err := f.svc.CreateWorkspace(ctx, task.ID, f.identity, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err=%v want ErrConflict", err)
	}
}

func TestConcurrentCreatesNeverProduceTwoWorktrees(t *testing.T) {
	f := newWorkspaceFixture(t, true)
	ctx := context.Background()
	task := f.claimedTask("raced")

	var wg sync.WaitGroup
	results := make([]error, 2)
	ids := make([]int64, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			info, err := f.svc.CreateWorkspace(ctx, task.ID, f.identity, "")
			results[i], ids[i] = err, info.Workspace.ID
		}(i)
	}
	wg.Wait()

	successes := 0
	for i, err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrConflict):
		default:
			t.Fatalf("result %d: unexpected error %v", i, err)
		}
	}
	if successes == 0 {
		t.Fatal("at least one create must win the race")
	}
	live, err := f.store.LiveWorkspaceForTask(ctx, task.ID)
	if err != nil || live == nil || live.State != domain.WorkspaceReady {
		t.Fatalf("live=%+v err=%v", live, err)
	}
	for i := range results {
		if results[i] == nil && ids[i] != live.ID {
			t.Fatalf("success %d returned workspace %d, live is %d", i, ids[i], live.ID)
		}
	}
	worktrees := strings.Count(gitCmd(t, f.root, "worktree", "list", "--porcelain"), "worktree ")
	if worktrees != 2 {
		t.Fatalf("worktrees=%d, racing creates must never produce two", worktrees)
	}
}

func TestRemoveWorkspaceKeepsBranchByDefault(t *testing.T) {
	f := newWorkspaceFixture(t, true)
	ctx := context.Background()
	task := f.claimedTask("removed cleanly")
	info, err := f.svc.CreateWorkspace(ctx, task.ID, f.identity, "")
	if err != nil {
		t.Fatal(err)
	}

	removed, err := f.svc.RemoveWorkspace(ctx, task.ID, RemoveWorkspaceOptions{Identity: &f.identity})
	if err != nil {
		t.Fatal(err)
	}
	if removed.State != domain.WorkspaceRemoved {
		t.Fatalf("state=%s", removed.State)
	}
	if _, err = os.Stat(info.Workspace.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree directory still exists: %v", err)
	}
	if out := gitCmd(t, f.root, "branch", "--list", info.Workspace.Branch); out == "" {
		t.Fatal("branch must be kept by default")
	}
	if live, liveErr := f.store.LiveWorkspaceForTask(ctx, task.ID); liveErr != nil || live != nil {
		t.Fatalf("live after remove=%+v err=%v", live, liveErr)
	}
}

func TestRemoveWorkspaceDeleteBranch(t *testing.T) {
	f := newWorkspaceFixture(t, true)
	ctx := context.Background()
	task := f.claimedTask("branch deleted")
	info, err := f.svc.CreateWorkspace(ctx, task.ID, f.identity, "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err = f.svc.RemoveWorkspace(ctx, task.ID, RemoveWorkspaceOptions{Identity: &f.identity, DeleteBranch: true}); err != nil {
		t.Fatal(err)
	}
	if out := gitCmd(t, f.root, "branch", "--list", info.Workspace.Branch); out != "" {
		t.Fatalf("branch still exists: %q", out)
	}
}

func TestRemoveWorkspaceDirtyRefusedWithoutForce(t *testing.T) {
	f := newWorkspaceFixture(t, true)
	ctx := context.Background()
	task := f.claimedTask("dirty worktree")
	info, err := f.svc.CreateWorkspace(ctx, task.ID, f.identity, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(info.Workspace.Path, "wip.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err = f.svc.RemoveWorkspace(ctx, task.ID, RemoveWorkspaceOptions{Identity: &f.identity}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err=%v want ErrConflict", err)
	}
	if _, err = os.Stat(info.Workspace.Path); err != nil {
		t.Fatalf("refused removal must leave the worktree in place: %v", err)
	}
	if _, err = f.svc.RemoveWorkspace(ctx, task.ID, RemoveWorkspaceOptions{Identity: &f.identity, Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(info.Workspace.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forced removal left the worktree: %v", err)
	}
}

func TestRemoveWorkspaceInUseAuthorization(t *testing.T) {
	f := newWorkspaceFixture(t, true)
	ctx := context.Background()
	task := f.claimedTask("in use")
	if _, err := f.svc.CreateWorkspace(ctx, task.ID, f.identity, ""); err != nil {
		t.Fatal(err)
	}

	other := domain.AgentIdentity{AgentName: "codex", InstanceID: "other-1"}
	if _, err := f.svc.RemoveWorkspace(ctx, task.ID, RemoveWorkspaceOptions{Identity: &other}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("other identity err=%v want ErrConflict", err)
	}
	if _, err := f.svc.RemoveWorkspace(ctx, task.ID, RemoveWorkspaceOptions{}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("anonymous err=%v want ErrConflict", err)
	}
	// Force is the explicit human override for an in-use workspace.
	if _, err := f.svc.RemoveWorkspace(ctx, task.ID, RemoveWorkspaceOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveWorkspaceIdleAfterCompletionNeedsNoIdentity(t *testing.T) {
	f := newWorkspaceFixture(t, true)
	ctx := context.Background()
	task := f.claimedTask("completed")
	info, err := f.svc.CreateWorkspace(ctx, task.ID, f.identity, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.CompleteClaimedTask(ctx, task.ID, "done", f.identity, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// Completion must not have touched the workspace row: still ready, idle.
	live, err := f.store.LiveWorkspaceForTask(ctx, task.ID)
	if err != nil || live == nil || live.State != domain.WorkspaceReady || live.ID != info.Workspace.ID {
		t.Fatalf("workspace after done=%+v err=%v", live, err)
	}

	if _, err = f.svc.RemoveWorkspace(ctx, task.ID, RemoveWorkspaceOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveWorkspaceMissingDirectoryRecovery(t *testing.T) {
	f := newWorkspaceFixture(t, true)
	ctx := context.Background()
	task := f.claimedTask("vanished")
	info, err := f.svc.CreateWorkspace(ctx, task.ID, f.identity, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(info.Workspace.Path); err != nil {
		t.Fatal(err)
	}

	removed, err := f.svc.RemoveWorkspace(ctx, task.ID, RemoveWorkspaceOptions{Identity: &f.identity})
	if err != nil {
		t.Fatal(err)
	}
	if removed.State != domain.WorkspaceRemoved {
		t.Fatalf("state=%s", removed.State)
	}
	if out := gitCmd(t, f.root, "worktree", "list", "--porcelain"); strings.Contains(out, info.Workspace.Path) {
		t.Fatalf("stale registration survived: %q", out)
	}
}

func TestRemoveWorkspaceUnregisteredDirectoryRequiresForce(t *testing.T) {
	f := newWorkspaceFixture(t, true)
	ctx := context.Background()
	task := f.claimedTask("unregistered")
	info, err := f.svc.CreateWorkspace(ctx, task.ID, f.identity, "")
	if err != nil {
		t.Fatal(err)
	}
	// Drop the registration while keeping the directory — the "unregistered"
	// health case, e.g. after a prune ran while the directory was unmounted.
	entries, err := os.ReadDir(filepath.Join(f.root, ".git", "worktrees"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("worktrees metadata entries=%v err=%v", entries, err)
	}
	if err = os.RemoveAll(filepath.Join(f.root, ".git", "worktrees", entries[0].Name())); err != nil {
		t.Fatal(err)
	}

	if _, err = f.svc.RemoveWorkspace(ctx, task.ID, RemoveWorkspaceOptions{Identity: &f.identity}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err=%v want ErrConflict", err)
	}
	if _, err = f.svc.RemoveWorkspace(ctx, task.ID, RemoveWorkspaceOptions{Identity: &f.identity, Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(info.Workspace.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forced removal left the directory: %v", err)
	}
}

func TestRemoveFailedWorkspaceAllowsFreshAllocation(t *testing.T) {
	f := newWorkspaceFixture(t, true)
	ctx := context.Background()
	task := f.claimedTask("failed then fresh")
	f.stub.addErr = errors.New("boom")
	if _, err := f.svc.CreateWorkspace(ctx, task.ID, f.identity, ""); err == nil {
		t.Fatal("expected failure")
	}
	f.stub.addErr = nil

	removed, err := f.svc.RemoveWorkspace(ctx, task.ID, RemoveWorkspaceOptions{Identity: &f.identity})
	if err != nil || removed.State != domain.WorkspaceRemoved {
		t.Fatalf("removed=%+v err=%v", removed, err)
	}
	info, err := f.svc.CreateWorkspace(ctx, task.ID, f.identity, "")
	if err != nil || info.Workspace.State != domain.WorkspaceReady {
		t.Fatalf("fresh allocation=%+v err=%v", info, err)
	}
}

func TestRemoveWorkspaceWithoutWorkspaceIsNotFound(t *testing.T) {
	f := newWorkspaceFixture(t, true)
	task := f.claimedTask("no workspace")
	if _, err := f.svc.RemoveWorkspace(context.Background(), task.ID, RemoveWorkspaceOptions{Identity: &f.identity}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err=%v want ErrNotFound", err)
	}
}

// TestNoNetworkGitCommands intercepts every git invocation of a full
// create/remove cycle through a PATH shim and asserts none of them is a
// network operation — "Griglia never talks to a network" made executable.
func TestNoNetworkGitCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim requires a POSIX shell")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary not available")
	}
	shimDir := t.TempDir()
	logPath := filepath.Join(shimDir, "git.log")
	shim := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$GIT_SHIM_LOG\"\nexec \"$REAL_GIT\" \"$@\"\n"
	if err = os.WriteFile(filepath.Join(shimDir, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAL_GIT", realGit)
	t.Setenv("GIT_SHIM_LOG", logPath)
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	f := newWorkspaceFixture(t, true)
	ctx := context.Background()
	task := f.claimedTask("offline only")
	if _, err = f.svc.CreateWorkspace(ctx, task.ID, f.identity, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.RemoveWorkspace(ctx, task.ID, RemoveWorkspaceOptions{Identity: &f.identity, DeleteBranch: true}); err != nil {
		t.Fatal(err)
	}

	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(logged)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "fetch", "pull", "push", "clone", "ls-remote", "remote", "submodule":
			t.Fatalf("network git command executed: %q", line)
		}
	}
}
