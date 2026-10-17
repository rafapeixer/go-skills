//go:build goexperiment.jsonv2

package main

// encoding/json on the v2 engine (the Go 1.27 default) names the root type.
// Code that compared this field, or the error text, broke on upgrade.
const typeErrorStruct = "Event"
