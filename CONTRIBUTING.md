# Contributing to Kari

Contributions are welcome. Kari prioritizes correctness, simple straight-line code, and strict layer isolation.

The full design and code standard is documented in [docs/CODEBASE.md](docs/CODEBASE.md).

---

## Prerequisites

- **Go**: 1.26 or newer.
- **Media Player**: [MPV](https://mpv.io/) installed and available in `$PATH` for playback tests.
- **Tools**: `staticcheck` (`go install honnef.co/go/tools/cmd/staticcheck@latest`).

---

## Build & Test Workflow

Always run verification before submitting a pull request:

```bash
# 1. Run unit tests with race detection
go test -race ./...

# 2. Static analysis and formatting
go vet ./...
staticcheck ./...

# 3. Build local binary
./build.sh
```

---

## Code Standards

Every contribution must follow these rules:

1. **Simple beats clever**: Prefer straight-line code over complex abstractions. Delete dead code; do not comment it out.
2. **Layer isolation**: Provider knowledge lives strictly under `internal/provider/**`. The TUI and services must never inspect provider names or import concrete provider packages.
3. **No global mutables**: Do not add global variables or `init()` side effects. Construct components in `internal/app/app.go` and pass them down.
4. **Shared HTTP client**: Always use `internal/httpclient`. Never instantiate raw `http.Client` instances.
5. **Strict typing**: Use `provider.ContentType` constants (`ModeAnime`, `ModeMovies`, etc.) rather than raw strings.
6. **Error handling**: Wrap errors with context (`fmt.Errorf("search titles: %w", err)`). Use sentinel errors from `internal/provider/errors.go` for control flow.
7. **Structured logging**: Use `logging.Debug`, `logging.Info`, `logging.Warn`, and `logging.Error` with key-value pairs. Never use `fmt.Printf` or `log.Printf`.
8. **Documentation**: Every exported package, type, method, function, and constant block must have a clear doc comment. Comments explain *why*, not *what*.

---

## Commit Message Convention

Kari enforces [Conventional Commits](https://www.conventionalcommits.org/) for automated changelog generation:

```txt
<type>(<scope>): <description>
```

- **Types**: `feat`, `fix`, `docs`, `refactor`, `perf`, `test`, `chore`, `ci`
- **Scope** (recommended): `provider/anikoto`, `player/mpv`, `tui`, `service`, `scrobble`

### Examples

```txt
feat(provider/cinecat): add multi-bitrate HLS fallback
fix(player/mpv): handle socket disconnect gracefully
docs: update installation instructions
```

---

## Pull Request Checklist

- [ ] Code builds without warnings: `go vet ./...`
- [ ] Linter passes cleanly: `staticcheck ./...`
- [ ] All tests pass: `go test -race ./...`
- [ ] Commit messages follow the Conventional Commits specification.
- [ ] Any new providers or features are documented in `docs/PROVIDERS.md` or `docs/CONFIGURATION.md`.
