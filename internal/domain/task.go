package domain

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
)

func NewUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

const MaxTitleLength = 500

var (
	ErrInvalid  = errors.New("invalid input")
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

type Lifecycle string

const (
	LifecycleBacklog   Lifecycle = "backlog"
	LifecycleReady     Lifecycle = "ready"
	LifecycleDone      Lifecycle = "done"
	LifecycleCancelled Lifecycle = "cancelled"
)

func ParseLifecycle(value string) (Lifecycle, error) {
	v := Lifecycle(value)
	switch v {
	case LifecycleBacklog, LifecycleReady, LifecycleDone, LifecycleCancelled:
		return v, nil
	default:
		return "", ErrInvalid
	}
}

type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
	PriorityUrgent Priority = "urgent"
)

func ParsePriority(value string) (Priority, error) {
	v := Priority(value)
	switch v {
	case PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent:
		return v, nil
	default:
		return "", ErrInvalid
	}
}

type Task struct {
	ID                int64
	UID               string
	Title             string
	Description       string
	Lifecycle         Lifecycle
	Priority          Priority
	Progress          int
	Phase             string
	CompletionSummary string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	CompletedAt       *time.Time
	CancelledAt       *time.Time
	Version           int64
}

func ValidateTitle(title string) error {
	if strings.TrimSpace(title) == "" || len([]rune(title)) > MaxTitleLength {
		return ErrInvalid
	}
	return nil
}
