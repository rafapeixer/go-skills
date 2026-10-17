package main

import "testing"

// MISTAKE: asserts the exact error text from Go 1.26. It passes there and fails
// on Go 1.27, where encoding/json names the root type: the test pins the
// standard library's wording, not this program's behavior.
func TestDecodeTypeError(t *testing.T) {
	var e Event
	err := decode(`{"source":{"port":"80"}}`, &e)
	want := "json: cannot unmarshal string into Go struct field Source.source.port of type int"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}
