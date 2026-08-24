package domain

import (
	"strings"
	"testing"
)

func TestWorkspaceSlugNormalizesForGit(t *testing.T) {
	cases := []struct{ title, want string }{
		{"Fix paste handling", "fix-paste-handling"},
		{"  Weird -- punctuation!! (v2) ", "weird-punctuation-v2"},
		{"UPPER case 123", "upper-case-123"},
		{"àccénts and émoji 🎉", "cc-nts-and-moji"},
		{"!!!", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := WorkspaceSlug(c.title); got != c.want {
			t.Errorf("WorkspaceSlug(%q)=%q want %q", c.title, got, c.want)
		}
	}
}

func TestWorkspaceSlugTruncatesWithoutTrailingDash(t *testing.T) {
	slug := WorkspaceSlug(strings.Repeat("word ", 20))
	if len(slug) > MaxWorkspaceSlugLength {
		t.Fatalf("slug %q longer than %d", slug, MaxWorkspaceSlugLength)
	}
	if strings.HasSuffix(slug, "-") || strings.HasPrefix(slug, "-") {
		t.Fatalf("slug %q has leading/trailing dash", slug)
	}
	for _, r := range slug {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			t.Fatalf("slug %q contains invalid rune %q", slug, r)
		}
	}
}

func TestWorkspaceBranchName(t *testing.T) {
	if got := WorkspaceBranchName(7, "Fix paste handling"); got != "griglia/task-7-fix-paste-handling" {
		t.Fatalf("branch=%q", got)
	}
	if got := WorkspaceBranchName(9, "???"); got != "griglia/task-9" {
		t.Fatalf("branch without slug=%q", got)
	}
}
