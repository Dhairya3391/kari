package tui

import (
	"strings"

	"kari/internal/provider"
	"kari/internal/provider/kit"
)

// Load serialization and play readiness live here: one media pipeline at
// a time (idempotent loads), and playback starts once the first source
// is in. Subtitles are fetched in the background and attach to the
// resolved media when ready; they never gate playback.

// loadInFlight reports whether any user-visible load is still running: a
// flagged spinner phase or an operation whose completion clears its opID.
// Search/episode/history-continue operations are covered by loading (set
// at start, cleared at done); resolve, play, chapters and pages fetches
// clear their opIDs on completion, so a cleared opID with loading
// already false means settled. Download and reader-render work
// stays out: downloads run in the background by design.
func (m *modelImpl) loadInFlight() bool {
	return m.loading ||
		m.resolveOpID != 0 ||
		m.playOpID != 0 ||
		m.chaptersOpID != 0 ||
		m.pagesOpID != 0
}

// inFlightText names the running load for status display.
func (m *modelImpl) inFlightText() string {
	if strings.TrimSpace(m.loadingText) != "" {
		return strings.TrimSpace(m.loadingText)
	}
	switch {
	case m.resolveOpID != 0:
		return "resolving streams"
	case m.playOpID != 0:
		return "opening player"
	case m.chaptersOpID != 0:
		return "loading chapters"
	case m.pagesOpID != 0:
		return "loading pages"
	default:
		return "loading"
	}
}

// guardLoad reports whether a new user-initiated load may start. While
// any load is in flight it shows what is running and refuses, so the
// user can neither abandon the in-flight operation nor stack a second
// one on top of it. Completion-chained continuations (autoplay,
// auto-advance, history resume steps) run with settled state and never
// hit the guard.
func (m *modelImpl) guardLoad() bool {
	if !m.loadInFlight() {
		return true
	}
	m.setStatus(statusInfo, "Please wait — "+m.inFlightText())
	return false
}

// pushPlayingView ensures playback runs in-place on the preview screen
// with active playback indicators.
func (m *modelImpl) pushPlayingView() {
	if m.activeView != viewPreview {
		m.pushView(viewPreview)
	}
}

// subtitlesWanted reports whether a subtitle fetch makes sense for the
// current settings and selection. It only prevents wasted network work;
// playback never waits on it.
func (m *modelImpl) subtitlesWanted() bool {
	if strings.EqualFold(strings.TrimSpace(m.subtitleLanguage), "off") {
		return false
	}
	if m.disableAnimeSubtitles && m.resolved != nil && m.resolved.MediaType == provider.MediaTypeAnime {
		return false
	}
	if src, ok := m.selectedPlaybackSource(); ok &&
		src.SubType == provider.SubTypeHard &&
		len(src.Subtitles) == 0 {
		return false
	}
	return true
}

// mediaReady reports whether at least one playable source is in.
func (m *modelImpl) mediaReady() bool {
	return len(m.orderedPlaybackSources()) > 0
}

// beginProviderWait opens the preview provider countdown: every provider
// serving mode is pending until its sources arrive in a snapshot.
func (m *modelImpl) beginProviderWait(mode provider.ContentType) {
	m.totalProviders = len(m.registry.ProvidersForMode(mode))
	m.loadingProviders = m.totalProviders
	m.failedProviderName = ""
}

// refreshProviderWait recounts pending providers from the distinct
// resolvers present in the aggregated snapshot.
func (m *modelImpl) refreshProviderWait() {
	if m.totalProviders == 0 || m.resolved == nil {
		return
	}
	seen := make(map[string]struct{}, len(m.resolved.Playback))
	for _, src := range m.resolved.Playback {
		seen[src.Resolver] = struct{}{}
	}
	remaining := m.totalProviders - len(seen)
	if remaining < 0 {
		remaining = 0
	}
	m.loadingProviders = remaining
}

// endProviderWait closes the countdown, naming the first failed provider
// (if any) so the preview can offer a retry.
func (m *modelImpl) endProviderWait() {
	m.loadingProviders = 0
	m.failedProviderName = ""
	if m.mediaService != nil {
		if failed := m.mediaService.LastFailures(); len(failed) > 0 {
			m.failedProviderName = failed[0]
		}
	}
}

// seenResolvers lists providers with rows in the current aggregate,
// in first-seen order.
func (m *modelImpl) seenResolvers() []string {
	if m.resolved == nil {
		return nil
	}
	var out []string
	seen := make(map[string]struct{})
	for _, src := range m.resolved.Playback {
		if _, ok := seen[src.Resolver]; !ok {
			seen[src.Resolver] = struct{}{}
			out = append(out, src.Resolver)
		}
	}
	return out
}

// retryExclude returns resolvers a retry must skip: seen delivering
// providers that did not fail. Failed providers are always re-queried,
// even when they delivered partial rows first. Empty when nothing
// failed, meaning a full refresh (e.g. expired signed URLs).
func (m *modelImpl) retryExclude() []string {
	if m.mediaService == nil {
		return nil
	}
	failed := m.mediaService.LastFailures()
	if len(failed) == 0 {
		return nil
	}
	isFailed := func(name string) bool {
		for _, f := range failed {
			if strings.EqualFold(f, name) {
				return true
			}
		}
		return false
	}
	var out []string
	for _, name := range m.seenResolvers() {
		if !isFailed(name) {
			out = append(out, name)
		}
	}
	return out
}

// beginRetryWait reopens the provider countdown for a retry that skips
// exclude providers (already delivered): only the re-queried ones count.
func (m *modelImpl) beginRetryWait(mode provider.ContentType, exclude []string) {
	m.beginProviderWait(mode)
	if n := len(exclude); n > 0 && n < m.totalProviders {
		m.totalProviders -= n
		m.loadingProviders = m.totalProviders
	}
}

// preferredRepairNeeded reports whether the last resolve left a
// routes-table preferred provider failed while others delivered: the
// case for one quiet background repair instead of a manual retry.
func (m *modelImpl) preferredRepairNeeded() bool {
	if m.selectedSeries == nil || m.selectedEpisode == nil || m.mediaService == nil {
		return false
	}
	preferred := provider.PreferredResolvers(m.appMode)
	if len(preferred) == 0 {
		return false
	}
	for _, f := range m.mediaService.LastFailures() {
		for _, p := range preferred {
			if strings.EqualFold(f, p) {
				return true
			}
		}
	}
	return false
}

// sourceBackendName names the backend behind one source for display:
// the [Backend] tag embedded in the quality label (what the aggregator
// actually serves, e.g. 4KHDHub behind Pengu), falling back to the
// provider's display name. The resolver alone (e.g. "pengu") never
// identifies the stream to the user.
func sourceBackendName(src provider.MediaSource, displayName func(string) string) string {
	if tag := qualityTag(src.Quality); tag != "" {
		return tag
	}
	if displayName != nil {
		if name := displayName(src.Resolver); name != "" {
			return name
		}
	}
	if strings.TrimSpace(src.Resolver) != "" {
		return src.Resolver
	}
	return "—"
}

// qualityTag extracts the [Backend] token via the shared kit helper.
func qualityTag(quality string) string {
	return kit.BackendTag(quality)
}
