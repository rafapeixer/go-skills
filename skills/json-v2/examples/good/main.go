// Command events encodes and decodes events with encoding/json on the Go 1.26
// floor, and adds the encoding/json/v2 methods in files built on Go 1.27+.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Source is a struct-valued field: omitempty never omits a struct, omitzero does.
type Source struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

// Event is the wire shape. Each tag says which "absent" it means.
type Event struct {
	ID        string    `json:"id"`
	Amount    Cents     `json:"amount"`
	Note      string    `json:"note,omitempty"` // empty string: leave it out
	CreatedAt time.Time `json:"created_at"`
	DeletedAt time.Time `json:"deleted_at,omitzero"` // zero time: leave it out (Go 1.24+)
	Source    Source    `json:"source,omitzero"`     // zero struct: leave it out
}

func main() {
	e := Event{ID: "e1", Amount: 1234, CreatedAt: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)}
	if err := json.NewEncoder(os.Stdout).Encode(e); err != nil {
		fmt.Fprintln(os.Stderr, "encode:", err)
		os.Exit(1)
	}

	var in Event
	err := decodeStrict(strings.NewReader(`{"id":"e2","amount":12.5}`), &in)
	fmt.Println("decode:", problem(err))
}
