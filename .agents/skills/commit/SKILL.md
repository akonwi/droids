---
name: commit
description: Inspect, validate, and commit changes using Conventional Commits. Use whenever the user asks to commit, create a commit, prepare a commit, or choose a commit message.
---

# Commit

Create focused, validated commits that follow the [Conventional Commits](https://www.conventionalcommits.org/) specification.

## Process

1. Inspect `git status`, staged and unstaged diffs, and recent commit style.
2. Identify the intended change and detect unrelated modifications.
3. Follow repository guidance in `AGENTS.md`.
4. Run the required validation when practical: `gofmt -l .` (must be empty), `go build ./...`, `go vet ./...`, and `go test ./...`.
5. Stage only files belonging to the intended change. Do not include unrelated changes without user approval.
6. Commit with a concise Conventional Commit message.
7. Report the commit hash, subject, and validation result.

If there is nothing to commit, say so. Do not amend an existing commit, bypass hooks, force operations, or push unless explicitly requested.

## Message Format

```text
type(scope): imperative summary
```

Omit the scope when it adds no clarity:

```text
type: imperative summary
```

Use a lowercase type and a concise imperative summary without a trailing period.

Common types:

- `feat`: add user-visible behavior
- `fix`: correct faulty behavior
- `refactor`: restructure code without changing behavior
- `perf`: improve performance
- `test`: add or revise tests
- `docs`: documentation only
- `build`: build system or dependencies
- `ci`: continuous integration configuration
- `chore`: maintenance not covered above
- `revert`: revert an earlier commit

Use `!` before the colon and add a `BREAKING CHANGE:` footer for breaking changes:

```text
feat(provider)!: rename StreamOptions to CallOptions

BREAKING CHANGE: `droids.StreamOptions` is now `droids.CallOptions`.
```

## Scopes

Prefer a scope that names the affected layer or file group:

- `provider` — provider registry / model abstraction (`provider.go`, `provider_openai.go`)
- `loop` — the agent loop (`loop.go`)
- `droid` — the facade (`droid.go`)
- `tool` — tool definitions (`tool.go`)
- `storage` — persistence seam (`storage.go`)
- `event` / `stream` — event and streaming protocols
- `chat` — the CLI example (`cmd/chat`)

## Choosing the Message

Describe the purpose of the complete diff, not the editing action or file names. Prefer specific subjects such as:

```text
feat(provider): add anthropic-messages translator
fix(loop): drain steering messages before the first turn
refactor(tool): derive JSON schema from Tool[Args]
docs: document the three-layer architecture
```

Avoid vague subjects such as `update files`, `fix stuff`, or `changes`.

For a change with multiple independent concerns, propose separate commits rather than forcing them into one message.
