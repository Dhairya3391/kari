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

- **Cineby**: API-based movie/TV provider (via TMDB).
- **Miruro**: API-based, high performance Anime provider.
- **VidNest**: Movie/TV provider with multi-stream support (via TMDB).
- **VidKing**: Fast movie/TV scraper with multi-server support (currently disabled).
- **WCO**: HTML scraping for Cartoons and Anime.
