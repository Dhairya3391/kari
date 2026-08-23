package provider

import (
	"context"
	"kari/internal/config"
	"kari/internal/tmdb"
)

// ContentType identifies one content mode (anime, movies, tv, …). It is the
// canonical mode vocabulary; never compare raw strings.
type ContentType string

const (
	ModeAnime    ContentType = "anime"
	ModeMovies   ContentType = "movies"
	ModeTV       ContentType = "tv"
	ModeCartoon  ContentType = "cartoon"
	ModeJellyfin ContentType = "jellyfin"
)

// Mode declares a provider's support for one content mode and its priority
// when several providers serve the same mode.
type Mode struct {
	Name     ContentType // e.g. ModeAnime, ModeMovies, etc.
	Priority int         // lower = higher priority when multiple providers share a mode
}

// Media type vocabulary for SearchResult.MediaType. Always compare or emit
// through these constants — raw "movie"/"tv" strings elsewhere are forbidden.
const (
	MediaTypeMovie   = "movie"
	MediaTypeTV      = "tv"
	MediaTypeAnime   = "anime"
	MediaTypeCartoon = "cartoon"
)

// Audio-track vocabulary for Episode.Audio and the sub/dub selection feature.
const (
	AudioSub = "sub"
	AudioDub = "dub"
)

// Stream container/protocol vocabulary for MediaSource.Type. Players and
// downloaders branch on these; providers emit them.
const (
	SourceTypeHLS  = "hls"
	SourceTypeM3U8 = "m3u8"
	SourceTypeMP4  = "mp4"
)

// SearchResult is a title returned by a provider search. It doubles as the
// series handle passed back into FetchEpisodes/ResolveSource, so providers
// must put whatever identifier they need later in ID.
type SearchResult struct {
	Title string
	// ID is the provider-specific handle for this title (episode source id,
	// TMDB id as a string, Jellyfin item id, …).
	ID string
	// Provider is stamped by MediaService after aggregation; providers
	// never set it themselves.
	Provider  string
	Type      ContentType
	Year      string
	MediaType string
	TMDBID    int
}

// Episode is one playable unit of a series. For movies, providers may
// return a single episode with zero Season/Episode numbers.
type Episode struct {
	Season  int
	Episode int
	Title   string
	// ID is the provider-specific handle for this episode (may equal the
	// series handle for TMDB-keyed providers).
	ID     string
	Audio  string // "sub", "dub", or ""
	Filler bool
	TMDBID int
}

// SubtitleOption is a subtitle track a provider offers alongside a
// MediaSource, tagged with its language so callers can pick one matching
// the user's preferred subtitle language instead of assuming English.
type SubtitleOption struct {
	URL      string
	Language string
}

// MediaSource is one playable stream. Providers fill everything except
// Resolver, which MediaService stamps with the producing provider's name.
type MediaSource struct {
	URL          string
	Quality      string
	Resolver     string
	Referer      string
	Type         string
	Subtitles    []SubtitleOption
	UserAgent    string
	CookieHeader string
	Language     string
	ExtraArgs    []string
	// SuppressOrigin stops the player layer from deriving an Origin header
	// from Referer. Some CDNs reject any Origin (or reject a full-path one);
	// providers that validate Referer only should set this.
	SuppressOrigin bool
}

// Provider is the core contract every media integration implements:
// search titles, list episodes, and resolve playable sources.
type Provider interface {
	Name() string
	Modes() []Mode
	Search(ctx context.Context, query string, mode ContentType) ([]SearchResult, error)
	FetchEpisodes(ctx context.Context, series SearchResult) ([]Episode, error)
	ResolveSource(ctx context.Context, mediaID string, episode Episode) ([]MediaSource, error)
}

// AudioLanguage is a display-ready audio-track language a provider can tag
// MediaSources with. Code is the stable identifier persisted in user
// settings and matched against MediaSource.Language; Display is the
// human-readable label shown in the UI.
type AudioLanguage struct {
	Code    string
	Display string
}

// AudioLanguagesSource is implemented by providers that set MediaSource.Language.
// It declares the full set of languages the provider may emit so the settings
// screen can offer per-language filters without hardcoding provider specifics.
// Optional: providers that never tag audio languages don't implement it.
type AudioLanguagesSource interface {
	AudioLanguages() []AudioLanguage
}

// MovieEpisodeFlow is implemented by providers whose movie-titled search
// results still require a normal episode listing before resolution — e.g.
// resolution needs a per-episode ID that only FetchEpisodes can supply.
// Optional: providers able to resolve movies straight from a SearchResult
// (via TMDB ID or similar) don't implement it; direct resolution is the default.
type MovieEpisodeFlow interface {
	RequiresEpisodeListForMovies() bool
}

// Features describes UI-relevant behavior of a content mode as declared by
// the providers supporting it. The zero value means "no special behavior";
// Registry.Features aggregates declarations across providers.
type Features struct {
	AllowEmptyQuery   bool   // an empty query is meaningful (e.g. browse whole library)
	NoCachedSearches  bool   // search results change server-side; never cache them
	SearchPlaceholder string // hint text for the search input
	AudioSelection    bool   // episode-level audio (sub/dub) selection applies
}

// FeatureSource lets a provider declare per-mode Features. Optional:
// unimplemented features fall back to defaults (CacheableSearches=true,
// everything else false).
type FeatureSource interface {
	Features(mode ContentType) Features
}

// Presenter lets a provider declare the user-facing codename shown in the
// UI instead of its internal Name(). Optional: without it the internal
// name is displayed as-is. Internal names still appear in logs and
// persisted history — Alias is purely presentational.
type Presenter interface {
	Alias() string
}

// Descriptor declares a provider for registration. Descriptors live only in
// internal/provider/defaults — the single place providers are listed.
type Descriptor struct {
	ID string
	// When gates registration on configuration; nil means always enabled.
	When func(*config.Config) bool
	// Factory constructs the provider from shared dependencies.
	Factory func(Deps) (Provider, error)
}

// Deps carries everything a provider factory may need at construction time.
type Deps struct {
	Config  *config.Config
	KeyPool *tmdb.KeyPool
}

// StreamingProvider is implemented by providers that deliver sources
// incrementally over a channel instead of one blocking slice.
type StreamingProvider interface {
	Provider
	ResolveStream(ctx context.Context, mediaID string, episode Episode, updates chan<- []MediaSource) error
}

// StatusCodedError is implemented by errors carrying an HTTP status code,
// letting callers react to 4xx/5xx without string matching.
type StatusCodedError interface {
	StatusCode() int
}
