module github.com/rafapeixer/go-skills/skills/go-version-policy/examples/bad

// MISTAKE: go 1.21 is the floor an old template or a code generator wrote years
// ago. Go 1.21 lost support when Go 1.23 shipped in August 2024, and the line keeps
// Go 1.21 semantics alive on a 1.26 compiler: loop variables shared across
// iterations, GOMAXPROCS ignoring the container CPU limit, and Go 1.21 GODEBUG
// defaults. Nobody reviews it because the code still builds.
go 1.21

// MISTAKE: a stale toolchain line recorded by whoever last ran go get. It names
// a release with no security fixes, and with GOTOOLCHAIN=auto it is a second
// version knob that drifts from the one CI actually uses.
toolchain go1.21.0
