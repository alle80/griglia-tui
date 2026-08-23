// Package buildinfo resolves the metadata reported by griglia version.
// Values injected at link time by release builds always win; when they are
// absent it falls back to the module and VCS information the Go toolchain
// embeds in every module-aware binary, so go install module@version reports
// the real version. Nothing here ever runs Git or invents missing data.
package buildinfo

import "runtime/debug"

// Defaults left in place when no metadata was injected at link time. The
// resolver treats them (and empty strings) as "not injected".
const (
	DefaultVersion = "dev"
	DefaultCommit  = "unknown"
	DefaultDate    = "unknown"
)

// Resolve returns the version, commit, and build date to report, filling
// non-injected fields from the binary's embedded build information.
func Resolve(version, commit, date string) (string, string, string) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		info = nil
	}
	return resolve(version, commit, date, info)
}

func resolve(version, commit, date string, info *debug.BuildInfo) (string, string, string) {
	if info == nil {
		return version, commit, date
	}
	if version == "" || version == DefaultVersion {
		if v := normalizeVersion(info.Main.Version); v != "" {
			version = v
		}
	}
	revision, vcsTime, dirty := vcsSettings(info)
	if (commit == "" || commit == DefaultCommit) && revision != "" {
		commit = revision
		if dirty {
			commit += "+dirty"
		}
	}
	if (date == "" || date == DefaultDate) && vcsTime != "" {
		date = vcsTime
	}
	return version, commit, date
}

// normalizeVersion maps a module version to the X.Y.Z form release builds
// inject. "(devel)" and empty mean the toolchain had nothing to stamp.
func normalizeVersion(v string) string {
	if v == "" || v == "(devel)" {
		return ""
	}
	if len(v) > 1 && v[0] == 'v' && v[1] >= '0' && v[1] <= '9' {
		v = v[1:]
	}
	return v
}

func vcsSettings(info *debug.BuildInfo) (revision, vcsTime string, dirty bool) {
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.time":
			vcsTime = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	return revision, vcsTime, dirty
}
