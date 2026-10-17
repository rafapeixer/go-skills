//go:build wasip1

// Command guest is a plugin built as a WASI reactor:
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
//
// The host calls _initialize once per instance, then the exported functions.
package main

import "os"

// main is required by package main but never runs in a reactor.
func main() {}

//go:wasmexport add
func add(a, b int32) int32 {
	return a + b
}

// spin never returns: a stand-in for a plugin with a bug or a hostile loop.
//
//go:wasmexport spin
func spin() {
	for {
	}
}

// readData returns the length of /data/input.txt, or -1 if it cannot be read.
// Only the host decides what /data is, if anything.
//
//go:wasmexport readData
func readData() int32 {
	b, err := os.ReadFile("/data/input.txt")
	if err != nil {
		return -1
	}
	return int32(len(b))
}

// grow allocates n MiB and keeps it reachable, to hit the host's memory limit.
//
//go:wasmexport grow
func grow(n int32) int32 {
	for range n {
		hold = append(hold, make([]byte, 1<<20))
	}
	return int32(len(hold))
}

var hold [][]byte
