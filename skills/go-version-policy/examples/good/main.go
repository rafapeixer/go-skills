// Command buildinfo reports which Go release built it and checks it against
// the floor declared in go.mod.
package main

import (
	"fmt"
	"go/version"
	"runtime"
	"runtime/debug"
	"sync"
)

// floor is the go line of go.mod in go/version form.
// TestFloorMatchesGoMod fails when the two drift apart.
const floor = "go1.26.0"

// Build describes the binary: the toolchain that compiled it and the commit.
type Build struct {
	Toolchain string  // for example "go1.27.2"
	Revision  *string // nil when built without VCS stamping
}

func readBuild() Build {
	b := Build{Toolchain: runtime.Version()}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return b
	}
	b.Toolchain = info.GoVersion
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			b.Revision = new(s.Value) // Go 1.26 new(expr): no ptr helper needed.
		}
	}
	return b
}

// atLeast reports whether toolchain is the floor or newer. Development builds
// such as "devel go1.28-abcdef" are not release versions and are accepted.
func atLeast(toolchain, floor string) bool {
	if !version.IsValid(toolchain) {
		return true
	}
	return version.Compare(toolchain, floor) >= 0
}

// probe runs one goroutine per name. With a go line of 1.22 or later every
// iteration has its own i and name, so no "name := name" copy is needed.
func probe(names []string) []string {
	out := make([]string, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() { out[i] = "ok " + name })
	}
	wg.Wait()
	return out
}

func main() {
	b := readBuild()
	fmt.Printf("toolchain %s, floor %s, meets floor: %t\n", b.Toolchain, floor, atLeast(b.Toolchain, floor))
	if b.Revision != nil {
		fmt.Println("revision", *b.Revision)
	}
	fmt.Println(probe([]string{"db", "cache"}))
}
