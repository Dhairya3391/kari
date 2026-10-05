package provider

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

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
	// ModeManga is the in-terminal manga/comic reading mode, served by
	// providers implementing MangaSource.
	ModeManga ContentType = "manga"
	// ModeLive is the live sports & TV streaming mode.
	ModeLive ContentType = "live"
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
	MediaTypeManga   = "manga"
	MediaTypeLive    = "live"
)

// Audio-track vocabulary for Episode.Audio and the sub/dub selection feature.
const (
	AudioSub = "sub"
	AudioDub = "dub"
)

// Subtitle-kind vocabulary for anime MediaSource.SubType values, declaring
// how subtitles reach the picture. Providers that don't know leave it blank.
const (
	SubTypeHard = "hard"
	SubTypeSoft = "soft"
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
	// CoverURL is stamped by the provider itself at search time when the
	// upstream catalog ships artwork directly (manga covers from
	// WeebCentral). Empty for providers whose artwork resolves through
	// the poster package instead (TMDB/AniList).
	CoverURL string
	// CoverReferer is the Referer sent with direct CoverURL downloads
	// when the image host enforces hotlink protection. Stamped alongside
	// CoverURL so the generic poster package never hardcodes a
	// provider-specific referer.
	CoverReferer string
	// Overview and Genres are stamped by the provider at search time when
	// the catalog ships them (WeebCentral descriptions/tags), so detail
	// screens render without extra requests.
	Overview string
	Genres   []string

	// Live TV / Sports scheduling fields. Populated only by live providers;
	// zero for all other modes so callers can check `Live || !StartsAt.IsZero()`.
	Live     bool      // true: stream is airing right now
	StartsAt time.Time // zero: no schedule known; non-zero: local start time
	Group    string    // live league / category / channel type
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
	Default  bool
	Referer  string
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
	// SubType marks the anime subtitle kind the upstream declares for this
	// stream: "hard" (burned in), "soft" (toggleable), or "" unknown.
	// Non-anime sources leave it blank; subtitle delivery is independent.
	SubType   string
	ExtraArgs []string
	// SuppressOrigin stops the player layer from deriving an Origin header
	// from Referer. Some CDNs reject any Origin (or reject a full-path one);
	// providers that validate Referer only should set this.
	SuppressOrigin bool
}

// TransportIdentity returns the canonical identity used to deduplicate a
// playable source without collapsing request variants that need different
// headers, language selection, or player options.
func (s MediaSource) TransportIdentity() string {
	rawURL := strings.TrimSuffix(strings.TrimSpace(s.URL), "/")
	if rawURL == "" {
		return ""
	}
	return strings.Join([]string{
		rawURL,
		strings.TrimSpace(s.Referer),
		strings.TrimSpace(s.UserAgent),
		strings.TrimSpace(s.CookieHeader),
		strings.TrimSpace(s.Language),
		strconv.FormatBool(s.SuppressOrigin),
		strings.Join(s.ExtraArgs, "\x00"),
	}, "\x00")
}

// IsDirectURL reports whether raw is a direct remote stream address: an
// http(s) URL with a non-local host and no proxy path. mpv plays sources
// itself, so anything needing a helper server (localhost, /proxy/ paths)
// is rejected — providers must return CDN addresses.
func IsDirectURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasSuffix(host, ".local") {
		return false
	}
	if strings.Contains(strings.ToLower(u.Path), "/proxy/") {
		return false
	}
	return true
}

// FilterDirectSources drops any source whose URL is not directly playable
// by mpv (see IsDirectURL), preserving order.
func FilterDirectSources(sources []MediaSource) []MediaSource {
	kept := sources[:0]
	for _, s := range sources {
		if IsDirectURL(s.URL) {
			kept = append(kept, s)
		}
	}
	return kept
}

// challengedHosts serve video but persistently challenge non-browser
// clients: browsers and curl pass while mpv gets HTTP 403 bot challenges
// on playlists and segments alike (verified across fresh signed URLs;
// UA/Referer/Origin/Accept variations change nothing — it keys on the
// TLS fingerprint, which no client flag can alter). Their rows stay
// listed as fallback and manual picks, but every ranking sorts them
// below playable-now sources so playback never waits out their failures
// first.
var challengedHosts = []string{"uwucdn.top", "owocdn.top"}

// ChallengedHost reports whether rawURL is served by a challenged host.
// Unparsable URLs fail open (false): an unknown shape must never sink a
// playable source.
func ChallengedHost(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, bad := range challengedHosts {
		if host == bad || strings.HasSuffix(host, "."+bad) {
			return true
		}
	}
	return false
}

// MangaChapter is one readable chapter of a manga/comic title. Number
// stays a string because chapters are fractional ("12.5"), prefixed, or
// unnumbered (oneshots) — an int cannot represent the catalog.
type MangaChapter struct {
	// ID is the provider-specific handle passed back into FetchPages.
	ID string
	// Provider names the producing provider, stamped by MangaService so
	// the TUI can fetch pages without retaining extra state.
	Provider string
	Number   string
	Volume   string
	Title    string
	Language string
	Group    string
}

// MangaPage is one page image of a chapter.
type MangaPage struct {
	URL string
	// Referer/UserAgent are sent with the image request when the image
	// host requires them (WeebCentral's CDN needs its site as Referer).
	Referer   string
	UserAgent string
	Width     int
	Height    int
	// FallbackURL is tried when URL fails with 404/5xx.
	FallbackURL string
}

// DisplayLabel renders the chapter as users expect to see it:
// "Ch 12.5 — Title [Group]", with a trailing "[pt-br]" tag when the
// translation is not English (English rows stay untagged).
func (c MangaChapter) DisplayLabel() string {
	var b strings.Builder
	if c.Number != "" {
		b.WriteString("Ch ")
		b.WriteString(c.Number)
	} else {
		b.WriteString("Oneshot")
	}
	if c.Title != "" {
		b.WriteString(" — ")
		b.WriteString(c.Title)
	}
	if c.Group != "" {
		b.WriteString(" [")
		b.WriteString(c.Group)
		b.WriteString("]")
	}
	if c.Language != "" && c.Language != "en" {
		b.WriteString(" [")
		b.WriteString(c.Language)
		b.WriteString("]")
	}
	return b.String()
}

// MangaSource is implemented by providers serving readable manga/comics:
// chapter listings plus page-image URLs. The embedded Provider contract
// still applies (Search must work); FetchEpisodes/ResolveSource are
// inapplicable to paged media and return sentinel errors.
type MangaSource interface {
	Provider
	FetchChapters(ctx context.Context, series SearchResult) ([]MangaChapter, error)
	FetchPages(ctx context.Context, chapter MangaChapter) ([]MangaPage, error)
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

// EpisodeAvailabilitySource lists only episodes whose requested audio track
// is known to be available from the provider.
type EpisodeAvailabilitySource interface {
	FetchAvailableEpisodes(ctx context.Context, series SearchResult) ([]Episode, error)
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
	// LanguageFilter mirrors the user's audio-language settings (see
	// internal/settings LanguageFilter): nil or missing keys mean enabled.
	// Providers use it to drop foreign dubbed audio at resolve time.
	LanguageFilter map[string]bool
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
