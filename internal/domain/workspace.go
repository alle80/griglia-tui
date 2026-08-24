package domain

import (
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
