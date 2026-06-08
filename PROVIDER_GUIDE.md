# Provider Integration Guide

This guide explains how to add a new media provider to Kari.

## 1. Define Constants
Add all base URLs, referers, and User-Agents to `internal/config/constants.go`. 
**Never** hardcode these inside the provider package.

```go
// internal/config/constants.go
const (
    MyProviderAPIBase = "https://api.myprovider.com"
    MyProviderUA      = "Mozilla/5.0 ..."
)
```

## 2. Create the Provider Package
Create a new package under `internal/provider/`.
Example: `internal/provider/myprovider/client.go`

### Implementation Requirements
Your client must implement the `provider.Provider` interface:

```go
type Client struct {
    httpClient *http.Client
    keyPool    *tmdb.KeyPool // Inject via constructor if using TMDB
}

func NewClient(keyPool *tmdb.KeyPool) (*Client, error) {
    // Validate required dependencies
    if keyPool == nil {
        return nil, fmt.Errorf("tmdb key pool is required")
    }
    return &Client{
        httpClient: httpclient.NewWithUserAgent(config.MyProviderUA),
        keyPool:    keyPool,
    }, nil
}

func (c *Client) Name() string { return "myprovider" }

func (c *Client) Modes() []provider.Mode {
    return []provider.Mode{
        {Name: provider.ModeMovies, Priority: 2},
        {Name: provider.ModeTV, Priority: 1},
    }
}

func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
    // Implement search logic
}

func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
    // Implement episode fetching logic
}

func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
    // Implement source resolution logic
}
```

## 3. Key Implementation Details

### TMDB Integration
If your provider relies on TMDB for search or episode metadata, you can:
1. Inject `*tmdb.KeyPool` into your constructor.
2. Embed `streambase.Base` from `internal/provider/streambase` to reuse common TMDB logic.

### Error Handling
- Use `provider.HTTPError` for any non-2xx HTTP responses.
- Use sentinel errors from `internal/provider/errors.go` (e.g., `provider.ErrNoSources`, `provider.ErrNoEpisodes`).
- Always wrap errors with context: `fmt.Errorf("action failed: %w", err)`.

### Interface Guards
Add a compile-time guard at the bottom of your `client.go` to ensure interface compliance:
```go
var _ provider.Provider = (*Client)(nil)
```
If your provider supports streaming updates, also implement `provider.StreamingProvider` and add:
```go
var _ provider.StreamingProvider = (*Client)(nil)
```

## 4. Register the Provider
Add your provider to the default registry in `internal/provider/defaults/defaults.go`.

```go
var DefaultProviders = []provider.Descriptor{
    // ... existing providers
    {
        ID: "myprovider",
        Factory: func(kp *tmdb.KeyPool) (provider.Provider, error) {
            return myprovider.NewClient(kp)
        },
        Modes: []provider.Mode{
            {Name: provider.ModeMovies, Priority: 2},
            {Name: provider.ModeTV, Priority: 1},
        },
        Priority: 2,
    },
}
```

## 5. Verification
Run the following commands to ensure your changes didn't break the build:
```bash
go build ./...
go vet ./...
go test ./...
```
Launch the app and verify that the new provider appears in the search results for the supported modes.
