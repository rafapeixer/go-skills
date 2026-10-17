//go:build !goexperiment.jsonv2

package main

// The original encoding/json (Go 1.26, or Go 1.27 built with
// GOEXPERIMENT=nojsonv2) names the struct that holds the field.
const typeErrorStruct = "Source"
