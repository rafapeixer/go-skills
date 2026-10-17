---
name: <skill-name>
description: Use when <the concrete situation that should trigger this skill: writing X, reviewing Y, deciding Z>. Covers <the 3 to 5 sub-topics in one line>.
---

# <Skill title>

## The decision

<One paragraph. Name the decision a Go developer faces here, when it comes up, and why getting it wrong hurts. No introduction about how important the topic is. Start at the decision.>

## The rule

1. <One sentence. Imperative. "Wrap errors with the operation name, not the function name.">
2. <One sentence.>
3. <One sentence.>
4. <...>

<Keep it to 4 to 8 rules. If you need more, this is two skills.>

## Why

<Short. For each rule that is not obvious, say what breaks if you don't follow it. Mention the cost of the alternative honestly: "wrapping every error adds noise to logs; the trade-off is that you lose the call path when you don't".>

## Good example

See [`examples/good/`](examples/good/).

```go
// 5 to 15 lines excerpted from examples/good/. Must be real code that is in the folder.
```

<One or two sentences on what makes this the right shape.>

## Bad example

See [`examples/bad/`](examples/bad/).

```go
// 5 to 15 lines excerpted from examples/bad/.
// Each mistake has an inline comment: // MISTAKE: <what is wrong and what it causes>
```

<The bad example must be a mistake agents and junior developers actually make, not a strawman.>

## Checklist

When reviewing code under this skill, confirm:

- [ ] <Yes/no question. "Every error returned across a package boundary is wrapped with context.">
- [ ] <Yes/no question.>
- [ ] <Yes/no question.>
- [ ] <Yes/no question.>
- [ ] <Yes/no question.>

<5 to 10 items. This section is the only checklist; there is no separate checklist file.>

## When not to apply

- <A case where the rule bends, and what to do instead. "In a 50-line CLI with no packages, skip the internal/ split.">
- <Another case.>

<Every rule has exceptions. A skill with none here is not finished.>
