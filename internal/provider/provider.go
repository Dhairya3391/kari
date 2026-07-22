package provider

import (
	"context"
	"kari/internal/tmdb"
)

type ContentType string

const (
	ModeAnime    ContentType = "anime"
	ModeMovies   ContentType = "movies"
	ModeTV       ContentType = "tv"
	ModeCartoon  ContentType = "cartoon"
	ModeJellyfin ContentType = "jellyfin"
)

type Mode struct {
	Name     ContentType // e.g. ModeAnime, ModeMovies, etc.
	Priority int         // lower = higher priority when multiple providers share a mode
}

type SearchResult struct {
	Title     string
	ID        string
	Type      ContentType
	Year      string
	MediaType string
	TMDBID    int
}

type Episode struct {
	Season  int
	Episode int
	Title   string
	ID      string
	Audio   string // "sub", "dub", or ""
	Filler  bool
	TMDBID  int
}

type MediaSource struct {
	URL          string
	Quality      string
	Resolver     string
	Referer      string
	Type         string
	Subtitles    []string
	UserAgent    string
	CookieHeader string
	Language     string
}

type Provider interface {
	Name() string
	Modes() []Mode
	Search(ctx context.Context, query string, mode ContentType) ([]SearchResult, error)
	FetchEpisodes(ctx context.Context, series SearchResult) ([]Episode, error)
	ResolveSource(ctx context.Context, mediaID string, episode Episode) ([]MediaSource, error)
}

type Descriptor struct {
	ID       string
	Factory  func(keyPool *tmdb.KeyPool) (Provider, error)
	Modes    []Mode
	Priority int
}

type StreamingProvider interface {
	Provider
	ResolveStream(ctx context.Context, mediaID string, episode Episode, updates chan<- []MediaSource) error
}

type StatusCodedError interface {
	StatusCode() int
}
