# Coding Conventions and Patterns

This document outlines the engineering standards and patterns used in the Kari codebase.

## 1. No Global State
Global variables and `init()` functions that register side-effects are strictly forbidden. 
- All components must be explicitly constructed in `internal/app/app.go`.
- Configuration and dependencies must be passed via constructors (Dependency Injection).

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

## 5. Logging
- Use the `internal/logging` package, which is a wrapper around `log/slog`.
- Prefer `Infof` and `Errorf`. Use `Debugf` for high-volume execution tracing.
- Structured logging is preferred when adding new features.

## 6. Type Safety
- Avoid `any` or `interface{}` where possible.
- Use typed constants for repeating strings (e.g., `provider.ContentType`).
- Ensure all provider modes and search types align with defined enums.

## 7. Platform Specifics
- Use Go build tags (e.g., `//go:build android`) to isolate platform-specific logic.
- Keep the interface consistent across platforms (e.g., `mpv` player should have the same name on Linux and Android).
