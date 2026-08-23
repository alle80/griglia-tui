package domain

import (
	"errors"
	"testing"
)

func TestValidateTitle(t *testing.T) {
	for _, tc := range []struct {
		title string
		valid bool
	}{{"task", true}, {"", false}, {"   ", false}} {
		if got := ValidateTitle(tc.title) == nil; got != tc.valid {
			t.Errorf("ValidateTitle(%q) valid=%v, want %v", tc.title, got, tc.valid)
		}
	}
}

func TestLifecycleTransitions(t *testing.T) {
	valid := [][2]Lifecycle{{LifecycleBacklog, LifecycleReady}, {LifecycleReady, LifecycleDone}, {LifecycleBacklog, LifecycleCancelled}, {LifecycleReady, LifecycleCancelled}}
	for _, pair := range valid {
		if err := ValidateTransition(pair[0], pair[1]); err != nil {
			t.Errorf("%s -> %s: %v", pair[0], pair[1], err)
		}
	}
	invalid := [][2]Lifecycle{{LifecycleBacklog, LifecycleDone}, {LifecycleDone, LifecycleReady}, {LifecycleCancelled, LifecycleReady}, {LifecycleReady, LifecycleReady}}
	for _, pair := range invalid {
		if err := ValidateTransition(pair[0], pair[1]); !errors.Is(err, ErrConflict) {
			t.Errorf("%s -> %s err=%v", pair[0], pair[1], err)
		}
	}
}
