package cli

// Workspace CLI integration tests run against real temporary Git repositories
// and SQLite databases, exercising the full binary surface: command wiring,
// project targeting, derived usage, authorization, and stable error mapping.
// Protocol (DTO/envelope) conformance for the same commands is pinned in
// workspace_protocol_test.go.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/alle80/griglia-tui/internal/domain"
	grsqlite "github.com/alle80/griglia-tui/internal/sqlite"
)

func wsGit(t *testing.T, dir string, args ...string) string {
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

// initWorkspaceProject builds the round-2 layout: a Git repository with one
// commit and an initialized Griglia project, inside a parent directory that
// will also receive the sibling .griglia-worktrees root.
func initWorkspaceProject(t *testing.T) (root, parent string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	parent = t.TempDir()
	root = filepath.Join(parent, "proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	wsGit(t, root, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(root, "main.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wsGit(t, root, "add", ".")
	wsGit(t, root, "commit", "--quiet", "-m", "initial")
	if code, _, stderr := run(t, root, "init"); code != 0 {
		t.Fatalf("init failed: %d %q", code, stderr)
	}
	return root, parent
}

func addClaimedReadyTask(t *testing.T, root, title, agent, instance string) {
	t.Helper()
	if code, _, _ := run(t, root, "task", "add", title, "--lifecycle", "ready"); code != 0 {
		t.Fatal(code)
	}
	if code, _, stderr := run(t, root, "task", "claim-next", "--agent", agent, "--instance", instance); code != 0 {
		t.Fatalf("claim failed: %d %q", code, stderr)
	}
}

func TestWorkspaceLifecycleCLI(t *testing.T) {
	root, parent := initWorkspaceProject(t)
	agent := []string{"--agent", "codex", "--instance", "one"}
	addClaimedReadyTask(t, root, "Build Feature", "codex", "one")
	head := wsGit(t, root, "rev-parse", "HEAD")

	code, out, stderr := run(t, root, append([]string{"workspace", "create", "1"}, agent...)...)
	if code != 0 || stderr != "" || !strings.Contains(out, "Workspace for task #1 is ready") {
		t.Fatalf("create: %d %q %q", code, out, stderr)
	}
	wantPath := filepath.Join(parent, ".griglia-worktrees", "proj", "task-1")
	if !strings.Contains(out, wantPath) || !strings.Contains(out, "griglia/task-1-build-feature") || !strings.Contains(out, head) {
		t.Fatalf("create output=%q", out)
	}
	if got := wsGit(t, wantPath, "rev-parse", "--abbrev-ref", "HEAD"); got != "griglia/task-1-build-feature" {
		t.Fatalf("checked-out branch=%q", got)
	}

	// Idempotent reuse: the same workspace comes back, no second worktree.
	code, out, stderr = run(t, root, append([]string{"workspace", "create", "1"}, agent...)...)
	if code != 0 || stderr != "" || !strings.Contains(out, wantPath) {
		t.Fatalf("idempotent create: %d %q %q", code, out, stderr)
	}
	if worktrees := strings.Count(wsGit(t, root, "worktree", "list", "--porcelain"), "worktree "); worktrees != 2 {
		t.Fatalf("worktrees=%d", worktrees)
	}

	code, out, stderr = run(t, root, "workspace", "show", "1")
	if code != 0 || stderr != "" || !strings.Contains(out, "State: ready") || !strings.Contains(out, "Usage: in_use (codex/one)") {
		t.Fatalf("show: %d %q %q", code, out, stderr)
	}
	code, out, stderr = run(t, root, "workspace", "list")
	if code != 0 || stderr != "" || !strings.Contains(out, "griglia/task-1-build-feature") || !strings.Contains(out, "in_use") {
		t.Fatalf("list: %d %q %q", code, out, stderr)
	}

	// Usage is derived from the claims table: release parks the workspace,
	// a later claim by a different instance resumes it — no workspace write.
	if code, _, _ = run(t, root, append([]string{"task", "release", "1"}, agent...)...); code != 0 {
		t.Fatal(code)
	}
	code, out, stderr = run(t, root, "workspace", "show", "1")
	if code != 0 || stderr != "" || !strings.Contains(out, "Usage: idle") {
		t.Fatalf("idle show: %d %q %q", code, out, stderr)
	}
	if code, _, _ = run(t, root, "task", "claim", "1", "--agent", "claude", "--instance", "two"); code != 0 {
		t.Fatal(code)
	}
	code, out, stderr = run(t, root, "workspace", "show", "1")
	if code != 0 || stderr != "" || !strings.Contains(out, "Usage: in_use (claude/two)") {
		t.Fatalf("reclaimed show: %d %q %q", code, out, stderr)
	}

	// Idle removal is human-operable: release first, then remove without
	// identity. The branch is kept by default.
	if code, _, _ = run(t, root, "task", "release", "1", "--agent", "claude", "--instance", "two"); code != 0 {
		t.Fatal(code)
	}
	code, out, stderr = run(t, root, "workspace", "remove", "1")
	if code != 0 || stderr != "" || !strings.Contains(out, "Removed workspace for task #1") {
		t.Fatalf("remove: %d %q %q", code, out, stderr)
	}
	if _, err := os.Stat(wantPath); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
	if out = wsGit(t, root, "branch", "--list", "griglia/task-1-build-feature"); out == "" {
		t.Fatal("branch must be kept by default")
	}
	code, _, stderr = run(t, root, "workspace", "show", "1")
	if code != 4 || !strings.Contains(stderr, "no workspace") {
		t.Fatalf("show after remove: %d %q", code, stderr)
	}
	code, out, stderr = run(t, root, "workspace", "list")
	if code != 0 || stderr != "" || out != "No workspaces.\n" {
		t.Fatalf("empty list: %d %q %q", code, out, stderr)
	}
}

func TestWorkspaceCreateExplicitBase(t *testing.T) {
	root, _ := initWorkspaceProject(t)
	first := wsGit(t, root, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(root, "main.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wsGit(t, root, "commit", "--quiet", "-am", "second")
	addClaimedReadyTask(t, root, "explicit base", "codex", "one")

	code, data, _ := runJSON(t, root, "workspace", "create", "1", "--agent", "codex", "--instance", "one", "--base", first)
	if code != 0 {
		t.Fatal(code)
	}
	workspace := data["workspace"].(map[string]any)
	if workspace["base_commit"] != first {
		t.Fatalf("base_commit=%v want %v", workspace["base_commit"], first)
	}
	content, err := os.ReadFile(filepath.Join(workspace["path"].(string), "main.txt"))
	if err != nil || string(content) != "v1\n" {
		t.Fatalf("worktree content=%q err=%v", content, err)
	}
}

func TestWorkspaceRemoveAuthorizationAndBranchDeletion(t *testing.T) {
	root, _ := initWorkspaceProject(t)
	agent := []string{"--agent", "codex", "--instance", "one"}
	addClaimedReadyTask(t, root, "guarded", "codex", "one")
	if code, _, _ := run(t, root, append([]string{"workspace", "create", "1"}, agent...)...); code != 0 {
		t.Fatal(code)
	}

	// In use: anonymous and foreign-identity removal are conflicts.
	code, out, stderr := run(t, root, "workspace", "remove", "1", "--json")
	if code != 5 || stderr != "" || !strings.Contains(out, `"code":"conflict"`) {
		t.Fatalf("anonymous remove: %d %q %q", code, out, stderr)
	}
	code, out, _ = run(t, root, "workspace", "remove", "1", "--agent", "claude", "--instance", "other", "--json")
	if code != 5 || !strings.Contains(out, `"code":"conflict"`) {
		t.Fatalf("foreign remove: %d %q", code, out)
	}

	// A dirty worktree refuses non-forced removal even for the owner.
	worktree := filepath.Join(filepath.Dir(root), ".griglia-worktrees", "proj", "task-1")
	if err := os.WriteFile(filepath.Join(worktree, "wip.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ = run(t, root, append([]string{"workspace", "remove", "1", "--json"}, agent...)...)
	if code != 5 || !strings.Contains(out, "uncommitted") {
		t.Fatalf("dirty remove: %d %q", code, out)
	}

	// Force is the explicit human override: discards the dirt, bypasses the
	// ownership check, and --delete-branch removes the managed branch too.
	code, out, stderr = run(t, root, "workspace", "remove", "1", "--force", "--delete-branch")
	if code != 0 || stderr != "" || !strings.Contains(out, "Removed workspace") {
		t.Fatalf("forced remove: %d %q %q", code, out, stderr)
	}
	if out = wsGit(t, root, "branch", "--list", "griglia/task-1-guarded"); out != "" {
		t.Fatalf("branch still exists: %q", out)
	}
}

func TestWorkspaceErrorMapping(t *testing.T) {
	root, _ := initWorkspaceProject(t)
	addClaimedReadyTask(t, root, "errors", "codex", "one")
	if code, _, _ := run(t, root, "task", "add", "unclaimed", "--lifecycle", "ready"); code != 0 {
		t.Fatal(code)
	}
	// A branch griglia has no record of is never adopted.
	wsGit(t, root, "branch", "griglia/task-1-errors")

	nonGit := t.TempDir()
	if code, _, _ := run(t, nonGit, "init"); code != 0 {
		t.Fatal(code)
	}

	for _, tc := range []struct {
		name string
		dir  string
		args []string
		code int
		kind string
	}{
		{"missing identity", root, []string{"workspace", "create", "1"}, 2, "invalid_input"},
		{"invalid task id", root, []string{"workspace", "create", "zero", "--agent", "codex", "--instance", "one"}, 2, "invalid_input"},
		{"bad base ref", root, []string{"workspace", "create", "1", "--agent", "codex", "--instance", "one", "--base", "no-such-ref"}, 2, "invalid_input"},
		{"subcommand required", root, []string{"workspace"}, 2, "invalid_input"},
		{"unknown subcommand", root, []string{"workspace", "explode"}, 2, "invalid_input"},
		{"task not found", root, []string{"workspace", "create", "99", "--agent", "codex", "--instance", "one"}, 4, "not_found"},
		{"show task not found", root, []string{"workspace", "show", "99"}, 4, "not_found"},
		{"show no workspace", root, []string{"workspace", "show", "2"}, 4, "not_found"},
		{"remove no workspace", root, []string{"workspace", "remove", "2"}, 4, "not_found"},
		{"non-owner create", root, []string{"workspace", "create", "1", "--agent", "claude", "--instance", "other"}, 5, "conflict"},
		{"unclaimed create", root, []string{"workspace", "create", "2", "--agent", "codex", "--instance", "one"}, 5, "conflict"},
		{"foreign branch", root, []string{"workspace", "create", "1", "--agent", "codex", "--instance", "one"}, 5, "conflict"},
		{"non-git create", nonGit, []string{"workspace", "create", "1", "--agent", "codex", "--instance", "one"}, 1, "git_error"},
		{"non-git list", nonGit, []string{"workspace", "list"}, 1, "git_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, errObj := runJSON(t, tc.dir, tc.args...)
			if code != tc.code || errObj["code"] != tc.kind {
				t.Fatalf("code=%d err=%v want %d/%s", code, errObj, tc.code, tc.kind)
			}
		})
	}
}

// TestWorkspaceFailedAllocationIsVisible pins the read-model decision: with no
// live workspace, show and list surface the latest failed row — its recorded
// error is what makes a failed allocation diagnosable — while removed rows
// stay history.
func TestWorkspaceFailedAllocationIsVisible(t *testing.T) {
	root, _ := initWorkspaceProject(t)
	addClaimedReadyTask(t, root, "flaky", "codex", "one")

	store, err := grsqlite.Open(filepath.Join(root, ".griglia", "griglia.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	identity := domain.AgentIdentity{AgentName: "codex", InstanceID: "one"}
	w, err := store.ReserveWorkspace(ctx, 1, filepath.Join(t.TempDir(), "task-1"), "griglia/task-1-flaky", "abc", identity, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.MarkWorkspaceFailed(ctx, w.ID, "simulated git failure", now); err != nil {
		t.Fatal(err)
	}

	code, data, _ := runJSON(t, root, "workspace", "show", "1")
	if code != 0 {
		t.Fatal(code)
	}
	workspace := data["workspace"].(map[string]any)
	if workspace["state"] != "failed" || workspace["error"] != "simulated git failure" {
		t.Fatalf("failed workspace=%v", workspace)
	}
	code, data, _ = runJSON(t, root, "workspace", "list")
	if code != 0 || len(data["workspaces"].([]any)) != 1 {
		t.Fatalf("list=%v", data)
	}
	code, out, stderr := run(t, root, "workspace", "show", "1")
	if code != 0 || stderr != "" || !strings.Contains(out, "Error: simulated git failure") {
		t.Fatalf("human failed show: %d %q %q", code, out, stderr)
	}

	// Removing the failed row clears it from the read model.
	if code, _, _ = run(t, root, append([]string{"workspace", "remove", "1"}, "--agent", "codex", "--instance", "one")...); code != 0 {
		t.Fatal(code)
	}
	code, _, errObj := runJSON(t, root, "workspace", "show", "1")
	if code != 4 || errObj["code"] != "not_found" {
		t.Fatalf("show after remove: %d %v", code, errObj)
	}
}

// TestWorkspaceProjectTargeting covers the recommended isolated-agent mode:
// the board is addressed explicitly with --project or GRIGLIA_PROJECT from
// outside the main checkout (including from inside the allocated worktree),
// with --project taking precedence and upward discovery unchanged.
func TestWorkspaceProjectTargeting(t *testing.T) {
	root, parent := initWorkspaceProject(t)
	addClaimedReadyTask(t, root, "targeted", "codex", "one")
	outside := t.TempDir()

	code, data, _ := runJSON(t, outside, "--project", root, "workspace", "create", "1", "--agent", "codex", "--instance", "one")
	if code != 0 {
		t.Fatal(code)
	}
	workspace := data["workspace"].(map[string]any)
	if workspace["project_root"] != root || workspace["database"] != filepath.Join(root, ".griglia", "griglia.db") {
		t.Fatalf("launcher facts=%v", workspace)
	}
	worktree := workspace["path"].(string)
	if !strings.HasPrefix(worktree, filepath.Join(parent, ".griglia-worktrees")) {
		t.Fatalf("worktree path=%q", worktree)
	}

	// GRIGLIA_PROJECT pins the board for an agent whose cwd is the isolated
	// worktree — upward discovery cannot find it from there.
	code, _, errObj := runJSON(t, worktree, "workspace", "show", "1")
	if code != 3 || errObj["code"] != "project_not_initialized" {
		t.Fatalf("discovery from worktree must fail: %d %v", code, errObj)
	}
	t.Setenv("GRIGLIA_PROJECT", root)
	code, data, _ = runJSON(t, worktree, "workspace", "show", "1")
	if code != 0 || data["workspace"].(map[string]any)["path"] != worktree {
		t.Fatalf("pinned show: %d %v", code, data)
	}
	code, data, _ = runJSON(t, worktree, "task", "show", "1")
	if code != 0 || data["task"].(map[string]any)["title"] != "targeted" {
		t.Fatalf("pinned task show: %d %v", code, data)
	}

	// --project wins over a bogus GRIGLIA_PROJECT.
	t.Setenv("GRIGLIA_PROJECT", filepath.Join(outside, "nowhere"))
	code, _, errObj = runJSON(t, outside, "workspace", "list")
	if code != 3 || errObj["code"] != "project_not_initialized" {
		t.Fatalf("bogus env must fail: %d %v", code, errObj)
	}
	code, data, _ = runJSON(t, outside, "--project", root, "workspace", "list")
	if code != 0 || len(data["workspaces"].([]any)) != 1 {
		t.Fatalf("--project precedence: %d %v", code, data)
	}
	t.Setenv("GRIGLIA_PROJECT", "")

	// Legacy upward discovery still works from inside the main checkout.
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	code, data, _ = runJSON(t, nested, "workspace", "list")
	if code != 0 || len(data["workspaces"].([]any)) != 1 {
		t.Fatalf("upward discovery: %d %v", code, data)
	}
}

// TestWorkspaceCLINoNetworkGitCommands intercepts every git invocation of a
// full CLI create/remove cycle through a PATH shim and asserts none is a
// network operation. The repository has no remote, which also proves none is
// required.
func TestWorkspaceCLINoNetworkGitCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim requires a POSIX shell")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary not available")
	}
	root, _ := initWorkspaceProject(t)
	addClaimedReadyTask(t, root, "offline", "codex", "one")

	shimDir := t.TempDir()
	logPath := filepath.Join(shimDir, "git.log")
	shim := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$GIT_SHIM_LOG\"\nexec \"$REAL_GIT\" \"$@\"\n"
	if err = os.WriteFile(filepath.Join(shimDir, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAL_GIT", realGit)
	t.Setenv("GIT_SHIM_LOG", logPath)
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if code, _, stderr := run(t, root, "workspace", "create", "1", "--agent", "codex", "--instance", "one"); code != 0 {
		t.Fatalf("create: %d %q", code, stderr)
	}
	if code, _, stderr := run(t, root, "workspace", "remove", "1", "--agent", "codex", "--instance", "one", "--delete-branch"); code != 0 {
		t.Fatalf("remove: %d %q", code, stderr)
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
