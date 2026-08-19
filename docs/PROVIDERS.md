# Media Providers

Providers are the heart of Kari's data extraction. This document explains how they are implemented and how to add new ones.

## 1. The Provider Interface

All providers must implement the `Provider` interface defined in `internal/provider/provider.go`:

```go
type Provider interface {
 Name() string
 Modes() []Mode
 Search(ctx context.Context, query string, mode ContentType) ([]SearchResult, error)
 FetchEpisodes(ctx context.Context, series SearchResult) ([]Episode, error)
 ResolveSource(ctx context.Context, mediaID string, episode Episode) ([]MediaSource, error)
}
```

## 2. Modes and Priority

- `Modes()`: Defines which categories (Anime, Movies, etc.) the provider supports.
- `Priority`: Lower numbers are higher priority. If multiple providers support the same mode, the search results will be interleaved/ordered based on priority.

## 3. Implementation Patterns

- **HTTP**: Use `httpclient.NewWithUserAgent(...)`. Many providers (like Miruro) require specific User-Agents to avoid blocks.
- **Parsing**: Use standard Go libraries. For HTML, use `regexp` or a DOM parser (carefully).
- **Errors**: Return `provider.ErrNoResults`, `provider.ErrNoEpisodes`, or `provider.ErrNoSources` for common empty states. This allows the `MediaService` to aggregate warnings properly.

## 4. How to Add a New Provider

1. Create a new package under `internal/provider/<name>`.
2. Implement the `Provider` interface.
3. Add a `NewClient(...)` constructor.
4. Open `internal/app/app.go`.
5. Instantiate your client in the `Run()` function.
6. Register it with the provider registry: `registry.Register(yourClient)`.

## 5. Existing Providers

- **Miruro**: API-based, high performance Anime provider.
- **MovieBox**: Movie/TV provider with multi-stream support (via TMDB).
- **VidKing**: Fast movie/TV scraper with multi-server support (via TMDB).
- **RiveStream**: Movie/TV provider (via TMDB) — a thin client for the `noob4.broggl.farm` JSON API that queries several sources in parallel and returns direct HLS/MP4 URLs (best quality first, MP4 preferred over HLS at equal quality). `/?type=movie|tv&id=<tmdb>&season=&episode=` returns `sources[]` + `subtitles[]`; the auth/expire tokens embedded in the URLs are short-lived, so kari always fetches fresh and never caches the resolved URLs. The API gives no per-source provider field, so kari infers the internal provider from each quality-string convention (pipe format → `citadel`, `tcloud`/`dcloud`/`ipcloud` → `primevids`, bare `HLS` → `apex`, bare integer → `flowcast`) and labels sources accordingly, falling back to `[RIVESTREAM]` when unidentifiable. Its subtitle metadata is unreliable and duplicated, so its subtitles are dropped in favor of VidKing / OpenSubtitles / YIFY.
- **PirateX**: API-based Cartoon provider for piratexplay.cc — a thin client for the `piratex.dhairya.codes` FastAPI service that does the scraping server-side. `/search` → `/series/{slug}` → `/watch/{episode_id}` (three JSON GETs) yields ready-to-play relayed `.m3u8` URLs; kari builds one source per served language (`audio[].play`, language preselected server-side via `?lang=` — no player-side `--alang`).
- **Jellyfin**: Optional Movies/TV provider backed by a self-hosted Jellyfin server (enabled via `JELLYFIN_URL` + `JELLYFIN_API_KEY`).
