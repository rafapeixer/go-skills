package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

var errTrailingData = errors.New("unexpected data after the JSON value")

// decodeStrict decodes exactly one JSON value into v and rejects fields the
// struct does not declare. The caller bounds r first (http.MaxBytesReader, see
// the http-server skill).
func decodeStrict(r io.Reader, v any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errTrailingData
	}
	return nil
}

// problem turns a decode error into a client message by its type, never by its
// text: Go 1.27 reworded encoding/json errors without changing their types.
func problem(err error) string {
	if err == nil {
		return "ok"
	}
	if se, ok := errors.AsType[*json.SyntaxError](err); ok {
		return fmt.Sprintf("malformed JSON near byte %d", se.Offset)
	}
	if te, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		return fmt.Sprintf("field %q has the wrong type", te.Field)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "empty or truncated body"
	}
	if errors.Is(err, errBadAmount) || errors.Is(err, errTrailingData) {
		return err.Error()
	}
	// Unknown fields land here: v1 reports them without a dedicated type.
	return "invalid JSON body"
}
