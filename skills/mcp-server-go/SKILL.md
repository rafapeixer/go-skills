---
name: mcp-server-go
description: Use when writing or reviewing a Go Model Context Protocol (MCP) server that exposes a service's data or actions as tools to coding agents and LLM clients, choosing between stdio and streamable HTTP, deploying it behind a load balancer, or adding authorization to it. Covers the official github.com/modelcontextprotocol/go-sdk, narrow intent-level tools instead of shell or SQL runners, typed mcp.AddTool handlers with input and output structs and a tightened JSON schema, StreamableHTTPOptions.Stateless with PropagateRequestCancellation, auth.RequireBearerToken with protected resource metadata and audience checks, a fail-fast concurrency cap and per-call deadline, and tool errors vs protocol errors.
metadata:
  verified: 2026-10-17
---

# MCP server in Go

## The decision

A Model Context Protocol server turns parts of your service into tools that a model decides to call. The decision is how much of the service those tools expose, how their inputs are checked, which transport carries them and who is allowed to call them. The fast path is the dangerous one: a `run_command` or `run_sql` tool, arguments parsed from `map[string]any`, handlers on `context.Background()`, the streamable HTTP handler with default options behind a load balancer, and no authentication because "only our agent calls it". The model reading tool results is steered by whatever text it read last, so every argument is attacker-influenced input, and a tool call is a request from a client you do not control. This skill fixes the server's shape with the official Go SDK. Server timeouts, overload policy and general crypto belong to `http-server`, `overload-protection` and `secure-defaults`; this skill only says where they plug in.

## The rule

1. Build the server with the official `github.com/modelcontextprotocol/go-sdk` (v1.7.0 or later; the examples pin v1.8.0) and expose narrow, intent-level tools such as `search_orders`, never a tool that runs a shell command, raw SQL or an arbitrary path or URL.
2. Register every tool with the generic `mcp.AddTool` and named input and output structs, and add an enum, bounds or a pattern to the inferred schema wherever a Go type cannot say which values are allowed.
3. Serve remote clients over streamable HTTP with `StreamableHTTPOptions{Stateless: true, PropagateRequestCancellation: true}`, and use `mcp.StdioTransport` only for a local server the client starts as a subprocess.
4. Wrap the HTTP handler in `auth.RequireBearerToken` with a verifier that rejects tokens not issued for this server, serve protected resource metadata, and scope every query to `req.Extra.TokenInfo`, never to an identity taken from the arguments.
5. Pass the handler's `ctx` to every downstream call, and wrap every tool in a concurrency cap that fails fast and a per-call `context.WithTimeoutCause`.
6. Return failures the model can act on (bad input, not found, busy, timed out) as a plain `error` from the typed handler, keep internal details out of that text, and return a `*jsonrpc.Error` only for protocol-level failures.

## Why

**Rule 1.** The official SDK is maintained by the MCP organization with Google, tracks the spec (v1.7.0 and later support protocol 2026-07-28, the version that makes HTTP sessionless) and needs only Go 1.25, so it builds on the 1.26 floor. It also ships what a remote server needs and a community SDK may not: the `auth` and `oauthex` packages for the resource-server side of the spec, a 4 MiB default request body limit (`MaxRequestBodyBytes`) and DNS-rebinding protection for localhost listeners. The harder part of rule 1 is tool design. The spec's security section says servers MUST validate all tool inputs and implement proper access controls; a `run_command` tool makes both impossible, because the allowed input is "anything the process can do". The model decides what to call based on text it has read, and that text can come from a README, an issue, a web page or an earlier tool result: one prompt injection turns `run_command` into remote code execution and `run_sql` into `DROP TABLE` with the service's credentials. A tool named after an intent (`search_orders`, `get_invoice`, `cancel_shipment`) has a small, checkable input and maps to one parameterized query or one service method. If a workflow needs a new capability, add a new narrow tool; do not widen an old one.

**Rule 2.** `mcp.AddTool[In, Out]` infers the input schema from `In` and the output schema from `Out`, validates every call's arguments against the input schema before the handler runs, unmarshals them into `In`, and validates and returns `Out` as structured content with a JSON text copy. Struct fields without `omitempty` or `omitzero` become required, and a struct schema gets `additionalProperties: false`, so an argument the model invents (`"owner": "bob"`) is rejected. The low-level `Server.AddTool` does none of that: the SDK docs say it performs no input validation, so every handler re-parses a `map[string]any` and forgets a case. The `jsonschema` struct tag only sets a description; constraints such as an enum or a maximum need the schema itself, so build it with `jsonschema.For[In]` from `github.com/google/jsonschema-go` (the package the SDK uses) and set `Enum`, `Minimum`, `Maximum` or `Pattern` on the property. The schema is also what the model sees, so an enum both blocks bad values and tells the model which ones exist. Rules a schema cannot express, such as "this order belongs to the caller", stay in the handler. A validation failure comes back as a tool result with `IsError` set, which the spec requires so the model can correct itself.

**Rule 3.** In the default (stateful) mode the streamable handler keeps each session in the memory of the process that answered `initialize`, keyed by the `Mcp-Session-Id` header. Behind a load balancer without affinity, the next request lands on another replica that has never seen the session; with affinity, a deploy, a scale-in or scale-to-zero drops every session on the replica. With `Stateless: true` the handler neither reads nor sets `Mcp-Session-Id` and serves each request with a temporary session, so any replica answers any request. Protocol 2026-07-28 removed the session from the protocol, and the SDK accepts requests at that version only when `Stateless` is true. The cost: the server cannot send requests to the client (sampling, elicitation, roots); 2026-07-28 deprecates roots, sampling and logging, and a tool that needs state across calls returns an explicit handle, as the spec's "Stateful Tools" guidance says. `PropagateRequestCancellation` ties the handler's context to the HTTP request (for 2026-07-28 requests). Without it, in v1.8.0, the good example's cancellation test fails: the client gives up and the handler keeps running. `stdio` is the right transport only when the client launches the server as a child process on the same machine; it has no network surface, and the spec says stdio servers take credentials from the environment instead of OAuth.

**Rule 4.** Authorization is optional in the spec, but an HTTP MCP server without it is a remote API that runs whatever its tools allow for anyone who reaches the port. When it is present, the server acts as an OAuth 2.1 resource server: it MUST serve OAuth 2.0 Protected Resource Metadata (RFC 9728), MUST validate that each access token was issued for it as the audience (RFC 8707), MUST answer 401 for invalid or expired tokens, and MUST NOT accept or pass through other tokens. `auth.RequireBearerToken` handles the header parsing, the 401 or 403, the `WWW-Authenticate` challenge with `resource_metadata` and `scope`, the expiry check (it rejects tokens without an expiration by default) and the required scopes; it puts the `TokenInfo` in the request, where a tool handler reads it as `req.Extra.TokenInfo`. Signature and audience checks are your `TokenVerifier`'s job: verify the JWT against the issuer's keys or introspect the token, and reject any `aud` other than this server's canonical URI, or a token minted for another service can be replayed here. `auth.ProtectedResourceMetadataHandler` serves the metadata document clients use to find the authorization server. Inside a tool, take the user from the token: a `customer_id` argument is whatever the model was told to send. Setting `TokenInfo.UserID` also lets stateful handlers bind sessions to users.

**Rule 5.** Tool calls are the slow part of an agent loop, and the SDK handles calls from one client concurrently. A model that retries or fans out can open dozens of calls at once, and a client that gives up still leaves the query running if the handler used `context.Background()`. On a connection transport such as stdio, the SDK cancels the handler's `ctx` when the client sends `notifications/cancelled`; on stateless HTTP, rule 3's `PropagateRequestCancellation` is what cancels it when the client gives up. Passing that `ctx` to the store or HTTP call stops the work (see `context-and-deadlines`). The spec says servers MUST rate limit tool invocations. A buffered-channel semaphore with a `select` and `default` caps calls per replica and answers "busy" at once instead of queueing, which is the bulkhead from `overload-protection` applied per tool; size it from what the dependency behind the tools can serve, and keep the HTTP-level limiter from that skill in front as well. The per-call deadline with a cause bounds the slowest call and tells the model which budget ran out. Wrapping the typed handler in a generic `bounded[In, Out]` keeps the schema inference of rule 2 intact.

**Rule 6.** The spec has two error channels. Protocol errors (unknown tool, malformed request, server fault) are JSON-RPC errors, which clients may not show to the model. Tool execution errors (API failure, invalid input, business rule, busy, timeout) are results with `isError: true`, which clients should show to the model so it can retry with other arguments or give up. A typed handler gets this for free: any ordinary returned `error` becomes an `IsError` result with the error text as content, and only a `*jsonrpc.Error` becomes a protocol error. The low-level handler does the opposite, turning every returned error into a protocol error. Because the error text goes to the model and from there to the user's screen and logs, it is output, and the spec says to sanitize outputs: log the driver error with `slog` and return a message such as "order store unavailable, retry later", never a SQL error with table names or a stack trace.

## Good example

See [`examples/good/`](examples/good/).

```go
func newHandler(srv *mcp.Server, verify auth.TokenVerifier) http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, PropagateRequestCancellation: true})
	metadataURL := "https://orders.example.com/.well-known/oauth-protected-resource"
	requireToken := auth.RequireBearerToken(verify, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: metadataURL,
		Scopes:              []string{scope},
	})
```

The one `*mcp.Server` is shared by every request, which the SDK allows, and the only route to it goes through the token check. Each tool is wrapped once, keeping its typed signature:

```go
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		select {
		case l.slots <- struct{}{}:
		default:
			var zero Out
			return nil, zero, errBusy // a tool error: the model sees it and can retry later
		}
		defer func() { <-l.slots }()
		ctx, cancel := context.WithTimeoutCause(ctx, l.timeout, errToolTimeout)
		defer cancel()
		return h(ctx, req, in)
	}
```

The tests drive the real HTTP handler with the SDK's own client over `httptest`, offline. They check that the listed input schema has the required field, the enum, the maximum and `additionalProperties: false`; that an out-of-enum status, an out-of-range limit, an invented `owner` argument and an injection string all come back as tool errors without reaching the store; that the store sees the token's user; that cancelling the client's context cancels the store call; that a slow store produces the timeout tool error; that a second call while the only slot is taken gets "busy" at once; that a missing, forged or wrong-audience token gets 401 with `resource_metadata`; and that a round-robin balancer over two replicas with separate servers serves every call.

## Bad example

See [`examples/bad/`](examples/bad/).

```go
	cmd, _ := args["command"].(string)
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	// MISTAKE: context.Background(): a cancelled or timed-out call keeps the
	// process running, with no deadline and no limit on how many run at once.
	out, err := exec.CommandContext(context.Background(), parts[0], parts[1:]...).CombinedOutput()
```

```go
	// MISTAKE: nil options means stateful sessions kept in this process's
	// memory. Behind a load balancer without affinity, the next request lands
	// on another replica, which answers "session not found"; with affinity,
	// a deploy or scale-in drops every session on the replica.
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)

	// MISTAKE: no auth.RequireBearerToken and no protected resource metadata:
	// anyone who can reach the port can run commands and queries.
	http.Handle("/mcp", handler)
```

This is the server that appears when the goal is "let the agent do things in our system": a shell tool and a SQL tool registered with `Server.AddTool` and an "any object" schema, arguments read from `map[string]any`, failures returned as protocol errors carrying the driver's message, and an unauthenticated stateful endpoint. It works in a demo with one replica and a friendly model, and each mistake is commented in the file.

## Checklist

When reviewing code under this skill, confirm:

- [ ] The server uses `github.com/modelcontextprotocol/go-sdk` at v1.7.0 or later, not a community SDK.
- [ ] No tool runs a shell command, `exec.Command`, a SQL string, a file path or a URL built from its arguments; each tool maps to one narrow operation.
- [ ] Every tool is registered with `mcp.AddTool` and named input and output structs, not `Server.AddTool` or `map[string]any`.
- [ ] Inputs with a closed set of values or a range have an enum, bounds or pattern in the schema, and the handler checks anything the schema cannot.
- [ ] An HTTP server sets `Stateless: true` and `PropagateRequestCancellation: true`, or documents why it needs sessions and how they survive replicas.
- [ ] The HTTP handler sits behind `auth.RequireBearerToken`, the verifier checks signature or introspection, audience and expiry, and protected resource metadata is served.
- [ ] Tool handlers take the caller's identity from `req.Extra.TokenInfo` (or, for stdio, from the environment), never from arguments.
- [ ] Every downstream call uses the handler's `ctx`, and each tool runs under a fail-fast concurrency cap and a per-call deadline.
- [ ] Tool failures return a plain `error` with a model-actionable message and no internal details; only protocol faults return `*jsonrpc.Error`.

## When not to apply

- **A local developer tool over stdio.** A server the editor launches as a subprocess has no load balancer and no network peer: skip rules 3 and 4, read credentials from the environment, and keep rules 1, 2, 5 and 6, because the model is still the one choosing the arguments.
- **A sandboxed code-execution tool.** If running code is the product (a build or test runner for an agent), run it in a disposable sandbox with no credentials, no network and resource limits, never in the server's own process or account, and still pass an allow-listed command and typed arguments rather than a shell string.
- **Server-to-client features.** A server that must send sampling or elicitation requests, or that serves clients pinned to protocol 2025-11-25 or earlier that need sessions, can run stateful; then route by `Mcp-Session-Id` at the balancer, set `SessionTimeout`, set `TokenInfo.UserID` so sessions are bound to users, and accept that a replica restart drops its sessions.
- **Read-only public data.** A server that only exposes public, non-personal data may skip OAuth; keep a rate limit at the edge and rules 1, 2, 5 and 6.
- **Authentication handled by a gateway.** When an API gateway or mesh validates the OAuth token, keep `RequireBearerToken` with a verifier that trusts only the gateway's signed identity, so a request that bypasses the gateway is still rejected.
- **A one-off internal prototype.** Rules 1, 2 and 4 still hold; the concurrency cap can start as a generous fixed number until load tests give a real one.
