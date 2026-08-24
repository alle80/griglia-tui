// Package git shells out to the git CLI for workspace (worktree) operations.
// It is the real implementation of the application-layer GitRunner port. Per
// docs/WORKSPACES.md there is no Git library dependency: worktree semantics
// are subtle and the CLI is the compatibility surface every agent already
// has. Every operation is purely local — no fetch, pull, push, or any other
// network command is ever constructed here.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Error carries the structured outcome of a failed git invocation so later
// CLI work can map it to the stable git_error protocol code. Stderr holds the
// git diagnostic (or the spawn error when git could not run at all).
type Error struct {
	Args     []string
	ExitCode int
	Stderr   string
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = "git failed"
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), msg)
}

// Runner executes git commands in a given repository directory. It is
// stateless: every method takes the directory to operate in, so one Runner
// serves any number of projects.
type Runner struct{}

func (Runner) run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Workspace operations are local-only; a credential prompt would mean
	// something asked for the network, which must fail instead of hanging.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		gitErr := &Error{Args: args, ExitCode: -1, Stderr: strings.TrimSpace(stderr.String())}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			gitErr.ExitCode = exit.ExitCode()
		} else if gitErr.Stderr == "" {
			gitErr.Stderr = err.Error()
		}
		return "", gitErr
	}
	return stdout.String(), nil
}

// CommonDir resolves the repository's common .git directory as an absolute
// path; it doubles as the "is this a Git repository?" check.
func (r Runner) CommonDir(ctx context.Context, repoDir string) (string, error) {
	out, err := r.run(ctx, repoDir, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(out)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(repoDir, dir)
	}
	return filepath.Clean(dir), nil
}

// ResolveCommit resolves ref to a concrete commit hash. An unborn HEAD or an
// unknown ref fails, which is how repositories with nothing to base a
// worktree on are rejected.
func (r Runner) ResolveCommit(ctx context.Context, repoDir, ref string) (string, error) {
	out, err := r.run(ctx, repoDir, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// BranchExists reports whether refs/heads/<branch> exists.
func (r Runner) BranchExists(ctx context.Context, repoDir, branch string) (bool, error) {
	_, err := r.run(ctx, repoDir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	var gitErr *Error
	if errors.As(err, &gitErr) && gitErr.ExitCode == 1 {
		return false, nil
	}
	return false, err
}

// AddWorktree creates branch at commit and checks it out into a new worktree
// at path. Uncommitted state in the main checkout is never copied: git
// populates the worktree from the commit alone.
func (r Runner) AddWorktree(ctx context.Context, repoDir, path, branch, commit string) error {
	_, err := r.run(ctx, repoDir, "worktree", "add", path, "-b", branch, commit)
	return err
}

// RemoveWorktree removes a registered worktree. Without force git refuses a
// worktree with uncommitted changes; the application layer decides when force
// is allowed.
func (r Runner) RemoveWorktree(ctx context.Context, repoDir, path string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	_, err := r.run(ctx, repoDir, append(args, path)...)
	return err
}

// PruneWorktrees drops registrations whose directories are gone — the
// non-destructive repair for stale worktree metadata.
func (r Runner) PruneWorktrees(ctx context.Context, repoDir string) error {
	_, err := r.run(ctx, repoDir, "worktree", "prune")
	return err
}

// WorktreeRegistered reports whether path is currently a registered worktree
// of the repository.
func (r Runner) WorktreeRegistered(ctx context.Context, repoDir, path string) (bool, error) {
	out, err := r.run(ctx, repoDir, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	want := filepath.Clean(path)
	for _, line := range strings.Split(out, "\n") {
		if entry, ok := strings.CutPrefix(line, "worktree "); ok && filepath.Clean(entry) == want {
			return true, nil
		}
	}
	return false, nil
}

// WorktreeDirty reports whether the worktree at path has uncommitted changes
// (staged, unstaged, or untracked).
func (r Runner) WorktreeDirty(ctx context.Context, worktreePath string) (bool, error) {
	out, err := r.run(ctx, worktreePath, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// DeleteBranch force-deletes refs/heads/<branch>. Callers gate this on the
// branch being one griglia itself created; unmerged work is deliberately
// deletable because deletion is always an explicit opt-in.
func (r Runner) DeleteBranch(ctx context.Context, repoDir, branch string) error {
	_, err := r.run(ctx, repoDir, "branch", "-D", branch)
	return err
}
