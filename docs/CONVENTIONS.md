# Coding Conventions and Patterns

This document outlines the engineering standards and patterns used in the Kari codebase.

## 1. No Global State
Global variables and `init()` functions that register side-effects are strictly forbidden. 
- All components must be explicitly constructed in `internal/app/app.go`.
- Configuration and dependencies must be passed via constructors (Dependency Injection).
- **Exception**: `internal/tui/styles.go`'s color/style package vars (e.g. `colorPrimary`, `sectionTitleStyle`). These are lipgloss's idiomatic pattern for theme constants, read directly by every `view_*.go` render function. `SetAccentColor` mutates them at runtime for the accent-color setting — this is deliberate, UI-presentation-only state, not a substitute for dependency injection elsewhere.

## 2. Dependency Injection
Use constructors to pass all required dependencies.
- **Config**: Always pass `*config.Config` to any component needing environment settings.
- **HTTP Client**: Do not create `&http.Client{}`. Use the shared `httpclient` package.
- **Services**: The TUI must only interact with `service.MediaService` or `service.DownloadService`.

## 3. Error Handling
- Use sentinel errors for common conditions (e.g., `provider.ErrNoResults`).
- Wrap errors with context using `%w` in `fmt.Errorf`.
- Standardize on direct error messages; avoid adding excessive prefixes that duplicate the package name.

## 4. Shared HTTP Client
All network calls must use `internal/httpclient`.
- Use `httpclient.New()` for default requests.
- Use `httpclient.NewWithUserAgent(ua)` if a specific User-Agent is required (common for providers).
- The client handles 3 retries and a 30s timeout by default.
- Connection pooling is tuned for providers that fan out several concurrent requests to their own host (e.g. multi-query-variant search) — `MaxIdleConnsPerHost` is 16, not Go's default of 2.
- Exception: one-off local JSON-RPC calls that aren't safely retryable (e.g. `internal/downloader/aria2_rpc.go`'s `aria2.addUri`) use a plain bounded-timeout `http.Client` instead, to avoid the shared client's automatic retry double-submitting a non-idempotent call.

## 5. Logging
- Use the `internal/logging` package, which is a wrapper around `log/slog`.
- Use the printf-style helpers (`Debugf`, `Infof`, `Warnf`, `Errorf`) — this is the convention actually used across the codebase, not the structured `Debug`/`Info`/`Warn`/`Error` forms the package also exposes.
- Level guide: `Debugf` for high-volume execution tracing and per-provider failures that are expected during normal fan-out (one provider failing while others succeed). `Warnf` for failures a user might care about even without debug logging on (e.g. a subtitle fetch failing, all providers failing to resolve a source). `Errorf` for failures that abort the current operation.
- `kari.log` rotates automatically past 10MB (`internal/logging/logger.go`) — no manual log management needed.

## 6. Type Safety
- Avoid `any` or `interface{}` where possible.
- Use typed constants for repeating strings (e.g., `provider.ContentType`).
- Ensure all provider modes and search types align with defined enums.

## 7. Platform Specifics
- Use Go build tags (e.g., `//go:build android`) to isolate platform-specific logic.
- Keep the interface consistent across platforms (e.g., `mpv` player should have the same name on Linux and Android).
