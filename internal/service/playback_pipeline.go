package service

import (
	"time"

	"kari/internal/filter"
	"kari/internal/provider"
)

// PlaybackOptions contains filters applied to visible sources. The legacy
// field types are kept at this boundary so CLI and TUI callers do not need to
// know about the domain filter package; they are converted once, here.
type PlaybackOptions struct {
	Mode                  provider.ContentType
	QualityMode           int
	PreferredProvider     string
	EnabledAudioLanguages map[string]bool
	// Preferences, when set, is the typed per-mode policy supplied by the
	// TUI. It takes precedence over the legacy fields above so settings are
	// converted to the domain vocabulary exactly once.
	Preferences *filter.Preferences
}

// PreparePlaybackSources filters and orders the visible rows using the single
// domain pipeline. Providers keep their first-seen order, the preferred one
// first, while adaptive rows remain eligible at every quality tier.
func PreparePlaybackSources(sources []provider.MediaSource, options PlaybackOptions) []filter.RankedSource {
	if options.Preferences != nil {
		prefs := *options.Preferences
		if prefs.PreferredProvider == "" {
			prefs.PreferredProvider = options.PreferredProvider
		}
		ranked := filter.Prepare(sources, prefs, options.Mode, nil, time.Time{})
		return filter.GroupByProvider(ranked, prefs.PreferredProvider)
	}
	prefs := filter.FromLegacy(
		options.QualityMode,
		options.QualityMode,
		options.EnabledAudioLanguages,
		"",
		"",
		false,
		options.PreferredProvider,
	).For(options.Mode)
	ranked := filter.Prepare(sources, prefs, options.Mode, nil, time.Time{})
	return filter.GroupByProvider(ranked, options.PreferredProvider)
}
