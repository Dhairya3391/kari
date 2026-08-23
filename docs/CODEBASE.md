# Kari Codebase Standard

This is the single source of truth for how every file, type, function,
variable, and comment in this repository must look. Nothing in the codebase
is exempt. If code violates this document, the document wins — fix the code.

Other docs: `README.md` (user-facing install/usage), `AGENTS.md` (short
mandatory rules for AI agents, deferring here for detail).

---

## 1. Prime Rules

1. **Simple beats clever.** Straight-line code over abstractions. If a
   construct needs a paragraph to explain, redesign it.
2. **Layer isolation is absolute.** Provider knowledge lives only under
   `internal/provider/**`. Adding or changing a provider must never require
   edits outside that tree (+ its one registration entry). TUI and services
   stay generic; providers declare capabilities instead.
3. **Every exported identifier has a doc comment.** Every type, function,
   method, const block member worth naming, and var. No exceptions.
4. **No dead code.** Unused fields, branches, interfaces, and parameters get
   deleted, not commented out or kept "for later". Git remembers.
5. **One way to do a thing.** One HTTP client, one logging package, one
   error-wrapping style, one concurrency pattern per situation (§10).

---

## 2. Repository Layout

```txt
cmd/kari/            entry point: flags, logging init, calls app.Run
internal/
  app/               composition root: loads config, wires everything (Run)
  tui/               bubbletea UI. No networking, no business logic.
  service/           orchestration between TUI and provider/player/downloader
  provider/          media source integrations + Registry + capability contracts
    <name>/          one package per provider; owns ALL of its own types
    streambase/      shared TMDB-backed base for TMDB-keyed providers
    defaults/        the ONLY place providers are registered
  model/             playback aggregates shared across layers (ResolvedMedia…)
  player/            external players. One file per playback paradigm:
                     mpv.go (desktop process+IPC), players_desktop.go
                     (vlc/iina launchers), android trio (intents, no IPC),
                     ipc*.go (mpv JSON IPC), registry + per-platform
                     register_*.go
  downloader/        aria2c / ytdlp download engines
  subtitles/         subtitle search/download/format conversion
  scrobble/          trakt + anilist clients
  poster/            poster lookup + cache
  tmdb/              TMDB key pool
  search/            meilisearch-backed TMDB title search
  httpclient/        THE shared HTTP client
  config/            user settings + constants.go (all external URLs/UAs)
  history/           watch-history persistence
  settings/          settings.json persistence
  lang/              language-code normalization/display
  aniskip/           aniskip API client
  termimg/           terminal image rendering (kitty protocol / half blocks)
  logging/           slog wrapper with rotation
  util/              tiny generic helpers
docs/                CODEBASE.md only
scripts/             release/packaging scripts
```

### Dependency direction (must never cycle)

```txt
cmd → app → {tui, service, player, …} → {provider, model, …}
```

- `provider` imports only leaf/utility packages (`config`, `httpclient`,
  `logging`, `tmdb`, `search`, `model` for aggregates it fills).
- `tui` talks to `service` and reads `provider` contracts/capabilities; it
  never talks to a concrete provider package.
- Only `app` knows concrete constructors. Only `provider/defaults` lists
  providers.

---

## 3. Type Ownership

| Kind of type | Lives in | Examples |
| --- | --- | --- |
| A provider's private API response/request shapes | that provider's package | `miruro.linkResp`, `moviebox.movieboxMeta` |
| Shared wire DTOs crossing layer boundaries | `internal/provider` | `SearchResult`, `Episode`, `MediaSource`, `SubtitleOption` |
| Cross-layer vocabularies | `internal/provider` constants only | `MediaTypeMovie`, `ModeAnime`, `AudioSub`, `SourceTypeHLS` |
| Playback aggregates with behavior | `internal/model` | `ResolvedMedia`, `SubtitleTrack` |
| UI state | `internal/tui` (unexported) | `modelImpl`, message types |
| Persistence shapes | their owning package (unexported where possible) | `history.Entry` |

Rules:

- Never duplicate a struct field-for-field in another package to "re-wrap"
  it. Pass the owner's type through; stamp service-injected fields there.
- Fields set by the framework/service rather than the producer are allowed
  on shared DTOs **only** when documented as such at the field:
  e.g. `SearchResult.Provider` ("stamped by MediaService"),
  `MediaSource.Resolver` ("stamped by MediaService").
- No untyped escape hatches (`map[string]any` bags) on shared types. If a
  new provider needs to carry new meaning, add a typed capability interface
  in `internal/provider/provider.go` — that is the sanctioned extension point.

---

## 4. Naming

- Packages: lowercase, single word, no underscores (`moviebox`, not
  `movie_box` or `movieboxprovider`).
- Files: snake_case, grouped by role. TUI files are prefixed by role:
  `view_*.go`, `update_*.go`, `*_helpers.go`. Tests: `_test.go` beside the
  subject.
- Identifiers: MixedCaps. Exported = PascalCase; unexported = camelCase.
- Acronyms stay capitalized: `URL`, `ID`, `HTTP`, `TMDBID`.
- Interfaces describe capability, not implementation:
  `AudioLanguagesSource`, `MovieEpisodeFlow`. Method-only marker suffixes
  like `-er` when natural (`Downloader`).
- Constructors: `NewXxx`, always return `(*T, error)` even if currently
  infallible, so validation can grow without breaking callers.
- Constants: PascalCase grouped in one `const` block per topic with a doc
  comment on the block.
- Booleans read as predicates: `NoCachedSearches`, `AllowEmptyQuery`,
  `RequiresEpisodeListForMovies`.

---

## 5. Comments

Comments exist to record intent and constraints that code cannot express.
Everything else is noise.

### Doc comments (mandatory)

- **Every exported identifier**: start with the identifier name, complete
  sentence(s), wrap ~80 columns.

```go
// AudioLanguages returns the deduplicated union of audio languages declared
// by providers supporting mode, in provider priority order.
func (r *Registry) AudioLanguages(mode ContentType) []AudioLanguage {
```

- **Unexported identifiers**: document whenever the purpose, constraint, or
  non-obvious decision isn't fully evident from the declaration itself.
  Trivial accessors need nothing.

- Field comments go on the fields that need explaining, not the whole struct:

```go
type MediaSource struct {
 URL string
 // SuppressOrigin stops the player from deriving an Origin header from
 // Referer. Some CDNs reject any Origin header; set this when the
 // provider validates Referer only.
 SuppressOrigin bool
}
```

### Inline comments

- Explain **why**, never what. If a line needs a "what" comment, rename
  things until it doesn't.

```go
// Good: records a non-obvious constraint
// The completion message must never be dropped: nothing else clears
// m.loading, so a dropped send would leave the app stuck on
// "Downloading..." forever.
select {
case m.downloadChan <- downloadDoneMsg{opID: opID, err: err}:
case <-ctx.Done():
}
```

- Comment blocks above tricky sections (concurrency handoff, ordering
  guarantees, protocol quirks) are encouraged — 2–6 lines, declarative.
- Forbidden: narrating obvious code, changelog/history comments
  ("changed in v1.2"), commented-out code, author tags, section-banner ASCII
  art, emoji.
- `TODO` comments must carry an owner and be actionable:
  `// TODO(dhairya): drop legacy MediaType fallback after v0.9 history migration.`

---

## 6. Function Design

- One function, one job. If you need "and" to describe it, split it.
- Guard clauses / early returns first; keep nesting ≤ 3 levels.
- No naked returns in non-trivial functions.
- ≤ 4 parameters; beyond that take a struct (see `Descriptor`, `Features`).
- Constructors validate dependencies and fail fast with a wrapped error.
- Prefer small pure helpers with table-driven tests over method chains.
- Repetitive switch/if ladders over the same discriminator belong in one
  helper, not copied at call sites.
- Methods on a type stay in the same file as the type (or same package when
  the file would balloon).

---

## 7. Error Handling

- Wrap with context, sentence-style, `%w` always:

```go
if err != nil {
 return nil, fmt.Errorf("jellyfin fetch episodes: %w", err)
}
```

- Lowercase, no trailing punctuation, prefix names the operation, not the
  package.
- Sentinel errors live in `internal/provider/errors.go`
  (`ErrNoResults`, `ErrNoEpisodes`, `ErrNoSources`, `ErrNotFound`) and are
  checked with `errors.Is`; typed errors (`HTTPError`) with `errors.As`.
- Never log *and* return the same error — pick one (return at internal
  boundaries, log at operation boundaries where the failure is finally
  absorbed).
- Expected fan-out failures (one provider down among many) are
  `logging.Debug`, not warnings.
- Empty results are sentinel errors (`ErrNoResults`), not `nil, nil`.

---

## 8. Logging

Structured-first, via `internal/logging` (slog-backed). Messages are static
lowercase phrases; all data rides as typed key/value pairs:

```go
logging.Debug("search done", "mode", mode, "query", query, "results", n)

// Subsystems scope every line with a bound field instead of embedding
// names in the message:
var mpvLog = logging.With("component", "player.mpv")
mpvLog.Warn("direct playback failed", "exitCode", rc, "stderr", errText)
```

- **No printf-style logging.** `Debugf/Infof/Warnf/Errorf` do not exist;
  a `key=%q` inside the message string is a review-blocking defect.
- **No component prefixes in messages** ("ytdlp: …", "[http] …") — bind a
  scope with `logging.With` and keep the message pure prose.
- Field names are camelCase identifiers (`"tmdbID"`, `"exitCode"`).
- Level guide:
  - `Debug` — execution tracing; per-provider failures expected during
    normal fan-out.
  - `Info` — lifecycle events (startup, provider registered, scrobble OK,
    download complete).
  - `Warn` — failures a user might care about without debug logs
    (subtitle fetch failed, all providers failed, retry gave up).
  - `Error` — failures aborting the current operation.
- Output: `~/.config/kari/kari.log`, rotated at 10 MB with one backup.
  Env toggles: `KARI_LOG_DEBUG` (level), `KARI_LOG_STDERR` (mirror),
  `KARI_LOG_FORMAT_JSON` (machine-readable output). Source locations are
  attached automatically while debugging.
- Never `fmt.Printf` / `log.Printf` for application logic — those are
  reserved for CLI stdout UX (`--version`, update progress bars).

---

## 9. Context & Timeouts

- `ctx context.Context` is always the first parameter.
- Every network call takes the ctx (`NewRequestWithContext`).
- Service entry points bound their work: `context.WithTimeout` at
  `MediaService.Search/FetchEpisodes/Resolve`, subtitle fetches, etc.
- Never store ctx in structs. Derive, don't retain.

---

## 10. Concurrency — one pattern per situation

1. **Single-shot request/response** → plain function returning values:

   `Search`, `FetchEpisodes`, `ResolveSource`.

2. **Incremental / multi-event streams** → context-cancelled channel:

   `StreamingProvider.ResolveStream(ctx, …, updates chan<- []MediaSource)`,
   download progress channels.

3. **Delivering into the TUI** → tea messages via the existing channel /
   callback bridges (`resolveChan`, `downloadChan`, `tea.Cmd` closures).

Fan-out/fan-in uses `errgroup.WithContext` + mutex-guarded aggregation
(see `MediaService.Resolve`). Channels get explicit small buffer sizes with
a comment justifying them. Every goroutine has a termination path: derive
from a cancellable ctx, drain abandoned channels, never leak on early exit.

No global mutable state. The single sanctioned exception remains
`internal/tui/styles.go` theme vars (documented there).

---

## 11. Dependency Injection & Wiring

- Components are constructed in `internal/app/app.go` and passed down via
  constructors. No `init()` side effects anywhere.
- Providers self-describe via `provider.Descriptor` in
  `internal/provider/defaults/defaults.go`:

```go
{
 ID: "myprovider",
 When: func(cfg *config.Config) bool { return cfg.MyProviderEnabled },
 Factory: func(d provider.Deps) (provider.Provider, error) {
  return myprovider.NewClient(d.KeyPool)
 },
},
```

  `When == nil` means always enabled. Factory errors skip the provider with
  a logged warning — startup must not die because one integration failed.

- The TUI receives `*provider.Registry` and asks it questions
  (`Features`, `AudioLanguages`, `RequiresEpisodeListForMovies`); it never
  switches on provider names or mode-specific quirks it invented itself.

---

## 12. HTTP

- All network I/O goes through `internal/httpclient`:
  `httpclient.New()` or `httpclient.NewWithUserAgent(ua)`. It provides
  retries, timeouts, and tuned pooling.
- Sole exception: non-idempotent local calls (e.g. aria2 JSON-RPC writes)
  use a plain bounded-timeout `http.Client` so retries can't double-submit.
- External endpoints (API bases, referers, UAs) live in
  `internal/config/constants.go`, never inside provider packages.
- Non-2xx responses become `&provider.HTTPError{Code:…, URL:…}`.

---

## 13. Strict Typing

- Modes are `provider.ContentType` constants — raw `"anime"` strings in
  comparisons are forbidden outside `internal/provider` vocabulary helpers.
- Media types, audio modes, and stream types go through the declared
  constants (`provider.MediaTypeMovie`, `provider.AudioSub`,
  `provider.SourceTypeHLS`, …). A raw literal in a comparison anywhere is a
  bug; extend the constant block instead. The only sanctioned literals are
  legacy-persisted-data matchers (e.g. `modeForHistoryEntry`), documented as
  such.
- Quality labels describe content only ("1080p Hindi", "Auto (Vidstream)") —
  never embed provider names or `[TAGS]` in them. Provider identity shown to
  users comes from `Registry.DisplayName` (the Presenter alias), never from
  string parsing.
- No `any`/`interface{}` except JSON decode targets and genuinely generic
  utilities.
- Compile-time interface guards at the bottom of implementation files:

```go
var (
 _ provider.Provider             = (*Client)(nil)
 _ provider.AudioLanguagesSource = (*Client)(nil)
)
```

---

## 14. Platform Specifics

- Isolate with build tags: `//go:build android`, `registry_windows.go`, etc.
- Behavior stays identical across platforms for the same player name.
- Platform hacks get a comment explaining the platform quirk that forces
  them.

---

## 15. Testing & Verification

- Verify after every change (AGENTS.md mandate):

```bash
go vet ./... && go build ./... && staticcheck ./... && go test ./...
```

- Tests live beside the subject (`foo_test.go`), use table-driven cases for
  multi-input logic, and test exported behavior plus tricky internals only —
  not getters.
- New concurrency helpers and capability aggregation get tests before the
  features built on them do.

### Live integration tests (`tests/live`)

Real-network tests for the full pipeline live under `tests/live`, gated by
the `live` build tag so normal `go test ./...` stays hermetic:

```bash
go test -tags live ./tests/live -v   # needs network; uses real providers
```

Coverage: every provider's Search→Episodes→Resolve against its own live
API, an end-to-end mpv playback proof via IPC duration, and a bounded real
download through yt-dlp. Catalog drift on a provider (a title genuinely
absent today) skips instead of failing; transport errors fail loudly.
Tests always clean up spawned mpv processes.

---

## 16. Commits

Conventional Commits, enforced (changelog depends on it):

```txt
<type>(<scope>): <description>
```

Types: `feat`, `fix`, `docs`, `refactor`, `perf`, `test`, `chore`, `ci`.
Scope = area touched: `provider/miruro`, `player/mpv`, `tui`, `service`,
`docs`. One logical change per commit; mixed changes get split.

---

## 17. Adding a Provider (checklist)

1. `internal/config/constants.go`: add endpoint/UA constants.
2. `internal/provider/<name>/`: create package. Private types for the API;
   implement `Provider` (+ any optional capabilities you genuinely support:
   `AudioLanguagesSource`, `MovieEpisodeFlow`, `FeatureSource`,
   `StreamingProvider`, `Presenter` for a user-facing codename). Add
   compile-time guards.
3. `internal/provider/defaults/defaults.go`: add one `Descriptor`
   (with `When` gate if config-dependent).
4. Done. Registry exposes your modes/features/languages/alias
   automatically; TUI picks them up without edits.
5. Verify (§15) and smoke-test the supported modes.
