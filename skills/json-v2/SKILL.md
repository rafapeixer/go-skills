---
name: json-v2
description: Use when writing or reviewing Go code that marshals or unmarshals JSON, adds json struct tags, writes MarshalJSON or UnmarshalJSON methods, decodes request bodies, inspects JSON errors, upgrades a service to Go 1.27, or considers encoding/json/v2, jsontext or a third-party JSON library such as jsoniter or sonic. Covers staying on encoding/json at the Go 1.26 floor and gating encoding/json/v2 code, omitzero vs omitempty, branching on JSON error types instead of text, testing v1 to v2 behavior differences (duplicate names, invalid UTF-8, case, nil slices), MarshalJSONTo and UnmarshalJSONFrom, strict decoding, third-party libraries, and the GOEXPERIMENT=nojsonv2 opt-out.
---

# JSON with encoding/json and encoding/json/v2

## The decision

Every Go service serializes JSON, and Go 1.27 changed what that means. It added `encoding/json/v2` and `encoding/json/jsontext`, and it rebuilt `encoding/json` (v1) on top of the v2 engine: the same behavior, faster unmarshaling, different error text. A module whose floor is Go 1.26 now builds against two engines, the original v1 on 1.26 and v1-on-v2 on 1.27, and can call a third API only on 1.27. The decisions are which package to call, which struct tags mean "absent", how custom types serialize, how strict request decoding is, and how code and tests inspect JSON errors so the upgrade does not change behavior silently. Bounding the request body is the `http-server` skill; general error wrapping and `errors.AsType` are the `errors` skill.

## The rule

1. Keep calling `encoding/json` while the floor is Go 1.26, and put `encoding/json/v2` code only in files gated with `//go:build go1.27 && goexperiment.jsonv2` (Go 1.27+), never behind `GOEXPERIMENT=jsonv2` on 1.26.
2. Tag a field `omitzero` when its zero Go value means "absent" (`time.Time`, structs, numbers, bools, pointers), and keep `omitempty` for strings, slices and maps whose empty value means "absent".
3. Branch on JSON errors by type or sentinel (`*json.SyntaxError`, `*json.UnmarshalTypeError`, your own sentinels; on Go 1.27+ also `jsontext.ErrDuplicateName` and `json.ErrUnknownName` from v2), never on `err.Error()` text, in code or in tests.
4. Before moving a call from v1 to v2 (Go 1.27+), pin with tests the inputs whose meaning changes (duplicate names, invalid UTF-8, case-mismatched names, nil slices and maps, `time.Duration`), and restore a v1 behavior with its option only for the caller that needs it.
5. Write custom marshalers that hand values to the encoder, never JSON assembled with `+`, `fmt.Sprintf` or `strconv.Quote`, and on Go 1.27+ add `MarshalJSONTo` and `UnmarshalJSONFrom` beside the v1 methods with a test that both produce the same JSON and the same errors.
6. Decode request bodies whose schema you own strictly: exactly one value, no trailing data, unknown fields rejected with `DisallowUnknownFields` (or `json.RejectUnknownMembers(true)` in v2, Go 1.27+), after bounding the body as `http-server` requires.
7. Stay on the standard library for JSON, and adopt jsoniter, sonic, goccy/go-json or easyjson only when a CPU profile on Go 1.27 shows JSON as the bottleneck and a benchmark on your own payloads shows the gain.
8. Treat `GOEXPERIMENT=nojsonv2` (Go 1.27+) as a temporary rollback with a filed upstream issue and a removal date, never as a permanent build setting.

## Why

**Rule 1.** Go 1.27 runs `encoding/json` on the v2 implementation. The release notes say "marshaling and unmarshaling behavior is preserved, but the exact text of error messages may differ", marshal is "broadly at parity" and unmarshal is "significantly faster" (the jsonv2 blog post measured up to 10x). So a service on the 1.26 floor gets the speedup by building with 1.27, without touching code. Calling `encoding/json/v2` directly is a different matter: on Go 1.26 it exists only under `GOEXPERIMENT=jsonv2`, with a pre-release API that Go 1.27 changed. The `inline` tag option became `embed`; the `format` and `unknown` tag options, `DiscardUnknownMembers` and `SkipFunc` were removed; `jsontext.Token.Int` and the other numeric accessors now return an error too. Code written against the experiment does not compile, or worse, compiles and means something else, on 1.27. Gate v2 files with both constraints: `go1.27` keeps out the 1.26 experiment, and `goexperiment.jsonv2` keeps the file out of a 1.27 build with `GOEXPERIMENT=nojsonv2`, where `encoding/json/v2` and `jsontext` are not available at all ("build constraints exclude all Go files"). Once the floor is 1.27, new code calls v2, as the v1 package documentation itself recommends.

**Rule 2.** `omitempty` asks "is this empty?", and v1 never considers a struct empty, so `time.Time{}` goes out as `"0001-01-01T00:00:00Z"` and clients read it as a real date. `omitzero` (Go 1.24, supported identically in v1 and v2) omits the zero value, using an `IsZero() bool` method when the type has one, which is exactly what `time.Time` needs. The split also survives the move to v2: v2 redefines `omitempty` in terms of JSON (omit `null`, `""`, `{}`, `[]`), so `omitempty` on a bool or number stops omitting `false` and `0`. The v1 package documentation says existing `omitempty` on bools, numbers, pointers and interfaces "should migrate to specifying `omitzero`". Note the remaining difference: `omitzero` omits only a nil slice or map, `omitempty` also omits an empty one.

**Rule 3.** Go 1.27 changed the text without changing the types. Measured on the same input: Go 1.26 reports `cannot unmarshal string into Go struct field Source.source.port`, Go 1.27 reports `Event.source.port`; a slice path `l.x` became `l.0.x`; `nul` changed from `invalid character ' ' in literal null` to `unexpected end of JSON input`. Even the `Struct` field of `*json.UnmarshalTypeError` now names the root type instead of the struct holding the field. A `strings.Contains(err.Error(), ...)` branch silently stops matching after the toolchain bump, and a test that compares error strings fails for a change in the standard library, not in your code. Use `errors.AsType` on the types and `errors.Is` on sentinels; v2 documents its `SemanticError` and `SyntacticError` contents as subject to change, so treat their `Error()` the same way. v1 reports an unknown field with no dedicated type: let it fall through to a generic 400 rather than parsing its text.

**Rule 4.** v2 changes meaning, not just speed. By default it rejects invalid UTF-8 (v1 replaces it with U+FFFD) and duplicate names (v1 keeps the last), matches field names case-sensitively (v1 accepts `"ID"` for `json:"id"`), encodes nil slices and maps as `[]` and `{}` (v1: `null`), errors on `time.Duration` (v1: nanoseconds), requires arrays of exact length and marshals maps in non-deterministic order. The strict defaults are a security fix: duplicate names and case folding are how two services that parse the same request read different users from it. So the migration is a set of tests that feed both paths the ambiguous inputs and assert what each does; then switch, and if one client really sends `"ID"`, restore `json.MatchCaseInsensitiveNames(true)` on that call only. Switching every protection back on for every caller (`jsontext.AllowDuplicateNames`, `jsontext.AllowInvalidUTF8`) makes the migration pointless.

**Rule 5.** A `MarshalJSON` built with `fmt.Sprintf("%q", s)` emits Go escapes (`\a`, `\xff`) that are not JSON; `encoding/json` rejects the output at run time, for whichever value first contains such a character. Returning `json.Marshal(value)` lets the encoder quote and escape. On Go 1.27, implement `MarshalJSONTo(*jsontext.Encoder) error` and `UnmarshalJSONFrom(*jsontext.Decoder) error`: they write and read tokens on the stream instead of an intermediate `[]byte`, and the jsonv2 post reports one Kubernetes service getting faster "by orders of magnitude" after moving its recursive `UnmarshalJSON` to `UnmarshalJSONFrom`. Keep the v1 methods while the floor is 1.26, and test that both sets agree: on Go 1.27, `encoding/json` (v1) calls `MarshalJSONTo` and `UnmarshalJSONFrom` in preference to the v1 methods, so the same `json.Unmarshal` call runs different code on 1.26 and 1.27. If the two methods return different errors for the same bad input, a handler that maps errors by type answers the same request differently after the toolchain bump; the good example has both return the same sentinel.

**Rule 6.** v1's `Decoder.Decode` reads one value and stops, so `{"id":"e1"} trailing` succeeds, and without `DisallowUnknownFields` a client typo such as `"deleted_ta"` is dropped while the request reports success. Check for trailing data with one more `dec.Token()` that must return `io.EOF`. In v2, `UnmarshalRead` "consumes the entirety of io.Reader until io.EOF" and rejects a second value itself, and `json.RejectUnknownMembers(true)` reports `json.ErrUnknownName`. The cost is that adding a field becomes a coordinated change: a client that sends a new field before the server knows it gets 400. That is the right default for endpoints you own; see the exceptions for public and versioned APIs.

**Rule 7.** Third-party JSON libraries were adopted mostly for decode speed, and the Go 1.27 unmarshal speedup applies to every caller of `encoding/json` with no code change, so a comparison made on an older Go no longer holds. Each one is a second parser with its own answer to exactly the ambiguous inputs v2 hardens (duplicates, invalid UTF-8, case), its own error types, and a dependency to patch. Swapping it in "because JSON is slow" without a profile usually moves no latency at all, because the time was in I/O or the database. If the profile does show JSON, benchmark your real payloads with `b.Loop()` and `benchstat` on Go 1.27 before and after; the `testing` skill covers benchmarks.

**Rule 8.** Go 1.27 lets you build with `GOEXPERIMENT=nojsonv2` to restore the original v1 implementation, and the release notes say the opt-out "is expected to be removed in a future release" and ask you to file an issue. A build that depends on it is a build that breaks on a future upgrade, and it also turns off the 1.27 unmarshal speedup. Use it to unblock a release, record why, and remove it once the incompatibility is fixed on either side.

## Good example

See [`examples/good/`](examples/good/).

```go
type Event struct {
	ID        string    `json:"id"`
	Amount    Cents     `json:"amount"`
	Note      string    `json:"note,omitempty"` // empty string: leave it out
	CreatedAt time.Time `json:"created_at"`
	DeletedAt time.Time `json:"deleted_at,omitzero"` // zero time: leave it out (Go 1.24+)
	Source    Source    `json:"source,omitzero"`     // zero struct: leave it out
}
```

```go
func decodeStrict(r io.Reader, v any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errTrailingData
	}
	return nil
}
```

```go
func (c Cents) MarshalJSONTo(enc *jsontext.Encoder) error {
	return enc.WriteToken(jsontext.String(c.String()))
}
```

The untagged files use only `encoding/json` and build on Go 1.26 and 1.27: `omitzero` tags, `MarshalJSON` that delegates to `json.Marshal`, strict decoding, and a `problem` function that picks the client message by error type. `v2_go127.go`, tagged `//go:build go1.27 && goexperiment.jsonv2`, adds `MarshalJSONTo`, `UnmarshalJSONFrom` and a v2 `decodeStrictV2`. The tests prove both method sets give the same JSON, that v2 rejects the duplicate-name, invalid-UTF-8 and case-mismatch inputs v1 accepts, and pin the one `UnmarshalTypeError` field that differs between engines in `engine_v1_test.go` and `engine_v2_test.go`.

## Bad example

See [`examples/bad/`](examples/bad/).

```go
	// MISTAKE: omitempty never omits a time.Time or any other struct, so every
	// live event is sent with "deleted_at":"0001-01-01T00:00:00Z" and clients
	// treat it as deleted. omitzero (Go 1.24+) is the tag that means this.
	DeletedAt time.Time `json:"deleted_at,omitempty"`
	// MISTAKE: "inline" is not a tag option. v1 never had one and v2 renamed its
	// experimental inline to embed before Go 1.27, so this field is silently
	// sent as a nested "Meta" object instead of being flattened.
	Meta   Meta   `json:",inline"`
```

```go
	// MISTAKE: matches message text. Go 1.27 reworded encoding/json errors
	// ("Go struct field Source.source.port" became "Event.source.port", slice
	// paths gained indexes), so this branch silently stops matching on upgrade.
	case strings.Contains(msg, "Go struct field Source."):
		return http.StatusUnprocessableEntity
```

The program also hand-builds a label with `%q`, which `encoding/json` rejects at run time, decodes with no unknown-field or trailing-data check, and, in a Go 1.27 file, "fixes" a v2 migration by turning off duplicate-name, UTF-8 and case checks for every caller. Its test compares the Go 1.26 error string: it compiles everywhere, passes on 1.26 and fails on 1.27, and the program answers 422 on 1.26 and 500 on 1.27 for the same request.

## Checklist

When reviewing code under this skill, confirm:

- [ ] Code that imports `encoding/json/v2` or `jsontext` in a module with a Go 1.26 floor sits in files tagged `//go:build go1.27 && goexperiment.jsonv2`, and nothing depends on `GOEXPERIMENT=jsonv2`.
- [ ] Every `time.Time`, struct, number, bool or pointer field meant to disappear when zero uses `omitzero`; `omitempty` appears only on strings, slices and maps.
- [ ] No struct tag uses `inline`, `format` or `unknown`; flattening a named field uses `embed` and only in v2 code.
- [ ] No code or test branches on or compares `err.Error()` from JSON; errors are inspected with `errors.AsType` or `errors.Is`.
- [ ] Every call moved from v1 to v2 has tests for duplicate names, invalid UTF-8, case-mismatched names and nil slices or maps, and any v1-compatibility option is scoped to one caller with a comment saying why.
- [ ] Custom `MarshalJSON` methods delegate to `json.Marshal` or write tokens; none builds JSON with `+`, `fmt.Sprintf` or `strconv.Quote`.
- [ ] A type with both `MarshalJSON` and `MarshalJSONTo` (or both unmarshal methods) has a test that they produce the same JSON and errors.
- [ ] Request bodies with an owned schema are decoded with unknown fields rejected and a trailing-data check, after `http.MaxBytesReader`.
- [ ] Any third-party JSON library comes with a Go 1.27 profile and a benchmark on real payloads that justify it.
- [ ] `GOEXPERIMENT=nojsonv2` appears, if at all, with a linked issue and a date to remove it.

## When not to apply

- **Public or versioned APIs with independent clients.** Ignore unknown fields so older servers accept newer clients (Postel's law at the edge), but keep rejecting trailing data, and log unknown members you drop if you need to see them. Webhook receivers for third-party payloads fall here too.
- **Interop with a peer that sends non-conforming JSON.** If a partner you cannot change sends duplicate names or Latin-1 bytes, opt that one decode path into `jsontext.AllowDuplicateNames(true)` or `jsontext.AllowInvalidUTF8(true)` with a test naming the partner; never set it as a package-wide default.
- **A module whose floor is already Go 1.27.** Drop the build tags, call `encoding/json/v2` directly, and drop the duplicate v1 methods unless the module must still build with `GOEXPERIMENT=nojsonv2` (rule 8).
- **A measured JSON hot path.** If a profile on Go 1.27 still shows JSON dominating CPU (large fan-out encoders, log shippers), generated code or a third-party encoder is legitimate for that one path, behind a small interface and with the stdlib path kept in tests as the reference.
- **Wire formats `omitzero` cannot express.** When a client must distinguish "absent" from "zero" or "null" (PATCH semantics), use a pointer or an explicit optional type with its own marshaler instead of either tag.
- **Throwaway tools and test fixtures.** A one-off script that dumps JSON for a human can use whatever tags and plain `json.Unmarshal` read best.
