//go:build go1.27 && goexperiment.jsonv2

package main

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
)

// decodeV2 was moved to encoding/json/v2 and then failed on real traffic.
// MISTAKE: instead of finding which clients send duplicate names or bad UTF-8,
// it switches every v2 protection off for every caller. Duplicate names are how
// two services read one request differently; restore v1 leniency only for a
// named client you cannot change, with a test that pins why.
func decodeV2(data []byte, v any) error {
	return jsonv2.Unmarshal(data, v,
		jsontext.AllowDuplicateNames(true),
		jsontext.AllowInvalidUTF8(true),
		jsonv2.MatchCaseInsensitiveNames(true),
	)
}
