# AI Agent Guidelines

This file is a mandatory set of rules for any AI agent modifying this codebase. 

## 1. Core Mandates
- **No Global Mutables**: Do not add global variables. Use dependency injection.
- **Dependency-Safe Wiring**: All new components MUST be instantiated in `internal/app/app.go` and passed down.
- **Strict Typing**: Use `provider.ContentType` for modes. Do not use raw strings.
- **Shared HTTP Client**: Use `internal/httpclient`. Never instantiate a raw `http.Client`.

## 2. Refactor Logic (The "Must Needed" Flexibility)
While these rules are strict, you are encouraged to propose or implement improvements IF:
1. It standardizes an existing inconsistency.
2. It removes redundant code.
3. It improves reliability across platforms (e.g., the Android MPV configuration hack).

**However**, you must always verify the build after any change:
`go vet ./... && go build ./... && staticcheck ./...`

## 3. Communication Patterns
- When creating a new provider, explain the resolution strategy (e.g., "HTML scraping vs. GraphQL").
- When modifying the TUI, ensure you have updated the corresponding service in `internal/service`.

## 4. Error Handling Style
- Always wrap errors with context: `fmt.Errorf("do something: %w", err)`.
- Use sentinel errors from `internal/provider/errors.go` for flow control.

## 5. Logging Style
- Use `logging.Debugf`, `logging.Infof`, etc.
- Never use `fmt.Printf` or `log.Printf` for application logic.

## 6. Commit Message Convention (MANDATORY)
Every commit MUST follow the [Conventional Commits](https://www.conventionalcommits.org/) format. The auto-generated changelog depends on this.

```
<type>(<scope>): <description>
```

Allowed types:
| Type | When to use | Changelog section |
|---|---|---|
| `feat` | New feature | 🚀 Features |
| `fix` | Bug fix | 🐛 Fixes |
| `docs` | README, comments, AGENTS.md changes | 📖 Docs |
| `refactor` | Code change that isn't a feature or fix | ♻️ Refactors |
| `perf` | Performance improvement | ⚡ Performance |
| `test` | Adding or fixing tests | ✅ Tests |
| `chore` | Deps, tooling, CI, build config | 🔧 Chores |
| `ci` | CI/CD workflow changes | 🔧 Chores |

Scope is optional but encouraged (e.g., `provider/miruro`, `player/mpv`, `tui`).

Examples:
```
feat(provider): add nyaa si provider
fix(player/mpv): handle socket disconnect gracefully
docs: update installation instructions
chore: bump go.mod dependencies
```

A messy commit message = a messy changelog. Don't do it.
