package buildinfo

import (
	"runtime/debug"
	"testing"
)

func info(mainVersion string, settings ...debug.BuildSetting) *debug.BuildInfo {
	return &debug.BuildInfo{Main: debug.Module{Path: "github.com/alle80/griglia-tui", Version: mainVersion}, Settings: settings}
}

func setting(key, value string) debug.BuildSetting {
	return debug.BuildSetting{Key: key, Value: value}
}

func TestResolve(t *testing.T) {
	vcs := []debug.BuildSetting{
		setting("vcs.revision", "abc123def456"),
		setting("vcs.time", "2026-08-23T21:00:00Z"),
		setting("vcs.modified", "false"),
	}
	for _, tc := range []struct {
		name                              string
		version, commit, date             string
		info                              *debug.BuildInfo
		wantVersion, wantCommit, wantDate string
	}{
		{
			name:    "release build keeps injected metadata",
			version: "0.2.0", commit: "deadbeef", date: "2026-08-23T20:00:00Z",
			info:        info("v0.1.0", vcs...),
			wantVersion: "0.2.0", wantCommit: "deadbeef", wantDate: "2026-08-23T20:00:00Z",
		},
		{
			name:    "go install of a tagged version reports it without inventing vcs data",
			version: DefaultVersion, commit: DefaultCommit, date: DefaultDate,
			info:        info("v0.1.0"),
			wantVersion: "0.1.0", wantCommit: DefaultCommit, wantDate: DefaultDate,
		},
		{
			name:    "go install of a pseudo-version keeps the pseudo-version",
			version: DefaultVersion, commit: DefaultCommit, date: DefaultDate,
			info:        info("v0.1.1-0.20260823210000-abc123def456"),
			wantVersion: "0.1.1-0.20260823210000-abc123def456", wantCommit: DefaultCommit, wantDate: DefaultDate,
		},
		{
			name:    "vcs-stamped local build fills commit and date",
			version: DefaultVersion, commit: DefaultCommit, date: DefaultDate,
			info:        info("(devel)", vcs...),
			wantVersion: DefaultVersion, wantCommit: "abc123def456", wantDate: "2026-08-23T21:00:00Z",
		},
		{
			name:    "modified checkout marks the commit dirty",
			version: DefaultVersion, commit: DefaultCommit, date: DefaultDate,
			info: info("(devel)",
				setting("vcs.revision", "abc123def456"),
				setting("vcs.modified", "true")),
			wantVersion: DefaultVersion, wantCommit: "abc123def456+dirty", wantDate: DefaultDate,
		},
		{
			name:    "empty strings count as not injected",
			version: "", commit: "", date: "",
			info:        info("v1.2.3", vcs...),
			wantVersion: "1.2.3", wantCommit: "abc123def456", wantDate: "2026-08-23T21:00:00Z",
		},
		{
			name:    "no build info leaves defaults untouched",
			version: DefaultVersion, commit: DefaultCommit, date: DefaultDate,
			info:        nil,
			wantVersion: DefaultVersion, wantCommit: DefaultCommit, wantDate: DefaultDate,
		},
		{
			name:    "devel and empty module versions stamp nothing",
			version: DefaultVersion, commit: DefaultCommit, date: DefaultDate,
			info:        info(""),
			wantVersion: DefaultVersion, wantCommit: DefaultCommit, wantDate: DefaultDate,
		},
		{
			name:    "non-semver module version is reported verbatim",
			version: DefaultVersion, commit: DefaultCommit, date: DefaultDate,
			info:        info("volatile"),
			wantVersion: "volatile", wantCommit: DefaultCommit, wantDate: DefaultDate,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version, commit, date := resolve(tc.version, tc.commit, tc.date, tc.info)
			if version != tc.wantVersion || commit != tc.wantCommit || date != tc.wantDate {
				t.Fatalf("resolve() = (%q, %q, %q), want (%q, %q, %q)",
					version, commit, date, tc.wantVersion, tc.wantCommit, tc.wantDate)
			}
		})
	}
}

// The real binary is built inside a module, so Resolve must at least keep
// injected values intact and never panic without them.
func TestResolveWithRealBuildInfo(t *testing.T) {
	version, commit, date := Resolve("9.9.9", "cafe", "2026-01-01T00:00:00Z")
	if version != "9.9.9" || commit != "cafe" || date != "2026-01-01T00:00:00Z" {
		t.Fatalf("injected metadata was rewritten: (%q, %q, %q)", version, commit, date)
	}
	if v, c, d := Resolve(DefaultVersion, DefaultCommit, DefaultDate); v == "" || c == "" || d == "" {
		t.Fatalf("fallback produced empty fields: (%q, %q, %q)", v, c, d)
	}
}
