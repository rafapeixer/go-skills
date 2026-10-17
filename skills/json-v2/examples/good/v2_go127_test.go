//go:build go1.27 && goexperiment.jsonv2

package main

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"strings"
	"testing"
)

func TestCentsMethodsAgree(t *testing.T) {
	c := Cents(1234)
	viaMethod, _ := c.MarshalJSON()
	viaV1, _ := json.Marshal(c) // calls MarshalJSONTo on Go 1.27
	viaV2, _ := jsonv2.Marshal(c)
	if string(viaMethod) != string(viaV1) || string(viaV1) != string(viaV2) {
		t.Fatalf("MarshalJSON %s, v1 %s, v2 %s", viaMethod, viaV1, viaV2)
	}
}

// Pin the inputs v1 accepts and v2 rejects before moving a call to v2.
func TestV2RejectsWhatV1Accepts(t *testing.T) {
	tests := []struct {
		name, body string
		v2Err      error // nil: only check that v2 fails
	}{
		{"duplicate name", `{"id":"a","id":"b"}`, jsontext.ErrDuplicateName},
		{"invalid UTF-8", "{\"id\":\"\xff\"}", nil},
		{"case mismatch", `{"ID":"a"}`, jsonv2.ErrUnknownName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v1, v2 Event
			if err := decodeStrict(strings.NewReader(tt.body), &v1); err != nil {
				t.Fatalf("v1 rejected %q: %v", tt.body, err)
			}
			err := decodeStrictV2(strings.NewReader(tt.body), &v2)
			if err == nil || (tt.v2Err != nil && !errors.Is(err, tt.v2Err)) {
				t.Fatalf("v2 error = %v, want %v", err, tt.v2Err)
			}
		})
	}
}

func TestDecodeStrictV2RejectsUnknownAndTrailing(t *testing.T) {
	var e Event
	if err := decodeStrictV2(strings.NewReader(`{"amout":"1.00"}`), &e); !errors.Is(err, jsonv2.ErrUnknownName) {
		t.Fatalf("unknown member: %v", err)
	}
	if err := decodeStrictV2(strings.NewReader(`{} {}`), &e); err == nil {
		t.Fatal("trailing value accepted")
	}
	if err := decodeStrictV2(strings.NewReader(`{"amount":"0.05"}`), &e); err != nil || e.Amount != 5 {
		t.Fatalf("UnmarshalJSONFrom: err %v, amount %d", err, e.Amount)
	}
}
