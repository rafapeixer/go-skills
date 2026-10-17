package main

import (
	"runtime/debug"
	"testing"
)

func TestDescribe(t *testing.T) {
	t.Parallel()
	mod := debug.Module{Path: "example.com/app", Version: "v1.4.0"}
	tests := []struct {
		name     string
		settings []debug.BuildSetting
		want     string
	}{
		{
			name:     "clean release",
			settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "3f2c1ab"}, {Key: "vcs.modified", Value: "false"}},
			want:     "v1.4.0 go1.26.0 rev=3f2c1ab",
		},
		{
			name:     "uncommitted changes",
			settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "3f2c1ab"}, {Key: "vcs.modified", Value: "true"}},
			want:     "v1.4.0 go1.26.0 rev=3f2c1ab+dirty",
		},
		{
			name:     "built with -buildvcs=false",
			settings: []debug.BuildSetting{{Key: "-trimpath", Value: "true"}},
			want:     "v1.4.0 go1.26.0 rev=unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			info := &debug.BuildInfo{GoVersion: "go1.26.0", Main: mod, Settings: tt.settings}
			if got := describe(info); got != tt.want {
				t.Errorf("describe() = %q, want %q", got, tt.want)
			}
		})
	}
}
