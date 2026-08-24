package cli

// Protocol v1 conformance for the workspace commands: exact DTO field sets,
// nullability, absolute launcher paths, one-document stdout, and the pinned
// guarantee that this slice leaves the Task DTO untouched
// (docs/WORKSPACES.md §10).

import (
	"path/filepath"
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
