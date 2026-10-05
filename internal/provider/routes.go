package provider

import (
	"strings"
	"time"
)

// Strategy selects how a mode's providers are used for one stage.
type Strategy string

// Routing strategies.
const (
	// StrategyMerge queries every provider in parallel and merges results.
	StrategyMerge Strategy = "merge"
	// StrategyFallback tries providers in order, moving on next on
	// error/empty/timeout.
	StrategyFallback Strategy = "fallback"
	// StrategyRace returns the first usable result and cancels the rest.
	StrategyRace Strategy = "race"
	// StrategySingle uses the lone provider for the mode.
	StrategySingle Strategy = "single"
)

// Search and source deadline budgets. A slow or dead provider never
// delays results beyond its per-provider deadline plus the overall
// context deadline enforced by the service.
const (
	DefaultSearchTimeout  = 8 * time.Second
	DefaultSourceTimeout  = 12 * time.Second
	DefaultSearchOverall  = 12 * time.Second
	DefaultSourcesOverall = 45 * time.Second

	// DefaultSingleSearchTimeout bounds a lone provider's search. A
	// single-provider mode has no sibling route to fall back on, so the
	// shared 8s cap would abort the mode's only search (a live browse
	// fans out to a multi-megabyte channel catalog) instead of giving
	// it room, while still finishing inside DefaultSearchOverall so
	// partial results reach the caller before the overall deadline.
	DefaultSingleSearchTimeout = 10 * time.Second
)

// Route is one provider's role in a mode: which strategy governs its
// search and source stages, its per-stage deadlines, and whether it is
// disabled with a reason. Disabled routes stay listed (never silently
// deleted) so status reports can show why a provider is out.
type Route struct {
	Provider      string
	Search        Strategy
	Sources       Strategy
	SearchTimeout time.Duration
	SourceTimeout time.Duration
	Disabled      bool
	Reason        string
}

// searchTimeout returns the effective per-provider search deadline.
func (r Route) searchTimeout() time.Duration {
	if r.SearchTimeout > 0 {
		return r.SearchTimeout
	}
	return DefaultSearchTimeout
}

// sourceTimeout returns the effective per-provider source deadline.
func (r Route) sourceTimeout() time.Duration {
	if r.SourceTimeout > 0 {
		return r.SourceTimeout
	}
	return DefaultSourceTimeout
}

// defaultRoutes is the single source of truth for provider routing:
// anime merges searches and falls back for sources; movies/tv/cartoon
// merge searches and race (with preference) for sources; live, manga
// and jellyfin are single-provider modes.
func defaultRoutes() map[ContentType][]Route {
	anime := []Route{
		{Provider: "anicine", Search: StrategyMerge, Sources: StrategyFallback},
		{Provider: "anikoto", Search: StrategyMerge, Sources: StrategyFallback},
		{Provider: "miruro", Search: StrategyMerge, Sources: StrategyFallback},
		{Provider: "anilight", Search: StrategyMerge, Sources: StrategyFallback},
	}
	movies := []Route{
		{Provider: "anicine", Search: StrategyMerge, Sources: StrategyRace},
		{Provider: "movysx", Search: StrategyMerge, Sources: StrategyRace},
		{Provider: "pengu", Search: StrategyMerge, Sources: StrategyRace},
		{Provider: "moovie", Search: StrategyMerge, Sources: StrategyRace},
	}
	return map[ContentType][]Route{
		ModeAnime:   anime,
		ModeMovies:  movies,
		ModeTV:      movies,
		ModeCartoon: movies,
		ModeLive: {
			{Provider: "pengu", Search: StrategySingle, Sources: StrategySingle},
		},
		ModeManga: {
			{Provider: "weebcentral", Search: StrategySingle, Sources: StrategySingle},
		},
		ModeJellyfin: {
			{Provider: "jellyfin", Search: StrategySingle, Sources: StrategySingle},
		},
	}
}

// RoutesFor returns the routing table for mode in registry priority
// order. Unknown modes yield no routes.
func RoutesFor(mode ContentType) []Route {
	return defaultRoutes()[mode]
}

// SearchStrategy returns the governing search strategy for mode:
// single when one route, merge when any route merges, else fallback.
func SearchStrategy(mode ContentType) Strategy {
	routes := RoutesFor(mode)
	if len(routes) == 1 {
		return StrategySingle
	}
	for _, r := range routes {
		if r.Search == StrategyMerge {
			return StrategyMerge
		}
	}
	return StrategyFallback
}

// SourcesStrategy returns the governing source strategy for mode.
func SourcesStrategy(mode ContentType) Strategy {
	routes := RoutesFor(mode)
	if len(routes) == 1 {
		return StrategySingle
	}
	for _, r := range routes {
		if r.Sources == StrategyRace {
			return StrategyRace
		}
		if r.Sources == StrategyMerge {
			return StrategyMerge
		}
	}
	return StrategyFallback
}

// sourceBoost is a data-driven sort preference: matching sources sort
// above the rest. It replaces the old name switches in service and tui.
type sourceBoost struct {
	Resolver        string
	QualityContains string
}

// sourceBoosts lists per-mode sort preferences in order. Anime prefers
// Anikoto's Vidstream-2 backend; movies/tv/cartoon prefer Movy.sx.
// Live, manga and jellyfin need no boost (single provider).
func sourceBoosts(mode ContentType) []sourceBoost {
	switch mode {
	case ModeAnime:
		return []sourceBoost{{Resolver: "anikoto", QualityContains: "vidstream-2"}}
	case ModeMovies, ModeTV, ModeCartoon:
		return []sourceBoost{{Resolver: "movysx"}}
	default:
		return nil
	}
}

// BoostRank returns the preference index of resolver/quality for mode,
// or -1 when the source matches no boost. Lower ranks sort first.
func BoostRank(mode ContentType, resolver, quality string) int {
	for i, b := range sourceBoosts(mode) {
		if !strings.EqualFold(resolver, b.Resolver) {
			continue
		}
		if b.QualityContains != "" && !strings.Contains(strings.ToLower(quality), strings.ToLower(b.QualityContains)) {
			continue
		}
		return i
	}
	return -1
}

// PreferredResolvers lists the resolver names boosted for mode, in
// order. The tui reads this instead of switching on provider names.
func PreferredResolvers(mode ContentType) []string {
	var out []string
	for _, b := range sourceBoosts(mode) {
		out = append(out, b.Resolver)
	}
	return out
}
