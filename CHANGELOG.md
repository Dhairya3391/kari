# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [v4.0.0] - 2026-08-23

### 🚀 Highlights & Architecture

- **Unified Codebase Standard**: Consolidated disparate documentation into a single authoritative standard at `docs/CODEBASE.md`. Every exported identifier is fully documented with purpose-first explanations.
- **Provider Capability Model**: Replaced hardcoded provider branching with clean interface capabilities (`provider.AudioLanguagesSource`, `provider.StreamingProvider`, `provider.Presenter`).
- **No Global Mutables**: Eliminated package-level mutable state and `init()` side effects across all internal subsystems. Application dependencies are instantiated centrally in `internal/app/app.go` and injected down.
- **Unified Player Framework**: Consolidated desktop media players (`MPV`, `IINA`, `VLC`) and Android intent dispatchers onto shared process management, startup checks, and IPC polling.

### ⚡ Performance & Subtitle Engine

- **Eager Subtitle Synchronization**: Subtitles now begin downloading in the background the moment the active playback source arrives (`onResolveProgress`), reducing perceived subtitle loading latency from up to 45s down to ~300ms.
- **Unblocked Autoplay & Next Episode**: Autoplay and episode transitions now launch immediately upon receiving the first playable stream + subtitle batch rather than blocking for full multi-provider resolution completion.
- **Multi-Language OpenSubtitles Fix**: Resolved an issue where OpenSubtitles search was hardcoded to filter English tracks, properly honoring user-selected subtitle languages (Spanish, French, German, Arabic, Japanese, etc.).
- **MPV Subtitle File Loading**: Standardized subtitle side-loading to `--sub-file=` across desktop and embedded players to ensure multi-subtitle appending works without track replacement.

### 🛠️ TUI & UX Improvements

- **Smoothed Batch Download Progress**: Blended per-episode byte progress (`batchEpisodeProgress`) with overall episode count for accurate, flicker-free progress bars during batch downloads.
- **Global Audio Language Settings**: Audio language filters in settings are now accessible regardless of the active content mode, enabling seamless configuration for Movies, TV, and Cartoons without requiring mode-switching.
- **Dynamic Language Resolution**: Expanded `internal/lang` with 14 selectable subtitle languages and ISO-639 normalization across all provider responses.
- **Resilient IPC & Resume Handling**: Hardened MPV JSON IPC socket polling to gracefully survive stream buffering, reconnect on transient I/O hiccups, and persist exact resume positions on exit.

### 🧹 Refactoring & Housekeeping

- **Clean Provider Isolation**: Provider-specific wire structures and scrapers are strictly encapsulated within their own packages under `internal/provider/**`.
- **Zero Dead Code**: Audited and eliminated unused constants, helper functions, and dead abstractions across `internal/subtitles`, `internal/player`, and `internal/tui`.
