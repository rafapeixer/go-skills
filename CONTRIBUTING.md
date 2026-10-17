# Contributing

**English** | [Português](CONTRIBUTING.pt-BR.md)

Thanks for helping. This repository is opinionated on purpose, so the bar for a change is a reason, not a preference.

## Changing a rule

1. Open an issue first. Say which rule, what breaks when people follow it, and what the rule should say instead.
2. Once agreed, open a PR that updates the rule, the examples and the checklist together. A rule that changes without its examples is not accepted.
3. Never weaken a rule to fit a bad example. Fix the example.

## Adding a skill

1. Check the planned list in the README and open an issue naming the decision the skill covers. One decision per skill.
2. Copy [templates/SKILL.template.md](templates/SKILL.template.md) to `skills/<name>/SKILL.md`. The folder name and the `name` field must match.
3. Write the `description` as "Use when ..." so agents know when to load it.
4. Add `examples/good/` and `examples/bad/`, each with its own `go.mod`:
   - module path `github.com/rafapeixer/go-skills/skills/<name>/examples/<good|bad>`
   - `go 1.26.0` directive
   - under 100 lines
5. Add the skill to the index in **both** `README.md` and `README.pt-BR.md`.

## Local checks

Run both before pushing. CI runs the same scripts.

```bash
bash scripts/validate.sh       # format: frontmatter, sections, length, README index
bash scripts/test-examples.sh  # good: gofmt + go vet + go test; bad: go build
```

## Commits and PRs

- One skill per PR. One logical change per commit.
- Commit messages: `skill(errors): add rule on wrapping with operation name`, `docs(readme): update skill index`, `ci: test against Go stable`.
- Any change to a README or CONTRIBUTING file updates its Portuguese or English counterpart in the same commit.
