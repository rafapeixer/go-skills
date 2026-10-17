// Command events, written the way JSON code drifts when nobody checks what
// the tags and error strings actually do.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Source struct {
	Port int `json:"port"`
}

type Meta struct {
	Trace string `json:"trace"`
}

type Event struct {
	ID string `json:"id"`
	// MISTAKE: omitempty never omits a time.Time or any other struct, so every
	// live event is sent with "deleted_at":"0001-01-01T00:00:00Z" and clients
	// treat it as deleted. omitzero (Go 1.24+) is the tag that means this.
	DeletedAt time.Time `json:"deleted_at,omitempty"`
	// MISTAKE: "inline" is not a tag option. v1 never had one and v2 renamed its
	// experimental inline to embed before Go 1.27, so this field is silently
	// sent as a nested "Meta" object instead of being flattened.
	Meta   Meta   `json:",inline"`
	Label  Label  `json:"label"`
	Source Source `json:"source"`
}

type Label string

// MISTAKE: JSON assembled by hand "because encoding/json is slow", with no
// benchmark. %q is Go quoting, not JSON quoting: a label with a control
// character or invalid UTF-8 becomes \a or \xff, which no JSON parser accepts.
// The same no-benchmark reasoning drives swaps to jsoniter or sonic.
func (l Label) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%q", string(l))), nil
}

// statusFor maps decode errors to HTTP status codes by reading their text.
func statusFor(err error) int {
	msg := err.Error()
	switch {
	// MISTAKE: matches message text. Go 1.27 reworded encoding/json errors
	// ("Go struct field Source.source.port" became "Event.source.port", slice
	// paths gained indexes), so this branch silently stops matching on upgrade.
	case strings.Contains(msg, "Go struct field Source."):
		return http.StatusUnprocessableEntity
	case strings.HasPrefix(msg, "invalid character"):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func decode(body string, v any) error {
	// MISTAKE: no DisallowUnknownFields and no trailing-data check: a client
	// typo such as "deleted_ta" is dropped silently and the request succeeds.
	return json.NewDecoder(strings.NewReader(body)).Decode(v)
}
func main() {
	_, err := json.Marshal(Event{ID: "e1", Meta: Meta{Trace: "t1"}, Label: "beep\a"})
	fmt.Println("marshal:", err) // Marshal rejects what Label produced
	var e Event
	err = decode(`{"id":"e1","deleted_ta":"x"} trailing`, &e)
	fmt.Println("typo and trailing data accepted:", err == nil)
	err = decode(`{"source":{"port":"80"}}`, &e)
	fmt.Println("status:", statusFor(err)) // 422 on Go 1.26, 500 on Go 1.27
}
