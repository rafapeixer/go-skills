// Command good shows errors crossing layers: the store reports a sentinel,
// the service adds context with %w, and one function maps errors to HTTP
// status codes. The error is logged once, where it stops travelling.
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
)

// ErrNotFound is a category: callers branch on it with errors.Is.
var ErrNotFound = errors.New("not found")

// ValidationError carries data the caller needs, so it is a type.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid %s: %s", e.Field, e.Reason)
}

type User struct {
	ID   string
	Name string
}

type store struct{ users map[string]User }

func (s *store) user(id string) (User, error) {
	u, ok := s.users[id]
	if !ok {
		return User{}, ErrNotFound
	}
	return u, nil
}

type service struct{ store *store }

func (s *service) Rename(id, name string) (User, error) {
	if name == "" {
		return User{}, &ValidationError{Field: "name", Reason: "must not be empty"}
	}
	u, err := s.store.user(id)
	if err != nil {
		return User{}, fmt.Errorf("rename user %s: %w", id, err)
	}
	u.Name = name
	return u, nil
}

// statusFor is the only place that turns errors into HTTP status codes.
func statusFor(err error) int {
	if _, ok := errors.AsType[*ValidationError](err); ok {
		return http.StatusBadRequest
	}
	if errors.Is(err, ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	svc := &service{store: &store{users: map[string]User{"1": {ID: "1", Name: "Ana"}}}}

	for _, id := range []string{"1", "2"} {
		if _, err := svc.Rename(id, "Bia"); err != nil {
			logger.Error("rename", "user_id", id, "status", statusFor(err), "err", err)
			continue
		}
		logger.Info("rename", "user_id", id, "status", http.StatusOK)
	}
}
