package tui

import (
	"strings"

	"kari/internal/provider"
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
	src, ok := m.selectedPlaybackSource()
	if !ok {
		return false
	}
	if src.SubType == provider.SubTypeHard && len(src.Subtitles) == 0 {
		// A hard-subtitled stream carries its captions burned into the
		// video, so a sidecar would duplicate them — unless the source's
		// own provider also shipped subtitle tracks for this episode
		// (exact match or same-provider mirror, which the subtitle
		// service resolves). Suppress only when the provider offered
		// nothing; provider-provided subs must always load.
		return m.providerSubtitlesAvailable(src.Resolver)
	}
	return true
}

// providerSubtitlesAvailable reports whether the resolver behind the
// selected source declared any subtitle tracks for the episode, exact
// source match or same-provider mirror alike.
func (m *modelImpl) providerSubtitlesAvailable(resolver string) bool {
	resolver = strings.ToLower(strings.TrimSpace(resolver))
	if resolver == "" {
		return false
	}
	for _, track := range m.rawSubtitles {
		if strings.EqualFold(strings.TrimSpace(track.Resolver), resolver) {
			return true
		}
	}
	return false
}

// mediaReady reports whether at least one playable source is in.
func (m *modelImpl) mediaReady() bool {
	return len(m.orderedPlaybackSources()) > 0
}

// enabledProvidersCount counts the number of enabled providers for mode.
func (m *modelImpl) enabledProvidersCount(mode provider.ContentType) int {
	if m.registry == nil {
		return 0
	}
	all := m.registry.ProvidersForMode(mode)
	count := 0
	for _, p := range all {
		if !m.isProviderDisabledKey(mode, p.Name()) {
			count++
		}
	}
	if count == 0 {
		return len(all)
	}
	return count
}

// beginProviderWait opens the preview provider countdown: only enabled providers
// serving mode are counted until their sources arrive in a snapshot.
func (m *modelImpl) beginProviderWait(mode provider.ContentType) {
	m.totalProviders = m.enabledProvidersCount(mode)
	m.loadingProviders = m.totalProviders
	m.failedProviderName = ""
}

// refreshProviderWait recounts pending providers from the distinct
// resolvers present in the aggregated snapshot.
func (m *modelImpl) refreshProviderWait() {
	if m.resolved != nil && m.resolved.TotalResolvers > 0 {
		m.totalProviders = m.resolved.TotalResolvers
	}
	if m.totalProviders == 0 || m.resolved == nil {
		return
	}
	if len(m.resolved.CompletedResolvers) > 0 {
		remaining := m.totalProviders - len(m.resolved.CompletedResolvers)
		if remaining < 0 {
			remaining = 0
		}
		m.loadingProviders = remaining
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
		m.loadingProviders = m.totalProviders - n
	}
}

// preferredRepairNeeded reports whether a provider-declared playback
// priority failed while another provider delivered sources.
func (m *modelImpl) preferredRepairNeeded() bool {
	if m.selectedSeries == nil || m.selectedEpisode == nil || m.mediaService == nil || m.registry == nil {
		return false
	}
	preferred := m.registry.PreferredPlaybackProviders(m.appMode)
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

// sourceBackendName uses provider-supplied backend metadata when present,
// falling back to the provider's display name.
func sourceBackendName(src provider.MediaSource, displayName func(string) string) string {
	if name := strings.TrimSpace(src.Backend); name != "" {
		return name
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
