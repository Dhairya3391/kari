package provider

import (
	"slices"
	"strings"
)

// Registry stores registered providers and returns providers by mode.
type Registry struct {
	providers []Provider
}

// Register adds a provider to the registry. It is idempotent.
func (r *Registry) Register(p Provider) {
	for _, existing := range r.providers {
		if existing.Name() == p.Name() {
			return
		}
	}
	r.providers = append(r.providers, p)
}

// ProvidersForMode returns providers supporting the given mode, ordered by priority.
func (r *Registry) ProvidersForMode(mode ContentType) []Provider {
	type rankedProvider struct {
		p        Provider
		priority int
	}
	var matched []rankedProvider
	for _, p := range r.providers {
		for _, m := range p.Modes() {
			if m.Name == mode {
				matched = append(matched, rankedProvider{p: p, priority: m.Priority})
				break
			}
		}
	}

	slices.SortStableFunc(matched, func(a, b rankedProvider) int {
		return a.priority - b.priority
	})

	out := make([]Provider, len(matched))
	for i, mp := range matched {
		out[i] = mp.p
	}
	return out
}

// ProviderByName returns the registered provider with the given name.
func (r *Registry) ProviderByName(name string) (Provider, bool) {
	for _, p := range r.providers {
		if strings.EqualFold(p.Name(), name) {
			return p, true
		}
	}
	return nil, false
}

// ProviderByNameForMode returns the provider with the given name if it
// supports the given mode (case-insensitive name match).
func (r *Registry) ProviderByNameForMode(name string, mode ContentType) (Provider, bool) {
	for _, p := range r.ProvidersForMode(mode) {
		if strings.EqualFold(p.Name(), name) {
			return p, true
		}
	}
	return nil, false
}

// DisplayName returns the user-facing codename for the named provider:
// its declared Alias when the provider implements Presenter, otherwise the
// internal name.
func (r *Registry) DisplayName(name string) string {
	p, ok := r.ProviderByName(name)
	if !ok {
		return name
	}
	if pr, ok := p.(Presenter); ok && pr.Alias() != "" {
		return pr.Alias()
	}
	return p.Name()
}

// Features aggregates the FeatureSource declarations of every provider
// supporting mode. The zero value means default behavior: caching enabled,
// nothing else on. AllowEmptyQuery/AudioSelection are enabled when any
// provider declares them; NoCachedSearches when any provider opts out.
func (r *Registry) Features(mode ContentType) Features {
	f := Features{}
	for _, p := range r.ProvidersForMode(mode) {
		fs, ok := p.(FeatureSource)
		if !ok {
			continue
		}
		pf := fs.Features(mode)
		if pf.AllowEmptyQuery {
			f.AllowEmptyQuery = true
		}
		if pf.NoCachedSearches {
			f.NoCachedSearches = true
		}
		if f.SearchPlaceholder == "" {
			f.SearchPlaceholder = pf.SearchPlaceholder
		}
		if pf.AudioSelection {
			f.AudioSelection = true
		}
	}
	return f
}

// AudioLanguages returns the union of audio languages declared by providers
// supporting ANY of the given modes (or all providers in the registry when no
// mode is given), in provider priority order, deduplicated case-insensitively
// by Code. Providers that don't implement AudioLanguagesSource contribute nothing.
func (r *Registry) AudioLanguages(modes ...ContentType) []AudioLanguage {
	var out []AudioLanguage
	seen := make(map[string]struct{})
	var providers []Provider
	if len(modes) == 0 {
		providers = r.providers
	} else {
		for _, mode := range modes {
			providers = append(providers, r.ProvidersForMode(mode)...)
		}
	}
	for _, p := range providers {
		als, ok := p.(AudioLanguagesSource)
		if !ok {
			continue
		}
		for _, l := range als.AudioLanguages() {
			key := strings.ToLower(strings.TrimSpace(l.Code))
			if key == "" {
				continue
			}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, l)
		}
	}
	return out
}

// RequiresEpisodeListForMovies reports whether the named provider needs a
// fetched episode listing even for movie-titled results. Unknown providers,
// and providers not implementing MovieEpisodeFlow, resolve movies directly.
func (r *Registry) RequiresEpisodeListForMovies(providerName string) bool {
	p, ok := r.ProviderByName(providerName)
	if !ok {
		return false
	}
	mef, ok := p.(MovieEpisodeFlow)
	if !ok {
		return false
	}
	return mef.RequiresEpisodeListForMovies()
}

// canonicalModeOrder is the tab-strip order from the UI spec (§5.6):
// jellyfin sits last when its server is configured. Modes outside this
// list (from a future provider) keep alphabetical relative order at the
// end so nothing registered silently disappears.
var canonicalModeOrder = []ContentType{
	ModeAnime, ModeCartoon, ModeMovies, ModeTV, ModeJellyfin, ModeLive, ModeManga,
}

// AllModes returns the unique modes supported by registered providers in
// canonical tab order. Modes only appear when a provider supplying them
// is registered — jellyfin and live therefore show up exactly when their
// providers are configured.
func (r *Registry) AllModes() []ContentType {
	modeSet := make(map[ContentType]struct{})
	for _, p := range r.providers {
		for _, m := range p.Modes() {
			modeSet[m.Name] = struct{}{}
		}
	}

	modes := make([]ContentType, 0, len(modeSet))
	for _, m := range canonicalModeOrder {
		if _, ok := modeSet[m]; ok {
			modes = append(modes, m)
			delete(modeSet, m)
		}
	}
	if len(modeSet) > 0 {
		extra := make([]string, 0, len(modeSet))
		for m := range modeSet {
			extra = append(extra, string(m))
		}
		slices.Sort(extra)
		for _, m := range extra {
			modes = append(modes, ContentType(m))
		}
	}
	return modes
}
