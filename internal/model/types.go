package model

import "kari/internal/provider"

// SubtitleTrack is one subtitle offering attached to resolved media. Tracks
// arrive from providers as URL references; Path is filled in once a track has
// been validated and downloaded to disk.
type SubtitleTrack struct {
	Label     string
	Language  string
	Path      string
	URL       string
	Referer   string
	SourceURL string
	SourceID  string
	Default   bool
	Resolver  string
}

// ResolvedMedia aggregates every playback source and subtitle track
// resolved for one selection, plus the metadata services need to scrobble,
// organize downloads, and display it.
type ResolvedMedia struct {
	SeriesTitle   string
	SeriesURL     string
	EpisodeTitle  string
	EpisodeURL    string
	MediaURL      string
	MediaType     string
	Year          string
	TMDBID        int
	SeasonNumber  int
	EpisodeNumber int
	Resolver      string
	// Playback holds the merged, quality-sorted sources across providers.
	// Entries are provider.MediaSource values with Resolver stamped by the
	// MediaService aggregation.
	Playback  []provider.MediaSource
	Subtitles []SubtitleTrack
	// SelectedSubtitle is the single validated local track passed to playback.
	// Subtitles remains the unselected provider candidate list.
	SelectedSubtitle *SubtitleTrack
	StartTime        float64
}
