# Extending Kari

This guide explains how to add new media providers, players, and skip engines to Kari.

---

## 1. Adding a Media Provider

Kari enforces strict provider isolation. Provider-specific logic, response structures, and scraping code live exclusively within `internal/provider/<name>/`. The TUI and application layers interact only through generic interfaces.

### Step 1: Create the Provider Package

Create a new directory under `internal/provider/<name>/` and implement the `provider.Provider` interface:

```go
package myprovider

import (
    "context"
    "fmt"
    "kari/internal/httpclient"
    "kari/internal/provider"
)

type Client struct {
    client *httpclient.Client
}

func NewClient() (*Client, error) {
    return &Client{
        client: httpclient.New(),
    }, nil
}

func (c *Client) Name() string {
    return "myprovider"
}

func (c *Client) Modes() []provider.Mode {
    return []provider.Mode{
        {Name: provider.ModeMovies, Priority: 2},
        {Name: provider.ModeTV, Priority: 2},
    }
}

func (c *Client) Search(ctx context.Context, query string) ([]provider.SearchResult, error) {
    // 1. Query upstream API / scrapers using c.client
    // 2. Return []provider.SearchResult with unified fields
    return nil, nil
}

func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
    return nil, nil
}

func (c *Client) ResolveSource(ctx context.Context, series provider.SearchResult, ep provider.Episode) ([]provider.MediaSource, error) {
    return nil, nil
}
```

### Step 2: Implement Optional Capabilities

Instead of hardcoding provider checks in the TUI, providers declare capabilities via interfaces in `internal/provider/provider.go`:

- `provider.AudioLanguagesSource`: Exposes supported audio languages for filter selection.
- `provider.MovieEpisodeFlow`: Signals whether movie playback requires an episode list step or resolves directly.
- `provider.FeatureSource`: Declares feature flags (e.g. Sub/Dub audio toggle support).
- `provider.MangaSource`: Exposes chapter and page retrieval for in-terminal reading.

### Step 3: Register in Defaults

Add a factory descriptor to `DefaultProviders` in `internal/provider/defaults/defaults.go`:

```go
{
    ID: "myprovider",
    Factory: func(d provider.Deps) (provider.Provider, error) {
        return myprovider.NewClient()
    },
},
```

### Step 4: Define Routing Strategy

Add routing policies for the provider in `internal/provider/routes.go`:

```go
{Provider: "myprovider", Search: StrategyMerge, Sources: StrategyRace},
```

- `StrategyMerge`: Query simultaneously with other providers and combine results.
- `StrategyFallback`: Query only when earlier priority providers fail.
- `StrategyRace`: Query providers concurrently and pick the fastest valid stream.

### Step 5: Write Tests

Use `net/http/httptest` to verify parsing and error mapping against real or recorded fixtures:

```bash
go test -v ./internal/provider/myprovider/...
```

---

## 2. Adding a Media Player

Desktop and mobile media players implement `player.Player` in `internal/player/player.go`:

```go
type Player interface {
    Name() string
    Available() bool
    Play(media model.ResolvedMedia) (PlaybackResult, error)
}
```

- For desktop players: Register the player in `internal/player/register_desktop.go`.
- For Android: Register intent launchers in `internal/player/register_android.go`.
- If the player supports Unix socket IPC (like MPV or IINA), implement real-time position reporting to allow automatic resume.

---

## 3. Adding a Skip Timing Source

Kari uses a parallel hybrid skip engine to fetch intro, outro, recap, and preview timestamps.

1. Create a client package under `internal/<servicename>/`.
2. Query the service in parallel inside `internal/player/aniskip.go`.
3. Normalize boundaries to seconds and populate `model.SkipTimings`.
