package main

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestOmitzeroLeavesOutZeroTimeAndStruct(t *testing.T) {
	e := Event{ID: "e1", Amount: 1234, CreatedAt: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)}
	got, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"e1","amount":"12.34","created_at":"2026-10-17T12:00:00Z"}`
	if string(got) != want {
		t.Fatalf("Marshal = %s, want %s", got, want)
	}
}

func TestDecodeStrict(t *testing.T) {
	var e Event
	if err := decodeStrict(strings.NewReader(`{"id":"e1","amount":"0.05"}`), &e); err != nil || e.Amount != 5 {
		t.Fatalf("valid body: err %v, amount %d", err, e.Amount)
	}
	tests := []struct {
		name, body string
		check      func(error) bool
	}{
		{"syntax", `{"id":}`, is[*json.SyntaxError]},
		{"wrong type", `{"source":{"port":"80"}}`, is[*json.UnmarshalTypeError]},
		{"truncated", `{"id":"e1"`, func(err error) bool { return errors.Is(err, io.ErrUnexpectedEOF) }},
		{"trailing", `{"id":"e1"} {"id":"e2"}`, func(err error) bool { return errors.Is(err, errTrailingData) }},
		{"unknown field", `{"id":"e1","amout":"1.00"}`, func(err error) bool { return err != nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var e Event
			if err := decodeStrict(strings.NewReader(tt.body), &e); !tt.check(err) {
				t.Fatalf("decodeStrict(%s) = %v (%T)", tt.body, err, err)
			}
		})
	}
}

func is[T error](err error) bool { _, ok := errors.AsType[T](err); return ok }

// The type and its Field are what callers can rely on; even the Struct field
// changed in Go 1.27 (see typeErrorStruct in the engine_*_test.go files).
func TestTypeErrorFields(t *testing.T) {
	var e Event
	err := decodeStrict(strings.NewReader(`{"source":{"port":"80"}}`), &e)
	te, ok := errors.AsType[*json.UnmarshalTypeError](err)
	if !ok {
		t.Fatalf("err = %v, want *json.UnmarshalTypeError", err)
	}
	if te.Field != "source.port" || te.Struct != typeErrorStruct {
		t.Fatalf("Field %q Struct %q, want source.port and %q", te.Field, te.Struct, typeErrorStruct)
	}
}

func TestCentsRoundTrip(t *testing.T) {
	b, err := json.Marshal(Cents(100007))
	if err != nil || string(b) != `"1000.07"` {
		t.Fatalf("Marshal = %s, %v", b, err)
	}
	for _, in := range []string{`12.34`, `"12.3"`, `"x.00"`} {
		var c Cents
		if err := json.Unmarshal([]byte(in), &c); !errors.Is(err, errBadAmount) {
			t.Errorf("Unmarshal(%s) = %v, want errBadAmount", in, err)
		}
	}
}
