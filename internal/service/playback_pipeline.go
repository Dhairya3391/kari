package service

import (
	"kari/internal/provider"
	"kari/internal/ranking"
)

// PlaybackOptions contains the user's playback preferences applied before
// playback sources are presented or selected.
type PlaybackOptions struct {
	Mode                  provider.ContentType
	QualityMode           int
	EnabledAudioLanguages map[string]bool
	SubtitlesEnabled      bool
}

// PreparePlaybackSources applies filtering and ordering stages in one place.
// The TUI supplies preferences and renders the resulting ordered sources.
func PreparePlaybackSources(sources []provider.MediaSource, options PlaybackOptions) []ranking.ScoredSource {
	sources = ranking.FilterAudioLanguages(sources, options.Mode, options.EnabledAudioLanguages)
	if options.QualityMode == ranking.QualityHighest {
		sources = ranking.KeepHighestVisible(sources)
	}

	scored := make([]ranking.ScoredSource, len(sources))
	for i, source := range sources {
		scored[i] = ranking.ScoredSource{
			Source:        source,
			OriginalIndex: i,
		}
	}
	scored = ranking.GroupSourcesByProvider(scored, options.QualityMode, options.Mode)
	return ranking.HardsubsFirst(options.Mode, options.QualityMode, options.SubtitlesEnabled, scored)
}
