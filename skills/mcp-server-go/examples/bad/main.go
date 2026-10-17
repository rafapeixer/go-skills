// Command bad exposes a service to MCP clients the way generated code often
// does: a generic command runner, untyped arguments, a detached context,
// sessions pinned to one process and no authentication.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var db *sql.DB // opened elsewhere

// MISTAKE: the low-level AddTool with an "any object" schema; nothing is
// validated, the model gets no schema to follow, and every handler parses
// a map[string]any by hand.
var anyObject = &jsonschema.Schema{Type: "object"}

// MISTAKE: a generic shell tool. Arguments come from the model, and the model
// takes instructions from whatever it read last (a README, an issue, a web
// page): one prompt injection and the server runs "curl ... | sh".
func runCommand(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args map[string]any
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return nil, err // MISTAKE: a protocol error; the model cannot see it and self-correct
	}
	cmd, _ := args["command"].(string)
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	// MISTAKE: context.Background(): a cancelled or timed-out call keeps the
	// process running, with no deadline and no limit on how many run at once.
	out, err := exec.CommandContext(context.Background(), parts[0], parts[1:]...).CombinedOutput()
	if err != nil {
		return nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(out)}}}, nil
}

// MISTAKE: raw SQL built from model input. "orders WHERE 1=1; DROP TABLE x"
// is a valid argument, and the query runs with the service's credentials
// for whoever reached the endpoint.
func query(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args map[string]any
	_ = json.Unmarshal(req.Params.Arguments, &args)
	q := fmt.Sprintf("SELECT * FROM %v WHERE customer = '%v'", args["table"], args["customer"])
	rows, err := db.QueryContext(context.Background(), q)
	if err != nil {
		// MISTAKE: the driver error, with table and column names, goes to the client.
		return nil, err
	}
	defer rows.Close()
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
}

func main() {
	srv := mcp.NewServer(&mcp.Implementation{Name: "ops", Version: "v0.1.0"}, nil)
	srv.AddTool(&mcp.Tool{Name: "run_command", InputSchema: anyObject}, runCommand)
	srv.AddTool(&mcp.Tool{Name: "query", InputSchema: anyObject}, query)

	// MISTAKE: nil options means stateful sessions kept in this process's
	// memory. Behind a load balancer without affinity, the next request lands
	// on another replica, which answers "session not found"; with affinity,
	// a deploy or scale-in drops every session on the replica.
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)

	// MISTAKE: no auth.RequireBearerToken and no protected resource metadata:
	// anyone who can reach the port can run commands and queries.
	http.Handle("/mcp", handler)
	// MISTAKE: all interfaces, no server timeouts (see http-server).
	log.Fatal(http.ListenAndServe(":8080", nil))
}
