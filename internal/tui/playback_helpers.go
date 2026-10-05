package tui

import (
	"strings"
	"time"

	"kari/internal/provider"
	"kari/internal/ranking"
)

func (m *modelImpl) orderedEnabledLanguages() []string {
	allLangs := m.availableLanguages()
	var enabled []string
	for _, l := range allLangs {
		if m.languageFilter[l.Code] || m.languageFilter[l.Display] {
			enabled = append(enabled, l.Display)
		}
	}
	return enabled
}

func (m *modelImpl) rankAndSelectSources() {
	if m.resolved == nil || len(m.resolved.Playback) == 0 {
		m.rankedSources = nil
		m.previewSelectedIndex = 0
		return
	}

	sources := m.resolved.Playback
	// Highest shows FHD/4K rows only, falling back to the best available
	// tier when nothing reaches FHD. Other modes order every row.
	if m.qualityMode == qualityHighest {
		sources = ranking.KeepHighestVisible(sources)
	}

	sticky := ""
	if m.stickyProviders != nil {
		sticky = m.stickyProviders[m.resolved.SeriesTitle]
	}

	crit := ranking.Criteria{
		Mode:                  m.appMode,
		QualityMode:           m.qualityMode,
		EnabledAudioLanguages: m.orderedEnabledLanguages(),
		AnimeAudio:            m.audioMode,
		AnimeSubtitlesEnabled: !m.disableAnimeSubtitles,
		PreferredSubtitleLang: m.subtitleLanguage,
		StickyProvider:        sticky,
		FailedProviders:       m.failedProviders,
		Now:                   time.Now(),
	}

	m.rankedSources = hardsubFirst(m.appMode, m.qualityMode, !m.disableAnimeSubtitles,
		preferredFirst(m.appMode, ranking.RankSources(sources, crit)))
	m.previewSelectedIndex = 0
}

// hardsubFirst stable-partitions hard-subtitled anime rows above the rest,
// preserving the ranked order inside each group. Burned-in subs need no
// subtitle plumbing and cannot desync, so they are the default pick —
// ahead of even preferred-resolver softsubs. It applies only when anime
// subtitles are enabled (a forced track would defeat an explicit off)
// and never in size-first quality modes (Lowest/Data Saver), where the
// user asked for small files. Challenged-host rows never join the top
// group: they stay last, always.
func hardsubFirst(mode provider.ContentType, qualityMode int, subsEnabled bool, sources []ranking.ScoredSource) []ranking.ScoredSource {
	if mode != provider.ModeAnime || !subsEnabled {
		return sources
	}
	if qualityMode != qualityAll && qualityMode != qualityHighest {
		return sources
	}
	var top, rest []ranking.ScoredSource
	for _, s := range sources {
		if strings.EqualFold(strings.TrimSpace(s.Source.SubType), provider.SubTypeHard) &&
			!provider.ChallengedHost(s.Source.URL) {
			top = append(top, s)
		} else {
			rest = append(rest, s)
		}
	}
	return append(top, rest...)
}

// preferredFirst stable-partitions rows from the routes-table preferred
// resolvers above the rest, preserving the ranked order inside each
// group. It mirrors the service aggregator so Preview and playback agree
// on the top pick, including when preferred rows arrive in a later
// progressive snapshot (refreshRanking re-runs this every merge, so the
// list updates dynamically and a cursor sitting on the top row tracks
// the new pick). Challenged-host rows never join the top group even when
// their resolver is preferred: they stay last, always.
func preferredFirst(mode provider.ContentType, sources []ranking.ScoredSource) []ranking.ScoredSource {
	preferred := provider.PreferredResolvers(mode)
	if len(preferred) == 0 {
		return sources
	}
	var top, rest []ranking.ScoredSource
	for _, s := range sources {
		matched := false
		if !provider.ChallengedHost(s.Source.URL) {
			for _, name := range preferred {
				if strings.EqualFold(s.Source.Resolver, name) {
					matched = true
					break
				}
			}
		}
		if matched {
			top = append(top, s)
		} else {
			rest = append(rest, s)
		}
	}
	return append(top, rest...)
}

// refreshRanking recomputes the ranked order from the current Playback
// snapshot. Providers stream in progressively, so every merge must
// re-rank, or the first snapshot's order sticks and later arrivals never
// appear in Preview. A cursor sitting on the auto-selected top row
// (index 0) tracks the new top pick; a cursor the user moved is kept on
// the same source URL.
func (m *modelImpl) refreshRanking() {
	trackBest := m.previewSelectedIndex == 0
	selectedURL := ""
	if !trackBest {
		if src, ok := m.selectedPlaybackSource(); ok {
			selectedURL = src.URL
		}
	}
	m.rankAndSelectSources()
	if trackBest || selectedURL == "" {
		return
	}
	for i, s := range m.rankedSources {
		if s.Source.URL == selectedURL {
			m.previewSelectedIndex = i
			return
		}
	}
}

func (m *modelImpl) selectedPlaybackSource() (provider.MediaSource, bool) {
	if len(m.rankedSources) == 0 {
		if m.resolved != nil && len(m.resolved.Playback) > 0 {
			return m.resolved.Playback[0], true
		}
		return provider.MediaSource{}, false
	}
	if m.previewSelectedIndex < 0 || m.previewSelectedIndex >= len(m.rankedSources) {
		return m.rankedSources[0].Source, true
	}
	return m.rankedSources[m.previewSelectedIndex].Source, true
}

func (m *modelImpl) orderedPlaybackSources() []provider.MediaSource {
	if len(m.rankedSources) == 0 {
		if m.resolved == nil {
			return nil
		}
		return m.resolved.Playback
	}

	total := len(m.rankedSources)
	start := m.previewSelectedIndex
	if start < 0 || start >= total {
		start = 0
	}

	out := make([]provider.MediaSource, 0, total)
	for i := range total {
		idx := (start + i) % total
		out = append(out, m.rankedSources[idx].Source)
	}
	return out
}
