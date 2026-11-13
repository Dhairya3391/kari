package service

import (
	"kari/internal/provider"
	"kari/internal/ranking"
)

// PlaybackOptions contains filters applied to visible sources.
type PlaybackOptions struct {
	Mode                  provider.ContentType
	QualityMode           int
	PreferredProvider     string
	EnabledAudioLanguages map[string]bool
}

// PreparePlaybackSources applies audio and quality filters, keeping the
// preferred provider first and arrival order otherwise. Adaptive sources remain
// eligible at every tier; no quality scoring or sorting is applied.
func PreparePlaybackSources(sources []provider.MediaSource, options PlaybackOptions) []ranking.ScoredSource {
	sources = ranking.FilterAudioLanguages(sources, options.Mode, options.EnabledAudioLanguages)
	sources = ranking.PreferProvider(sources, options.PreferredProvider)
	indices := FilterPlaybackIndices(sources, options.QualityMode, nil)
	visible := make([]bool, len(sources))
	for _, index := range indices {
		visible[index] = true
	}
	scored := make([]ranking.ScoredSource, 0, len(sources))
	for i, source := range sources {
		if !visible[i] && !ranking.IsAdaptiveQuality(source.Quality) {
			continue
		}
		scored = append(scored, ranking.ScoredSource{
			Source:        source,
			OriginalIndex: i,
		})
	}
	return scored
}
