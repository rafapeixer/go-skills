// Command buildinfo, written the way code looks when the go line is stuck at
// an old release: every workaround below exists only because of go.mod.
package main

import (
	"fmt"
	"runtime"
	"sync"
)

// MISTAKE: a pointer helper. new(expr) replaces it since Go 1.26, but this
// module cannot use it: the go 1.21 line makes new(s) a compile error.
func ptr[T any](v T) *T { return &v }

// MISTAKE: compares versions as strings. "go1.9" >= "go1.26.0" is true
// lexically, so a go1.9 build passes the check. Use go/version.Compare.
func atLeast(toolchain, floor string) bool {
	return toolchain >= floor
}

func probe(names []string) []string {
	out := make([]string, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		// MISTAKE: the pre-1.22 copy. It is needed here only because the go line
		// is below 1.22; delete it without raising the floor and every goroutine
		// may read the last name. Raise the floor and go fix (forvar) removes it.
		i, name := i, name
		// MISTAKE: Add and Done by hand. sync.WaitGroup.Go (Go 1.25) cannot
		// forget the Add; go fix rewrites this once the floor allows it.
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = "ok " + name
		}()
	}
	wg.Wait()
	return out
}

func main() {
	// MISTAKE: sets GOMAXPROCS by hand "because Go is not container aware".
	// The runtime has been since Go 1.25, but only for go lines >= 1.25. NumCPU
	// counts the node's cores, so a 2-CPU pod on a 64-core node runs 64 Ps and is
	// throttled, and setting it at all turns off the runtime's periodic updates.
	runtime.GOMAXPROCS(runtime.NumCPU())

	floor := ptr("go1.21")
	fmt.Println("meets floor:", atLeast(runtime.Version(), *floor))
	fmt.Println(probe([]string{"db", "cache"}))
}
