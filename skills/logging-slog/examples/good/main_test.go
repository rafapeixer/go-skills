package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func decodeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(buf.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestRequestLogsAreCorrelatedAndRedacted(t *testing.T) {
	var primary, audit bytes.Buffer
	logger := newLogger(slog.LevelDebug, &primary, slog.NewJSONHandler(&audit, nil))
	users := map[string]User{"1": {ID: "1", Email: "ana@example.com", APIKey: "sk_live_123"}}
	newHandler(logger, users).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/users/1", nil))

	if s := primary.String(); strings.Contains(s, "sk_live_123") || strings.Contains(s, "ana@example.com") {
		t.Fatalf("secret or PII in logs:\n%s", s)
	}
	lines := decodeLines(t, &primary)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want debug + access:\n%s", len(lines), primary.String())
	}
	id, _ := lines[0][keyRequestID].(string)
	if id == "" || lines[1][keyRequestID] != id {
		t.Errorf("request_id = %v and %v, want the same non-empty ID", lines[0][keyRequestID], lines[1][keyRequestID])
	}
	if u, _ := lines[0]["user"].(map[string]any); u["id"] != "1" {
		t.Errorf("user = %v, want {id: 1}", lines[0]["user"])
	}
	if got := lines[1][keyRoute]; got != "GET /users/{id}" {
		t.Errorf("route = %v, want the pattern", got)
	}

	// The second sink is at Info: it gets the access line only, with request_id.
	auditLines := decodeLines(t, &audit)
	if len(auditLines) != 1 || auditLines[0][keyRequestID] != id {
		t.Errorf("audit sink = %v, want one access line with request_id %s", auditLines, id)
	}
}

func TestTokenRedactedInAnyHandler(t *testing.T) {
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("login", "token", Token("sk_live_123"))
	if strings.Contains(buf.String(), "sk_live_123") {
		t.Fatalf("token leaked: %s", buf.String())
	}
}

func TestRunRejectsUnknownLevel(t *testing.T) {
	getenv := func(string) string { return "LOUD" }
	if err := run(getenv, &bytes.Buffer{}); err == nil {
		t.Fatal("run accepted LOG_LEVEL=LOUD")
	}
}
