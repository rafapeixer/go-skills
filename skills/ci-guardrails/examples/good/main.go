// Command buildinfo prints the version and VCS revision stamped into its own binary.
// A release built with -trimpath and default VCS stamping reports a real revision;
// one built with -buildvcs=false reports nothing a scanner or an incident can use.
package main

import (
	"fmt"
	"runtime/debug"
)

func main() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Println("no build info")
		return
	}
	fmt.Println(describe(info))
}

// describe returns "<module version> <go version> rev=<revision>[+dirty]".
func describe(info *debug.BuildInfo) string {
	rev, dirty := "unknown", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if dirty {
		rev += "+dirty"
	}
	return fmt.Sprintf("%s %s rev=%s", info.Main.Version, info.GoVersion, rev)
}
