package domain

import "time"

// DependencyView is a direct prerequisite edge joined with the facts needed
// to decide satisfaction. A prerequisite is satisfied only when it is done;
// cancelled prerequisites remain unsatisfied.
type DependencyView struct {
	TaskID          int64
	DependsOnTaskID int64
	Title           string
	Lifecycle       Lifecycle
	CreatedAt       time.Time
}

func (d DependencyView) Satisfied() bool { return d.Lifecycle == LifecycleDone }
