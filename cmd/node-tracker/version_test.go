package main

import (
	"runtime/debug"
	"testing"
)

func TestFormatVersion(t *testing.T) {
	vcs := func(rev, modified string) []debug.BuildSetting {
		return []debug.BuildSetting{{Key: "vcs.revision", Value: rev}, {Key: "vcs.modified", Value: modified}}
	}
	for _, tc := range []struct {
		name, v, c, bt string
		bi             *debug.BuildInfo
		want           string
	}{
		{"make build", "v1.0.0", "a1b2c3d", "2026-11-01T08:00:00Z",
			&debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			"node-tracker v1.0.0 (a1b2c3d, built 2026-11-01T08:00:00Z, go1.26.10)"},
		{"go install tag", "dev", "", "",
			&debug.BuildInfo{Main: debug.Module{Version: "v1.0.0"}},
			"node-tracker v1.0.0 (go install, go1.26.10)"},
		{"go install pseudo-version", "dev", "", "",
			&debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20261101080000-a1b2c3d4e5f6"}},
			"node-tracker v0.0.0-20261101080000-a1b2c3d4e5f6 (a1b2c3d, go install, go1.26.10)"},
		{"go install pseudo-version after a tag", "dev", "", "",
			&debug.BuildInfo{Main: debug.Module{Version: "v1.0.1-0.20261101080000-a1b2c3d4e5f6"}},
			"node-tracker v1.0.1-0.20261101080000-a1b2c3d4e5f6 (a1b2c3d, go install, go1.26.10)"},
		{"go build in a checkout", "dev", "", "",
			&debug.BuildInfo{Main: debug.Module{Version: "v1.0.1-0.20261101080000-a1b2c3d4e5f6+dirty"},
				Settings: vcs("a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", "true")},
			"node-tracker v1.0.1-0.20261101080000-a1b2c3d4e5f6+dirty (a1b2c3d-dirty, go1.26.10)"},
		{"go build without vcs", "dev", "", "",
			&debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			"node-tracker dev (go1.26.10)"},
		{"no build info", "dev", "", "", nil, "node-tracker dev (go1.26.10)"},
	} {
		if got := formatVersion(tc.v, tc.c, tc.bt, tc.bi, "go1.26.10"); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}
