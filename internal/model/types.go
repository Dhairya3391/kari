package model

type SearchResult struct {
	Title     string
	URL       string
	Provider  string
	MediaType string
	Year      string
	TMDBID    int
}

type EpisodeResult struct {
	Title     string
	URL       string
	Number    int
	Season    int
	Kind      string
	Provider  string
	MediaType string
	Filler    bool
}

type PlaybackSource struct {
	Label        string
	URL          string
	Referer      string
	Type         string
	CookieHeader string
	UserAgent    string
	Resolver     string
}

type SubtitleTrack struct {
	Label    string
	Language string
	Path     string
	URL      string
	Referer  string
	Default  bool
}

type ResolvedMedia struct {
	SeriesTitle   string
	SeriesURL     string
	EpisodeTitle  string
	EpisodeURL    string
	IframeURL     string
	GetvidlinkURL string
	MediaURL      string
	MediaType     string
	Year          string
	TMDBID        int
	SeasonNumber  int
	EpisodeNumber int
	Resolver      string
	Playback      []PlaybackSource
	Subtitles     []SubtitleTrack
	StartTime     float64
}
