package domain

import "testing"

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
