package vyla

// sseMessage represents the envelope for incoming Server-Sent Events from
// Vyla Stream API.
type sseMessage struct {
	Type      string         `json:"type"`
	Meta      *mediaMeta     `json:"meta,omitempty"`
	Subtitles []vylaSubtitle `json:"subtitles,omitempty"`
	Source    *vylaSource    `json:"source,omitempty"`
	Total     int            `json:"total,omitempty"`
}

// mediaMeta captures title metadata emitted in the SSE meta event.
type mediaMeta struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	IMDBID        string `json:"imdb_id"`
}

// vylaSubtitle represents one subtitle track descriptor from Vyla's
// multi-source subtitle aggregator.
type vylaSubtitle struct {
	Label  string `json:"label"`
	File   string `json:"file"`
	Type   string `json:"type"` // "vtt" or "srt"
	Source string `json:"source"`
}

// vylaSource represents one verified stream candidate delivered by Vyla.
type vylaSource struct {
	Source   string            `json:"source"`
	Index    int               `json:"index"`
	URL      string            `json:"url"`
	RawURL   string            `json:"raw_url"`
	Proxied  bool              `json:"proxied"`
	Label    string            `json:"label"`
	Quality  string            `json:"quality"`
	Type     string            `json:"type"` // "hls", "mp4", "dash"
	Name     string            `json:"name"`
	Language string            `json:"language"`
	Headers  map[string]string `json:"headers"`
}
