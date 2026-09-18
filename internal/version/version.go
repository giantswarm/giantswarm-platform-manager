// Package version names the build — the org convention: no -ldflags,
// debug.ReadBuildInfo() is the source. The server reports it in get_info,
// platformctl in `platformctl version`.
package version

import "runtime/debug"

// String is the module version of a release, else the VCS revision the
// binary was built from ("dev-<12 hex>", "-dirty" with uncommitted changes),
// else "dev".
func String() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	rev, dirty := "", ""
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if rev == "" {
		return "dev"
	}
	return "dev-" + rev + dirty
}
