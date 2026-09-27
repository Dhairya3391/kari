package model

import (
	"net/http"
	"strings"
	"time"
)

// Title is one search result and series handle. Providers fill only what
// the catalog returns; screens omit any line without data.
type Title struct {
	ID       string
	Provider string
	Name     string
	Year     string
	Poster   string
	Overview string
	Kind     Kind
	Genres   []string
	Rating   float32
	// Group is the live league, category, or channel type. Empty for
	// non-live media.
	Group string
	// StartsAt is the scheduled live start in local time. Zero means the
	// event is live now (see Live) or no schedule is known. Never render
	// schedule text from Year.
	StartsAt time.Time
	// Live reports the event is airing right now.
	Live bool
	// ExternalIDs carries third-party identifiers (anilist/mal/tmdb/imdb)
	// as available, resolved lazily and cached in memory.
	ExternalIDs ExternalIDs
}

// ExternalIDs carries the third-party identifiers a title is known by.
type ExternalIDs struct {
	AniList int
	MAL     int
	TMDB    int
	IMDB    string
}

// Item is one episode or chapter of a title. Manga chapter numbers stay
// display strings ("12.5", oneshots) in Display; Number carries the
// sortable value when the provider supplies one.
type Item struct {
	ID      string
	Title   string
	Display string
	Season  int
	Number  int
	Runtime time.Duration
	Aired   time.Time
}

// Source is one playable stream. Headers derives the request headers;
// players and downloaders must call it instead of duplicating the
// Referer/Origin/User-Agent/Cookie logic.
type Source struct {
	URL       string
	Quality   string
	Provider  string
	Referer   string
	UserAgent string
	Cookie    string
	Language  string
	ExtraArgs []string
	// SuppressOrigin stops Headers from deriving Origin from Referer for
	// CDNs that reject any Origin header.
	SuppressOrigin bool
}

// Headers derives the HTTP request headers for the source. It is the
// single place Referer, Origin, User-Agent and Cookie are derived: Origin
// is scheme://host parsed from Referer unless SuppressOrigin is set.
func (s Source) Headers() http.Header {
	h := make(http.Header)
	if ua := strings.TrimSpace(s.UserAgent); ua != "" {
		h.Set("User-Agent", ua)
	}
	if ref := strings.TrimSpace(s.Referer); ref != "" {
		h.Set("Referer", ref)
		if !s.SuppressOrigin {
			if origin := originFromReferer(ref); origin != "" {
				h.Set("Origin", origin)
			}
		}
	}
	if cookie := strings.TrimSpace(s.Cookie); cookie != "" {
		h.Set("Cookie", cookie)
	}
	return h
}

// originFromReferer extracts scheme://host from a referer URL.
func originFromReferer(referer string) string {
	for i := 0; i < len(referer); i++ {
		if referer[i] == ':' && i+3 <= len(referer) && referer[i+1] == '/' && referer[i+2] == '/' {
			end := strings.IndexAny(referer[i+3:], "/?#")
			if end == -1 {
				return referer
			}
			return referer[:i+3+end]
		}
	}
	return ""
}

// Page is one manga page image.
type Page struct {
	URL         string
	Referer     string
	UserAgent   string
	FallbackURL string
}
