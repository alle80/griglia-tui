package git

// Runner tests use real temporary Git repositories: worktree semantics are
// exactly what this adapter exists to encapsulate, so mocking git here would
// test nothing.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
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

// initRepo creates a repository with one commit and returns its path.
func initRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "--quiet", "-m", "initial")
	return dir
}

func TestCommonDirIsAbsolute(t *testing.T) {
	repo := initRepo(t)
	r := Runner{}
	dir, err := r.CommonDir(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(dir) || filepath.Base(dir) != ".git" {
		t.Fatalf("common dir=%q", dir)
	}
	if _, err = r.CommonDir(context.Background(), t.TempDir()); err == nil {
		t.Fatal("expected error outside a repository")
	}
}

func TestResolveCommit(t *testing.T) {
	repo := initRepo(t)
	ctx := context.Background()
	head := runGit(t, repo, "rev-parse", "HEAD")
	r := Runner{}
	got, err := r.ResolveCommit(ctx, repo, "HEAD")
	if err != nil || got != head {
		t.Fatalf("got=%q err=%v want %q", got, err, head)
	}
	if _, err = r.ResolveCommit(ctx, repo, "no-such-ref"); err == nil {
		t.Fatal("expected error for unknown ref")
	}
	var gitErr *Error
	if _, err = r.ResolveCommit(ctx, repo, "no-such-ref"); !errors.As(err, &gitErr) || gitErr.ExitCode == 0 {
		t.Fatalf("expected structured *Error with exit code, got %v", err)
	}
}

func TestResolveCommitUnbornHead(t *testing.T) {
	requireGit(t)
	dir := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "--quiet")
	r := Runner{}
	if _, err := r.ResolveCommit(context.Background(), dir, "HEAD"); err == nil {
		t.Fatal("expected error for unborn HEAD")
	}
}

func TestBranchExists(t *testing.T) {
	repo := initRepo(t)
	ctx := context.Background()
	runGit(t, repo, "branch", "feature")
	r := Runner{}
	if exists, err := r.BranchExists(ctx, repo, "feature"); err != nil || !exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
	if exists, err := r.BranchExists(ctx, repo, "missing"); err != nil || exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
}

func TestWorktreeLifecycle(t *testing.T) {
	repo := initRepo(t)
	ctx := context.Background()
	r := Runner{}
	head := runGit(t, repo, "rev-parse", "HEAD")
	path := filepath.Join(filepath.Dir(repo), "wt")

	if err := r.AddWorktree(ctx, repo, path, "griglia/task-1-test", head); err != nil {
		t.Fatal(err)
	}
	if registered, err := r.WorktreeRegistered(ctx, repo, path); err != nil || !registered {
		t.Fatalf("registered=%v err=%v", registered, err)
	}
	if dirty, err := r.WorktreeDirty(ctx, path); err != nil || dirty {
		t.Fatalf("fresh worktree dirty=%v err=%v", dirty, err)
	}
	if err := os.WriteFile(filepath.Join(path, "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dirty, err := r.WorktreeDirty(ctx, path); err != nil || !dirty {
		t.Fatalf("dirty=%v err=%v", dirty, err)
	}
	if err := r.RemoveWorktree(ctx, repo, path, false); err == nil {
		t.Fatal("expected git to refuse removing a dirty worktree without force")
	}
	if err := r.RemoveWorktree(ctx, repo, path, true); err != nil {
		t.Fatal(err)
	}
	if registered, err := r.WorktreeRegistered(ctx, repo, path); err != nil || registered {
		t.Fatalf("registered after remove=%v err=%v", registered, err)
	}
	// The branch survives worktree removal until explicitly deleted.
	if exists, err := r.BranchExists(ctx, repo, "griglia/task-1-test"); err != nil || !exists {
		t.Fatalf("branch exists=%v err=%v", exists, err)
	}
	if err := r.DeleteBranch(ctx, repo, "griglia/task-1-test"); err != nil {
		t.Fatal(err)
	}
	if exists, err := r.BranchExists(ctx, repo, "griglia/task-1-test"); err != nil || exists {
		t.Fatalf("branch after delete=%v err=%v", exists, err)
	}
}

func TestPruneClearsStaleRegistration(t *testing.T) {
	repo := initRepo(t)
	ctx := context.Background()
	r := Runner{}
	head := runGit(t, repo, "rev-parse", "HEAD")
	path := filepath.Join(filepath.Dir(repo), "stale")
	if err := r.AddWorktree(ctx, repo, path, "griglia/task-2-stale", head); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := r.PruneWorktrees(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if registered, err := r.WorktreeRegistered(ctx, repo, path); err != nil || registered {
		t.Fatalf("registered after prune=%v err=%v", registered, err)
	}
}
