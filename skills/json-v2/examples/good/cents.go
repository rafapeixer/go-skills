package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Cents is money in minor units, sent as a decimal string ("12.34") so that
// no client parses it into a float.
type Cents uint64

// errBadAmount is what both decode methods return, so callers get the same
// error whichever method the running encoding/json picks.
var errBadAmount = errors.New(`amount must be a decimal string such as "12.34"`)

func (c Cents) String() string { return fmt.Sprintf("%d.%02d", c/100, c%100) }

func parseCents(s string) (Cents, error) {
	whole, frac, ok := strings.Cut(s, ".")
	if !ok || len(frac) != 2 {
		return 0, fmt.Errorf("%w: got %q", errBadAmount, s)
	}
	w, err1 := strconv.ParseUint(whole, 10, 64)
	f, err2 := strconv.ParseUint(frac, 10, 64)
	if err1 != nil || err2 != nil {
		return 0, fmt.Errorf("%w: got %q", errBadAmount, s)
	}
	return Cents(w*100 + f), nil
}

// MarshalJSON hands the string to the encoder, which quotes and escapes it.
// It never assembles JSON with + or fmt.Sprintf.
func (c Cents) MarshalJSON() ([]byte, error) { return json.Marshal(c.String()) }

func (c *Cents) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || b[0] != '"' {
		return errBadAmount
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := parseCents(s)
	if err != nil {
		return err
	}
	*c = v
	return nil
}
