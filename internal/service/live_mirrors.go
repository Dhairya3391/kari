package service

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode"

	"kari/internal/provider"
)

const (
	// liveMirrorTimeout bounds the whole cross-provider fallback pass. It only
	// runs when the selected provider produced nothing playable, so a short
	// budget keeps a dead channel from stalling the playback screen.
	liveMirrorTimeout = 8 * time.Second
	// liveMirrorProviderTimeout caps one mirror provider inside that pass;
	// live catalogs can hang, and the fallback is best-effort.
	liveMirrorProviderTimeout = 6 * time.Second
	// liveMirrorGrace is how long the pass keeps collecting extra mirrors
	// after the first one lands. One working source is enough to play, so
	// waiting for every provider would only slow the dead-channel path.
	liveMirrorGrace = 1500 * time.Millisecond
)

// liveMirror is one fallback provider batch resolved for the same channel
// title as the selected result.
type liveMirror struct {
	provider string
	sources  []provider.MediaSource
}

// resolveLiveMirrors searches the other enabled live providers for the same
// title and resolves their matching channel. Live catalogs are keyed by
// channel name rather than a shared TMDB ID, so a title match is the only
// cross-provider handle available. Providers that fail or expose no match are
// skipped; the caller merges whatever comes back.
func (s *MediaService) resolveLiveMirrors(ctx context.Context, mode provider.ContentType, series provider.SearchResult, routes []provider.Route, exclude []string) []liveMirror {
	target := liveTitleKey(series.Title)
	if target == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, liveMirrorTimeout)
	defer cancel()

	found := make(chan liveMirror, len(routes))
	var wg sync.WaitGroup
	for _, route := range routes {
		p := route.Provider
		if strings.EqualFold(p.Name(), series.Provider) || excludedProvider(exclude, p.Name()) {
			continue
		}
		if s.IsProviderDisabled(mode, p.Name()) {
			continue
		}
		if route.Playback == nil {
			continue
		}
		if skipped, _ := s.health.Skipped(p.Name(), false); skipped {
			continue
		}
		source := route.Playback
		timeout := min(providerSourceTimeout(route), liveMirrorProviderTimeout)
		wg.Add(1)
		go func() {
			defer wg.Done()
			pCtx, pCancel := context.WithTimeout(ctx, timeout)
			defer pCancel()
			sources := s.resolveLiveMirror(pCtx, p, source, target)
			if len(sources) == 0 {
				return
			}
			s.health.RecordSuccess(p.Name())
			found <- liveMirror{provider: p.Name(), sources: sources}
		}()
	}
	go func() {
		wg.Wait()
		close(found)
	}()

	// Stop shortly after the first mirror so one slow catalog cannot hold the
	// playback screen open once a playable source already exists.
	var (
		mirrors []liveMirror
		grace   <-chan time.Time
	)
	for {
		select {
		case mirror, ok := <-found:
			if !ok {
				return mirrors
			}
			mirrors = append(mirrors, mirror)
			if grace == nil {
				timer := time.NewTimer(liveMirrorGrace)
				defer timer.Stop()
				grace = timer.C
			}
		case <-grace:
			return mirrors
		case <-ctx.Done():
			return mirrors
		}
	}
}

// resolveLiveMirror finds the provider's result whose normalized title matches
// the selected channel and resolves it to verified sources. The query is the
// normalized title too: raw channel names carry quality markers ("HD+") that
// sibling catalogs do not use, so searching them verbatim finds nothing.
func (s *MediaService) resolveLiveMirror(ctx context.Context, p provider.Provider, source provider.EpisodeSource, target string) []provider.MediaSource {
	results, err := p.Search(ctx, target, provider.ModeLive)
	if err != nil {
		return nil
	}
	var match provider.SearchResult
	found := false
	for _, result := range results {
		if liveTitleKey(result.Title) == target {
			match, found = result, true
			break
		}
	}
	if !found {
		return nil
	}
	episodes, err := source.FetchEpisodes(ctx, match)
	if err != nil {
		return nil
	}
	episode := provider.Episode{}
	if len(episodes) > 0 {
		episode = episodes[0]
	}
	sources, err := source.ResolveSource(ctx, match.ID, episode)
	if err != nil || len(sources) == 0 {
		return nil
	}
	if verifier, ok := p.(provider.LiveStreamVerifier); ok {
		sources = verifyLiveSources(ctx, provider.ModeLive, verifier, sources)
	}
	return sources
}

// liveTitleKey normalizes a channel name for cross-provider matching. Live
// catalogs disagree on quality and feed markers ("Cartoon Network HD+" vs
// "Cartoon Network"), so those tokens are dropped while the channel identity
// is kept.
func liveTitleKey(title string) string {
	fields := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	kept := make([]string, 0, len(fields))
	for _, field := range fields {
		switch field {
		case "hd", "fhd", "uhd", "sd", "4k", "8k", "plus":
			continue
		}
		kept = append(kept, field)
	}
	return strings.Join(kept, " ")
}

// excludedProvider reports whether name appears in the resolve exclusion list.
func excludedProvider(exclude []string, name string) bool {
	for _, candidate := range exclude {
		if strings.EqualFold(strings.TrimSpace(candidate), name) {
			return true
		}
	}
	return false
}
