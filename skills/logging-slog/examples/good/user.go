package main

import "log/slog"

// Token is a credential. Its LogValue keeps it out of every log line,
// whatever handler or key it is logged under.
type Token string

func (Token) LogValue() slog.Value { return slog.StringValue("REDACTED") }

type User struct {
	ID     string
	Email  string // PII
	APIKey Token  // secret
}

// LogValue logs only the ID: logging a User can never leak the email or the
// key, even after someone adds a field.
func (u User) LogValue() slog.Value {
	return slog.GroupValue(slog.String("id", u.ID))
}
