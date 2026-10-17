package orders

import (
	"context"
	"errors"
	"testing"
)

// fakeCharger implements one method, because charger has one method.
type fakeCharger struct{ err error }

func (f fakeCharger) Charge(context.Context, string, int64) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "ch_test", nil
}

func TestPlace(t *testing.T) {
	o, err := New(fakeCharger{}).Place(t.Context(), "cus_1", 2500)
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if o.ChargeID != "ch_test" || o.Cents != 2500 {
		t.Errorf("Place = %+v, want charge ch_test for 2500 cents", o)
	}
}

func TestPlaceChargeFails(t *testing.T) {
	errDeclined := errors.New("declined")
	_, err := New(fakeCharger{err: errDeclined}).Place(t.Context(), "cus_1", 2500)
	if !errors.Is(err, errDeclined) {
		t.Fatalf("Place error = %v, want it to wrap %v", err, errDeclined)
	}
}
