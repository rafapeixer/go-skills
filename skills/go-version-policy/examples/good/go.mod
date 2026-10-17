module github.com/rafapeixer/go-skills/skills/go-version-policy/examples/good

// The floor: the oldest Go release that still receives security fixes.
// No toolchain line: CI builds with GOTOOLCHAIN=local and the newest patch.
go 1.26.0
