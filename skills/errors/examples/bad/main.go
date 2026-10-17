// Command bad compiles, but every marked line is a mistake that breaks error
// inspection, duplicates logs or panics at run time.
package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
)

type ValidationError struct{ Field string }

func (e *ValidationError) Error() string { return "invalid " + e.Field }

type User struct{ ID, Name string }

var users = map[string]User{"1": {ID: "1", Name: "Ana"}}

func findUser(id string) (*User, error) {
	u, ok := users[id]
	if !ok {
		// MISTAKE: a new error value on every call; callers can only match it by its text.
		return nil, errors.New("not found")
	}
	return &u, nil
}

func rename(id, name string) (*User, error) {
	if name == "" {
		return nil, &ValidationError{Field: "name"}
	}
	u, err := findUser(id)
	// MISTAKE: uses the result before checking err; u is nil for a missing user, so this panics.
	log.Printf("renaming %s", u.Name)
	if err != nil {
		// MISTAKE: logs and returns; every caller up the stack logs the same failure again.
		log.Printf("failed to find user: %v", err)
		// MISTAKE: %v flattens the chain, so errors.Is and errors.As can no longer see the cause.
		// MISTAKE: "failed to" adds words, not context; the user ID that failed is missing.
		return nil, fmt.Errorf("failed to rename: %v", err)
	}
	u.Name = name
	return u, nil
}

func statusFor(err error) int {
	// MISTAKE: the target is a value, but only *ValidationError implements error,
	// so errors.As panics at run time. errors.AsType makes this a compile error.
	var ve ValidationError
	if errors.As(err, &ve) {
		return http.StatusBadRequest
	}
	// MISTAKE: string matching breaks as soon as anyone rewords the message or wraps it.
	if err.Error() == "not found" {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func main() {
	if _, err := rename("2", "Bia"); err != nil {
		log.Printf("status %d: %v", statusFor(err), err)
	}
}
