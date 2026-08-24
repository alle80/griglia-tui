package cli

// Protocol v1 conformance for the workspace commands: exact DTO field sets,
// nullability, absolute launcher paths, one-document stdout, and the pinned
// guarantee that this slice leaves the Task DTO untouched
// (docs/WORKSPACES.md §10).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func assertWorkspaceDTO(t *testing.T, workspace map[string]any) {
	t.Helper()
	assertExactKeys(t, "workspace", workspace,
		"task_id", "state", "usage", "active_claim", "path", "branch",
		"base_commit", "created_by", "project_root", "database",
		"git_common_dir", "created_at", "updated_at", "error")
	assertExactKeys(t, "created_by", workspace["created_by"].(map[string]any), "agent_name", "instance_id")
	if _, isNumber := workspace["task_id"].(float64); !isNumber {
		t.Fatalf("task_id is not a number: %v", workspace["task_id"])
	}
	switch workspace["state"] {
	case "allocating", "ready", "failed", "removed":
	default:
		t.Fatalf("state=%v", workspace["state"])
	}
	switch workspace["usage"] {
	case "in_use", "idle":
	default:
		t.Fatalf("usage=%v", workspace["usage"])
	}
	for _, field := range []string{"created_at", "updated_at"} {
		if !protocolTimeRE.MatchString(workspace[field].(string)) {
			t.Fatalf("%s=%v", field, workspace[field])
		}
	}
	for _, field := range []string{"path", "project_root", "database", "git_common_dir"} {
		if !filepath.IsAbs(workspace[field].(string)) {
			t.Fatalf("%s must be an absolute path: %v", field, workspace[field])
		}
	}
	if claim, present := workspace["active_claim"].(map[string]any); present {
		assertClaimDTO(t, claim)
	}
}

func TestProtocolWorkspaceDTOs(t *testing.T) {
	root, parent := initWorkspaceProject(t)
	addClaimedReadyTask(t, root, "Proto Workspace", "codex", "one")

	code, data, _ := runJSON(t, root, "workspace", "create", "1", "--agent", "codex", "--instance", "one")
	if code != 0 {
		t.Fatal(code)
	}
	assertExactKeys(t, "create", data, "workspace")
	workspace := data["workspace"].(map[string]any)
	assertWorkspaceDTO(t, workspace)
	if workspace["state"] != "ready" || workspace["usage"] != "in_use" || workspace["error"] != "" {
		t.Fatalf("created workspace=%v", workspace)
	}
	if workspace["branch"] != "griglia/task-1-proto-workspace" {
		t.Fatalf("branch=%v", workspace["branch"])
	}
	if workspace["path"] != filepath.Join(parent, ".griglia-worktrees", "proj", "task-1") {
		t.Fatalf("path=%v", workspace["path"])
	}
	if workspace["project_root"] != root || workspace["database"] != filepath.Join(root, ".griglia", "griglia.db") || workspace["git_common_dir"] != filepath.Join(root, ".git") {
		t.Fatalf("launcher facts=%v", workspace)
	}
	claim := workspace["active_claim"].(map[string]any)
	if claim["agent_name"] != "codex" || claim["instance_id"] != "one" {
		t.Fatalf("active_claim=%v", claim)
	}
	createdBy := workspace["created_by"].(map[string]any)
	if createdBy["agent_name"] != "codex" || createdBy["instance_id"] != "one" {
		t.Fatalf("created_by=%v", createdBy)
	}

	// This slice must not grow the Task DTO: the exact-key assertion proves
	// no workspace field leaked into the core task protocol.
	code, data, _ = runJSON(t, root, "task", "show", "1")
	if code != 0 {
		t.Fatal(code)
	}
	assertTaskDTO(t, data["task"].(map[string]any))

	code, data, _ = runJSON(t, root, "workspace", "show", "1")
	if code != 0 {
		t.Fatal(code)
	}
	assertExactKeys(t, "show", data, "workspace")
	assertWorkspaceDTO(t, data["workspace"].(map[string]any))

	code, data, _ = runJSON(t, root, "workspace", "list")
	if code != 0 {
		t.Fatal(code)
	}
	assertExactKeys(t, "list", data, "workspaces")
	workspaces := data["workspaces"].([]any)
	if len(workspaces) != 1 {
		t.Fatalf("workspaces=%v", workspaces)
	}
	assertWorkspaceDTO(t, workspaces[0].(map[string]any))

	// After release the derived fields flip: usage idle, active_claim an
	// explicit null — never an omitted key.
	if code, _, _ = runJSON(t, root, "task", "release", "1", "--agent", "codex", "--instance", "one"); code != 0 {
		t.Fatal(code)
	}
	code, data, _ = runJSON(t, root, "workspace", "show", "1")
	if code != 0 {
		t.Fatal(code)
	}
	workspace = data["workspace"].(map[string]any)
	assertWorkspaceDTO(t, workspace)
	if workspace["usage"] != "idle" || workspace["active_claim"] != nil {
		t.Fatalf("idle workspace=%v", workspace)
	}

	code, data, _ = runJSON(t, root, "workspace", "remove", "1")
	if code != 0 {
		t.Fatal(code)
	}
	assertExactKeys(t, "remove", data, "workspace")
	workspace = data["workspace"].(map[string]any)
	assertWorkspaceDTO(t, workspace)
	if workspace["state"] != "removed" {
		t.Fatalf("removed workspace=%v", workspace)
	}

	code, data, _ = runJSON(t, root, "workspace", "list")
	if code != 0 || len(data["workspaces"].([]any)) != 0 {
		t.Fatalf("workspaces must be an empty array, got %v", data["workspaces"])
	}
}

// failingGitShim prepends a git wrapper to PATH that fails with exit 1 on any
// invocation whose arguments match the shell case pattern and delegates every
// other invocation to the real binary — the only way to force a specific git
// step to fail through the full CLI surface.
func failingGitShim(t *testing.T, pattern string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim requires a POSIX shell")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary not available")
	}
	shimDir := t.TempDir()
	shim := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\n  %s) echo 'simulated git failure' >&2; exit 1;;\nesac\nexec %q \"$@\"\n", pattern, realGit)
	if err = os.WriteFile(filepath.Join(shimDir, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func createdWorkspaceProject(t *testing.T, title string) string {
	t.Helper()
	root, _ := initWorkspaceProject(t)
	addClaimedReadyTask(t, root, title, "codex", "one")
	if code, _, _ := runJSON(t, root, "workspace", "create", "1", "--agent", "codex", "--instance", "one"); code != 0 {
		t.Fatal(code)
	}
	return root
}

// TestProtocolWorkspaceRemoveFailures pins the two distinguishable removal
// failure shapes (PROTOCOL.md): a failure before the worktree is destroyed is
// a plain error envelope (data null, workspace still live), while a failure
// of post-removal cleanup carries the removed workspace as data — the only
// error envelope with a payload — so callers can see the destructive step
// already happened and the row is genuinely removed.
func TestProtocolWorkspaceRemoveFailures(t *testing.T) {
	agent := []string{"--agent", "codex", "--instance", "one"}

	t.Run("failure before destruction keeps the workspace live", func(t *testing.T) {
		root := createdWorkspaceProject(t, "predestruction")
		failingGitShim(t, `"worktree remove"*`)
		code, data, errObj := runJSON(t, root, append([]string{"workspace", "remove", "1"}, agent...)...)
		if code != 1 || errObj["code"] != "git_error" {
			t.Fatalf("code=%d err=%v", code, errObj)
		}
		if data != nil {
			t.Fatalf("pre-destruction failure must not carry a payload: %v", data)
		}
		code, data, _ = runJSON(t, root, "workspace", "show", "1")
		if code != 0 || data["workspace"].(map[string]any)["state"] != "ready" {
			t.Fatalf("workspace must remain live: %d %v", code, data)
		}
	})

	t.Run("prune failure after removal carries the removed workspace", func(t *testing.T) {
		root := createdWorkspaceProject(t, "prune fails")
		failingGitShim(t, `"worktree prune"*`)
		code, data, errObj := runJSON(t, root, append([]string{"workspace", "remove", "1"}, agent...)...)
		if code != 1 || errObj["code"] != "git_error" {
			t.Fatalf("code=%d err=%v", code, errObj)
		}
		if !strings.HasPrefix(errObj["message"].(string), "workspace removed, but post-removal cleanup failed") {
			t.Fatalf("message=%v", errObj["message"])
		}
		assertExactKeys(t, "partial-success data", data, "workspace")
		workspace := data["workspace"].(map[string]any)
		assertWorkspaceDTO(t, workspace)
		if workspace["state"] != "removed" {
			t.Fatalf("workspace=%v", workspace)
		}
		// The row is genuinely removed: the read model no longer serves it.
		code, _, errObj = runJSON(t, root, "workspace", "show", "1")
		if code != 4 || errObj["code"] != "not_found" {
			t.Fatalf("show after removal: %d %v", code, errObj)
		}
	})

	t.Run("branch deletion failure after removal carries the removed workspace", func(t *testing.T) {
		root := createdWorkspaceProject(t, "branch delete fails")
		failingGitShim(t, `"branch -D"*`)
		code, data, errObj := runJSON(t, root, append([]string{"workspace", "remove", "1", "--delete-branch"}, agent...)...)
		if code != 1 || errObj["code"] != "git_error" {
			t.Fatalf("code=%d err=%v", code, errObj)
		}
		if !strings.HasPrefix(errObj["message"].(string), "workspace removed, but post-removal cleanup failed") {
			t.Fatalf("message=%v", errObj["message"])
		}
		workspace := data["workspace"].(map[string]any)
		assertWorkspaceDTO(t, workspace)
		if workspace["state"] != "removed" {
			t.Fatalf("workspace=%v", workspace)
		}
		// The failed deletion left the branch behind — resource reality the
		// caller can still act on.
		if out := wsGit(t, root, "branch", "--list", "griglia/task-1-branch-delete-fails"); out == "" {
			t.Fatal("branch should still exist after the failed deletion")
		}
	})

	t.Run("human output states the removal happened", func(t *testing.T) {
		root := createdWorkspaceProject(t, "human cleanup")
		failingGitShim(t, `"worktree prune"*`)
		code, out, stderr := run(t, root, append([]string{"workspace", "remove", "1"}, agent...)...)
		if code != 1 || out != "" {
			t.Fatalf("code=%d stdout=%q", code, out)
		}
		if !strings.Contains(stderr, "workspace removed, but post-removal cleanup failed") {
			t.Fatalf("stderr=%q", stderr)
		}
	})
}
