# AI Agent Guidelines

Short, mandatory rules for any AI agent modifying this codebase.
The full standard — structure, naming, comments, patterns — is
`docs/CODEBASE.md`. It wins over any habit or external convention.

## 1. Core Mandates

- **Follow `docs/CODEBASE.md` exactly**: every exported identifier gets a
  doc comment; comments explain why, not what; no dead code; simple over
  clever.
- **No Global Mutables**: no global variables, no `init()` side effects.
  Construct everything in `internal/app/app.go` and pass it down. The one
  sanctioned exception is `internal/tui/styles.go`'s theme/style vars
  (`colorPrimary` and friends) — lipgloss's idiomatic pattern; see the
  comment there.
- **Provider isolation**: provider knowledge lives only under
  `internal/provider/**`. Providers declare optional capabilities
  (`AudioLanguagesSource`, `MovieEpisodeFlow`, `FeatureSource`,
  `StreamingProvider`) instead of TUI/service branching on names or modes.
  New providers are registered only in `internal/provider/defaults`.
- **Type ownership**: providers keep their API types private in their own
  package; shared wire DTOs live in `internal/provider`; playback
  aggregates in `internal/model`. No field-for-field duplicate structs, no
  untyped `map[string]any` escape hatches on shared types.
- **Strict Typing**: use `provider.ContentType` and the shared media-type
  helpers. No raw strings for modes.
- **Shared HTTP Client**: use `internal/httpclient`. Never instantiate a
  raw `http.Client`, except non-idempotent local calls (e.g. aria2 JSON-RPC)
  where retries could double-submit — see CODEBASE.md §12.

## 2. Refactor Logic (The "Must Needed" Flexibility)

Improvements are encouraged when they standardize inconsistency, remove
redundancy, or improve cross-platform reliability — but verify the build
after every change:

```sh
go vet ./... && go build ./... && staticcheck ./... && go test ./...
```

## 3. Communication Patterns

- When creating a new provider, explain the resolution strategy
  (e.g. "HTML scraping vs GraphQL").
- When modifying the TUI, ensure the corresponding behavior is expressed as
  a service/provider capability rather than new hardcoded branches.

## 4. Error Handling

Wrap with context: `fmt.Errorf("do something: %w", err)`. Use sentinel
errors from `internal/provider/errors.go` for flow control.

## 5. Logging

Use structured `logging.Debug/Info/Warn/Error` with key/value pairs and
`logging.With` to scope components. Never `fmt.Printf` or `log.Printf`
for application logic.

## 6. Commit Message Convention (MANDATORY)

Conventional Commits — the auto-generated changelog depends on it:

```txt
<type>(<scope>): <description>
```

Types: `feat`, `fix`, `docs`, `refactor`, `perf`, `test`, `chore`, `ci`.
Scope encouraged: `provider/miruro`, `player/mpv`, `tui`.

Examples:

```txt
feat(provider): add nyaa si provider
fix(player/mpv): handle socket disconnect gracefully
docs: update installation instructions
```
