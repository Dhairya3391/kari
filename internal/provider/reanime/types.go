package reanime

import "encoding/json"

// searchResp represents the response payload returned by Re:ANIME /search endpoint.
type searchResp struct {
	Results []searchResultItem `json:"results"`
}

// searchResultItem represents one anime match in Re:ANIME search results.
type searchResultItem struct {
	ID     json.Number `json:"id"`
	Name   string      `json:"name"`
	Format string      `json:"format"`
	Year   int         `json:"year"`
}

// episodeItem represents one episode returned by Re:ANIME /episodes endpoint.
type episodeItem struct {
	ID       string  `json:"id"`
	Number   float64 `json:"number"`
	Category string  `json:"category"`
	Title    string  `json:"title"`
	Image    string  `json:"image"`
	Filler   bool    `json:"filler"`
	Provider string  `json:"provider"`
}

// watchResp represents stream resolution payload returned by Re:ANIME /watch endpoint.
type watchResp struct {
	Streams   []watchStream   `json:"streams"`
	Subtitles []watchSubtitle `json:"subtitles"`
}

// watchStream represents a playable media stream option in watchResp.
type watchStream struct {
	Type        string            `json:"type"`
	URL         string            `json:"url"`
	DirectURL   string            `json:"directUrl"`
	Quality     string            `json:"quality"`
	Provider    string            `json:"provider"`
	Server      string            `json:"server"`
	Referer     string            `json:"referer"`
	Headers     map[string]string `json:"headers"`
	HTTPHeaders map[string]string `json:"httpHeaders"`
	MPV         *watchMPV         `json:"mpv"`
	Default     bool              `json:"default"`
}

// watchMPV carries player launch flags when provided by the backend.
type watchMPV struct {
	URL  string   `json:"url"`
	Args []string `json:"args"`
}

// watchSubtitle represents an external subtitle track provided with the stream.
type watchSubtitle struct {
	File     string `json:"file"`
	URL      string `json:"url"`
	Label    string `json:"label"`
	Language string `json:"language"`
	Lang     string `json:"lang"`
	Kind     string `json:"kind"`
	Default  bool   `json:"default"`
	Format   string `json:"format"`
}
