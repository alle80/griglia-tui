package domain

import (
	"fmt"
	"strings"
	"time"
)

// WorkspaceState is the persisted lifecycle of the Git workspace resource
// itself. It never records ownership or usage: whoever holds the task's
// active claim is, by derivation, the workspace's current user.
type WorkspaceState string

const (
	WorkspaceAllocating WorkspaceState = "allocating"
	WorkspaceReady      WorkspaceState = "ready"
	WorkspaceFailed     WorkspaceState = "failed"
	WorkspaceRemoved    WorkspaceState = "removed"
)

func ParseWorkspaceState(value string) (WorkspaceState, error) {
	v := WorkspaceState(value)
	switch v {
	case WorkspaceAllocating, WorkspaceReady, WorkspaceFailed, WorkspaceRemoved:
		return v, nil
	default:
		return "", ErrInvalid
	}
}

// Live reports whether the state occupies the partial unique indexes that
// guarantee at most one workspace, path, and branch per task at a time.
func (s WorkspaceState) Live() bool {
	return s == WorkspaceAllocating || s == WorkspaceReady
}

// Workspace is keyed by task. CreatedBy is audit provenance only, like
// Question.AskedBy: it is never an input to ownership checks, which always
// consult the live claims table.
type Workspace struct {
	ID         int64
	TaskID     int64
	State      WorkspaceState
	Path       string
	Branch     string
	BaseCommit string
	CreatedBy  AgentIdentity
	CreatedAt  time.Time
	UpdatedAt  time.Time
	RemovedAt  *time.Time
	Error      string
}

// MaxWorkspaceSlugLength bounds the title-derived part of a workspace branch
// name (docs/WORKSPACES.md §5.2).
const MaxWorkspaceSlugLength = 40

// WorkspaceSlug normalizes a task title for use in a Git branch name:
// lowercase, runs of non-alphanumerics collapsed to single dashes, truncated
// to MaxWorkspaceSlugLength without a trailing dash. The result contains only
// [a-z0-9-], so it is always a valid Git ref component. Determinism across a
// task's life comes from persisting the branch at allocation, not from
// recomputing this: a title edit never renames an existing branch.
func WorkspaceSlug(title string) string {
	var b strings.Builder
	pendingDash := false
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pendingDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingDash = false
			b.WriteRune(r)
			continue
		}
		pendingDash = true
	}
	slug := b.String()
	if len(slug) > MaxWorkspaceSlugLength {
		slug = strings.TrimRight(slug[:MaxWorkspaceSlugLength], "-")
	}
	return slug
}

// WorkspaceBranchName derives the managed branch for a task at allocation
// time: griglia/task-<id>-<slug>, or griglia/task-<id> when the title yields
// no slug. The griglia/ prefix namespaces managed branches away from human
// branches.
func WorkspaceBranchName(taskID int64, title string) string {
	slug := WorkspaceSlug(title)
	if slug == "" {
		return fmt.Sprintf("griglia/task-%d", taskID)
	}
	return fmt.Sprintf("griglia/task-%d-%s", taskID, slug)
}

func ValidateWorkspacePath(path string) error {
	if strings.TrimSpace(path) == "" {
		return ErrInvalid
	}
	return nil
}

func ValidateWorkspaceBranch(branch string) error {
	if strings.TrimSpace(branch) == "" {
		return ErrInvalid
	}
	return nil
}
