---
name: project-layout
description: Use when creating a new Go backend service, adding a package or directory to one, deciding where an interface or shared helper goes, or deciding whether to split a service. Covers cmd/ and internal/, packages by domain instead of by layer, consumer-side interfaces and concrete return types, package naming without util or pkg/, and when to split a modular monolith into services.
---

# Project layout

## The decision

Every Go service starts with a directory tree, and the tree decides which code can import which. A layout copied from another language (`controllers/`, `services/`, `repositories/`, `models/`) or from the unofficial `golang-standards/project-layout` repo (`pkg/`, `util/`) looks organized on day one and turns every feature change into edits across four packages, import cycles that force a `common` package, and interfaces nobody can fake. This skill fixes where binaries, domain code, interfaces and shared code go in a backend service, and when one module with one binary should become several services.

## The rule

1. Put each binary in `cmd/<name>/main.go` and keep it to wiring: read config, build the domain packages, connect them, run.
2. Put every other package under `internal/`, never in `pkg/` or at the module root, and move code to a separate module only when another repository must import it.
3. Organize `internal/` by domain (`internal/orders`, `internal/billing`), with each package owning its types, logic and storage, not by layer (`models`, `services`, `repositories`).
4. Declare an interface in the package that consumes it, unexported and listing only the methods that package calls.
5. Return concrete types from constructors (`func New(...) *Service`), not interfaces.
6. Name packages after what they provide, never `util`, `utils`, `common`, `helpers`, `shared`, `base` or `models`.
7. Keep one module and one deployable until a domain has a measured need for independent scaling, deploy cadence or failure isolation, then split along an existing `internal/` package boundary.

## Why

**Rule 1.** The official module layout guide (go.dev/doc/modules/layout) places commands under `cmd/` and server logic under `internal/`. A `main` package cannot be imported, so any rule written there cannot be tested from another package and has to be copied when a second binary (a worker, a migration command) needs it. A `run(ctx, ...) error` function called from `main` keeps the wiring itself testable.

**Rule 2.** The Go toolchain refuses to build an import of `a/b/internal/c` from code outside the tree rooted at `a/b`. That is what lets you rename, split and delete packages in a service without breaking anyone. Code at the module root or in `pkg/` has no such protection: the first other team that imports it turns your refactor into a breaking change. The layout guide recommends keeping packages in `internal` "as much as possible" and splitting shareable code into its own module; it never mentions `pkg/`, which adds a path segment that says nothing. A separate module gets its own version and changelog, which is what a real consumer needs.

**Rule 3.** Layer packages couple every feature to every layer. Adding a field to an order touches `models`, `repositories`, `services` and `handlers`, and a review cannot see the feature in one place. Layers also produce import cycles, which Go rejects at compile time: `services` imports `repository`, then `repository` needs a type from `services`, and the fix is a `models` or `common` package that everything imports. A domain package keeps the things that change together in one place and hides its storage behind its own methods, so `orders` cannot read `billing`'s tables. This rule is practitioner consensus (Ben Johnson's "Standard Package Layout", Peter Bourgon's "Go best practices"), not an official Go document; it follows from the Google style guide's advice to keep coupled code together and name packages by what they do.

**Rule 4.** The Google Go style guide says the consumer of an interface should define it, and to avoid creating interfaces until a real need exists. When `billing` exports a `BillingService` interface with every method, `orders` depends on methods it never calls, every fake implements all of them, and adding a method breaks every fake in the repository. A one-method `charger` interface in `orders` documents exactly what orders needs, makes the fake four lines long, and means `orders` does not import `billing` at all. Go interfaces are satisfied implicitly, so `main` passing `*billing.Service` to `orders.New` is the compile-time check that the two still fit.

**Rule 5.** "Accept interfaces, return concrete types" (Google style guide, with `error` as the accepted exception). A constructor returning an interface hides the type's other methods and its documentation, forces a type assertion when a caller needs one more method, and makes the provider the owner of an abstraction it cannot know the shape of.

**Rule 6.** A package name is part of every call site: `billing.Charge` says what happens, `utils.Charge` does not. Catch-all packages attract unrelated code because nothing about the name says no, everything ends up importing them, and they become the hub of the import graph. The Google style guide lists `util`, `common` and `helper` as names to avoid. When a helper serves one domain, put it in that domain; when it is a real shared capability, name the package after it (`money`, `httpserver`, `postgres`).

**Rule 7.** A function call becomes a network call, with latency, retries, timeouts, partial failure, and a distributed transaction where there used to be one database transaction. Each service also brings its own deploy pipeline, minimum replicas, dashboards and on-call surface, a fixed cost that dominates the bill for a small team. Domain packages already give you most of the modularity: if `orders` only talks to billing through a `charger` interface, replacing `*billing.Service` with an HTTP client later is a change in `main` and one new package, not a rewrite.

The cost: domain packages sometimes repeat a small type (an `Order` in orders, an order reference in billing) instead of sharing one, and `main` grows a few lines of wiring per package. That duplication is what keeps the packages independent; share a type only when both sides must change together.

A layout that follows these rules:

```text
cmd/app/main.go          # wiring only
internal/billing/        # billing types, logic, storage
internal/orders/         # orders types, logic, storage, its charger interface
go.mod
```

## Good example

See [`examples/good/`](examples/good/).

```go
// charger is what orders needs from billing, declared here by the consumer.
type charger interface {
	Charge(ctx context.Context, customerID string, cents int64) (string, error)
}

// New returns a concrete *Service, not an interface.
func New(c charger) *Service {
	return &Service{charger: c, orders: make(map[string]Order)}
}
```

```go
func run(ctx context.Context, logger *slog.Logger) error {
	bill := billing.New(10_000)
	ord := orders.New(bill) // *billing.Service satisfies orders' charger at compile time.

	o, err := ord.Place(ctx, "cus_1", 2_500)
	if err != nil {
		return err
	}
	logger.Info("order placed", "order_id", o.ID, "charge_id", o.ChargeID)
	return nil
}
```

`internal/orders` declares the one method it calls and never imports `internal/billing`; `cmd/app` is the only package that sees both. The orders test fakes `charger` with a single method, and `billing.New` returns `*Service`, so callers see every method and pick the ones they need.

## Bad example

See [`examples/bad/`](examples/bad/).

```go
// MISTAKE: the provider declares the interface with every method it has, so
// each consumer depends on all of them and each fake must implement all of them.
type BillingService interface {
	Charge(ctx context.Context, customerID string, cents int64) (models.Charge, error)
	Refund(ctx context.Context, chargeID string) error
	ListCharges(ctx context.Context, customerID string) ([]models.Charge, error)
}

// MISTAKE: returns the interface; adding a method breaks every fake, and
// callers cannot see the concrete type's documentation or extra methods.
func NewBillingService(store *repository.Store) BillingService {
	return &billingService{store: store}
}
```

```go
	// MISTAKE: orders reads billing's storage directly because they share a
	// layer package and a store; billing can no longer change how it keeps charges.
	if _, ok := s.Store.Charges[c.ID]; !ok {
		return models.Order{}, fmt.Errorf("place order for %s: charge %s missing", customerID, c.ID)
	}
```

The bad module has the layout that gets generated most often for a "clean" Go service: `pkg/utils`, `internal/models`, `internal/repository` and `internal/services`, with a provider-side interface, a constructor returning it, one store shared by every feature, and a discount rule written in `main`. It compiles and runs; the cost shows up on the second feature, when every change touches every layer.

## Checklist

When reviewing code under this skill, confirm:

- [ ] Every binary is `cmd/<name>/main.go`, and `main` contains wiring only, with no business rules.
- [ ] All non-`main` packages are under `internal/`; there is no `pkg/` directory and no library package at the module root.
- [ ] Packages under `internal/` are named after domains or capabilities, not layers (`models`, `services`, `repositories`, `handlers`, `controllers`).
- [ ] No package is named `util`, `utils`, `common`, `helpers`, `shared` or `base`.
- [ ] Each domain package owns its storage; no package reads or writes another domain's tables or in-memory state.
- [ ] Interfaces are declared in the consuming package, are unexported unless another package must name them, and list only the methods that package calls.
- [ ] Constructors return concrete types (or `error` as the second result), not interfaces.
- [ ] Domain packages reach each other through consumer interfaces wired in `main`, and no `models` or `common` package exists only to break an import cycle.
- [ ] A proposal to split out a service names the measured scaling, deploy-cadence or isolation need and the `internal/` package it splits along.

## When not to apply

- **A single small command.** A 200-line CLI or tool with one package can keep `main.go` at the module root with no `cmd/` or `internal/`; the layout guide says `cmd/` is not necessary in a repository that consists only of commands. Add `internal/` when a second package appears.
- **A library module.** If the repository exists so others can import it, the exported packages are the product: put them at the module root or in named subdirectories, still without `pkg/`, and keep only implementation details in `internal/`.
- **An interface the provider must own.** When a package defines a contract that many unrelated implementations satisfy (`io.Reader`, a plugin or driver API, `http.Handler`), the provider declares it. That is a standard extension point, not a dependency you are faking.
- **Constructors that pick an implementation at run time.** A factory that returns one of several types based on config (a storage backend chosen by flag) returns an interface by design; keep that interface small and owned by the package that defines the choice.
- **Generated code.** sqlc, protobuf and OpenAPI generators emit their own layout; keep generated code in a package named after what it is (`internal/orders/db`, `internal/api/gen`) and do not hand-edit it to match these rules.
- **Separate teams or hard isolation requirements.** When two teams already deploy on different schedules, or a component handles regulated data that must run in its own security boundary, split into services now and record the reason; the cost in rule 7 is then the price of a real requirement.
