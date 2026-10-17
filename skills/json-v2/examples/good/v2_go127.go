//go:build go1.27 && goexperiment.jsonv2

package main

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"io"
)

// MarshalJSONTo writes the token straight to the encoder, with no intermediate
// []byte. On Go 1.27 encoding/json (v1) calls it too, ahead of MarshalJSON, so
// both methods must produce the same JSON; TestCentsMethodsAgree checks that.
func (c Cents) MarshalJSONTo(enc *jsontext.Encoder) error {
	return enc.WriteToken(jsontext.String(c.String()))
}

// UnmarshalJSONFrom reads one token from the stream instead of a buffered value.
func (c *Cents) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != '"' {
		return errBadAmount
	}
	v, err := parseCents(tok.String())
	if err != nil {
		return err
	}
	*c = v
	return nil
}

// decodeStrictV2 is decodeStrict on the v2 API. UnmarshalRead already rejects
// trailing data, RejectUnknownMembers replaces DisallowUnknownFields, and the
// v2 defaults also reject duplicate names, invalid UTF-8 and names whose case
// does not match the tag.
func decodeStrictV2(r io.Reader, v any) error {
	return jsonv2.UnmarshalRead(r, v, jsonv2.RejectUnknownMembers(true))
}
