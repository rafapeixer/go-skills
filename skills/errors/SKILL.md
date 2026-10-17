---
name: errors
description: Use when writing or reviewing Go code that creates, wraps, returns, inspects, logs or maps errors to HTTP status or exit codes. Covers wrapping with %w, sentinel vs typed errors, errors.Is and errors.AsType, translating errors at package boundaries, handling an error once, and errors.Join.
metadata:
  verified: 2026-10-17
---

# Errors

## The decision

Every Go function that can fail returns an `error`, so the real decision is not whether to handle errors but what each error carries and who acts on it. Get this wrong and the failure shows up far from the code: callers cannot tell "not found" from "database down", HTTP handlers return 500 for bad input, logs show the same failure five times without the one ID you need, and a refactor that rewords a message silently changes behavior. This skill fixes how errors are created, wrapped, inspected and finally handled.

## The rule

1. Wrap with `fmt.Errorf("<operation> <key input>: %w", err)`: name what was being done and with what, never "failed to" or "error:".
2. Use a sentinel (`var ErrX = errors.New("...")`) for categories callers branch on, and a type for errors that carry data.
3. Inspect with `errors.Is` and `errors.AsType[*T]`, never with `==` on a possibly wrapped error, never with `err.Error()`.
4. Translate a dependency's errors into your own at the package that hides that dependency, so callers never import the driver to check an error.
5. Handle an error exactly once: return it with context or log it, never both.
6. Map errors to HTTP status codes or exit codes in a single function at the edge.
7. Check `err` before touching any other return value.
8. Return the error from `Close` on anything you wrote to, combined with `errors.Join`.

## Why

**Rule 1.** The wrapped chain is the stack trace Go does not give you. `rename user 42: query users: connection refused` tells you what failed and for which input without opening a debugger. "failed to" adds length and no information, and every layer repeating it turns the message into noise. `%w` (not `%v`) keeps the original error inspectable; with `%v` the chain becomes a flat string and `errors.Is(err, ErrNotFound)` returns false three layers up.

**Rule 2.** A sentinel is cheap and comparable: `ErrNotFound`, `ErrConflict`, `ErrUnauthorized`. When the caller needs more than the category (which field failed validation, which limit was exceeded), a struct type carries it. Creating `errors.New("not found")` inside a function returns a new value every call, so callers can only match it by text.

**Rule 3.** Wrapping changes identity: `err == ErrNotFound` is false once anyone adds context. `errors.Is` walks the chain. For types, `errors.AsType[*ValidationError](err)` (Go 1.26) returns the typed value and a bool, and a wrong type parameter is a compile error. The older `errors.As(err, &target)` takes `any`, so passing a value where only the pointer implements `error` compiles and panics at run time; `go vet` catches some of these, not all. Matching on `err.Error()` breaks the first time someone improves a message.

**Rule 4.** If your store returns `pgx.ErrNoRows` wrapped with `%w`, every handler that wants a 404 must import pgx, and switching drivers becomes a change across the codebase. The store owns that knowledge: it turns `pgx.ErrNoRows` into `ErrNotFound`, and a unique-violation code into `ErrConflict`. Inside one package or one module, keep wrapping with `%w`; at the boundary that hides an implementation, translate, or wrap with `%v` when the cause should be visible in logs but not part of your API.

**Rule 5.** Logging and returning means the same failure appears once per layer, each copy missing the context the next layer adds. Log where the error stops travelling: the HTTP handler or middleware, the worker loop, `main`. Everywhere else, add context and return.

**Rule 6.** When every handler decides its own status code, the same `ErrNotFound` becomes 404 in one endpoint and 500 in another, and internal messages leak to clients. One `statusFor(err)` (or `exitCode(err)`) makes the mapping reviewable in one place and testable with a table.

**Rule 7.** When `err != nil`, the other results are undefined: often a nil pointer, sometimes a partial value. Since Go 1.25 the compiler no longer delays nil checks, so code that read a result before checking `err` and happened to work now panics, as it always should have.

**Rule 8.** For files and buffered writers, `Close` (or `Flush`) is where the last write actually happens. `defer f.Close()` on a file you wrote to throws away a disk-full or network error and reports success for data that never landed. Use a named return and `err = errors.Join(err, f.Close())` in the defer. For read-only resources, ignoring the `Close` error is fine.

The cost: wrapping at every layer makes messages long. That is the trade-off for knowing where the failure happened. Keep each wrap short (operation plus the one input that identifies it) and the chain stays readable.

## Good example

See [`examples/good/`](examples/good/).

```go
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

func statusFor(err error) int {
	if _, ok := errors.AsType[*ValidationError](err); ok {
		return http.StatusBadRequest
	}
	if errors.Is(err, ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}
```

The service adds the operation and the input, keeps the cause with `%w`, and does not log. The edge maps the error to a status code in one place and logs it once, producing `"err":"rename user 2: not found"` with status 404.

## Bad example

See [`examples/bad/`](examples/bad/).

```go
u, err := findUser(id)
// MISTAKE: uses the result before checking err; u is nil for a missing user, so this panics.
log.Printf("renaming %s", u.Name)
if err != nil {
	// MISTAKE: logs and returns; every caller up the stack logs the same failure again.
	log.Printf("failed to find user: %v", err)
	// MISTAKE: %v flattens the chain, so errors.Is and errors.As can no longer see the cause.
	return nil, fmt.Errorf("failed to rename: %v", err)
}

// MISTAKE: the target is a value, but only *ValidationError implements error,
// so errors.As panics at run time. errors.AsType makes this a compile error.
var ve ValidationError
if errors.As(err, &ve) {
	return http.StatusBadRequest
}
// MISTAKE: string matching breaks as soon as anyone rewords the message or wraps it.
if err.Error() == "not found" {
```

Each of these is common in code written quickly or generated from older examples: `errors.New` inside the function, `%v` instead of `%w`, log-and-return, string comparison, and `errors.As` with the wrong target. The file compiles; `go vet` flags only the `errors.As` line.

## Checklist

When reviewing code under this skill, confirm:

- [ ] Every returned error that came from a call is wrapped with the operation and its identifying input, using `%w` inside the module.
- [ ] No error message starts with "failed to", "error:" or "unable to".
- [ ] Categories callers branch on are package-level sentinels; errors carrying data are types.
- [ ] No code compares errors with `==` (except `io.EOF` straight from a reader) or inspects `err.Error()`.
- [ ] Type checks use `errors.AsType[*T]`, not `errors.As`.
- [ ] No caller outside the data layer imports a driver package only to inspect an error.
- [ ] No function both logs an error and returns it.
- [ ] Errors are mapped to status or exit codes in one function, covered by a table test.
- [ ] No return value is used before `err` is checked.
- [ ] Writers and files opened for writing return their `Close` error.

## When not to apply

- **`io.EOF` and similar documented sentinels returned unwrapped by a reader.** `err == io.EOF` is the documented contract of `io.Reader` and is fine on the direct result of `Read`. Use `errors.Is` once any wrapping might be involved.
- **Hot loops where the error is expected and immediately handled.** A parser trying alternatives can return a preallocated sentinel without wrapping, because the error never leaves the function and wrapping would allocate on every attempt.
- **Errors that must not reveal causes.** Authentication failures should return one generic error to the caller ("invalid credentials") and log the specific cause internally, so the API does not tell attackers whether the user exists.
- **Small CLIs and scripts.** A 50-line tool can wrap and print errors in `main` and exit 1 without a status-mapping function or custom types.
- **Go older than 1.26.** If you are stuck below the floor of this repository, use `errors.As` with a pointer target of the exact type that implements `error` (`var ve *ValidationError; errors.As(err, &ve)`) and run `go vet`.
