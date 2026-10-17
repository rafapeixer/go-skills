package main

import (
	"errors"
	"net/http"
	"testing"
)

func newService() *service {
	return &service{store: &store{users: map[string]User{"1": {ID: "1", Name: "Ana"}}}}
}

func TestRenameStatus(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		newName    string
		wantStatus int
	}{
		{"missing user", "2", "Bia", http.StatusNotFound},
		{"empty name", "1", "", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newService().Rename(tt.id, tt.newName)
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := statusFor(err); got != tt.wantStatus {
				t.Errorf("statusFor(%v) = %d, want %d", err, got, tt.wantStatus)
			}
		})
	}
}

func TestRenameKeepsCause(t *testing.T) {
	_, err := newService().Rename("2", "Bia")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("errors.Is(%v, ErrNotFound) = false; the cause was lost while wrapping", err)
	}
}

func TestRenameValidationField(t *testing.T) {
	_, err := newService().Rename("1", "")
	ve, ok := errors.AsType[*ValidationError](err)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if ve.Field != "name" {
		t.Errorf("Field = %q, want %q", ve.Field, "name")
	}
}
