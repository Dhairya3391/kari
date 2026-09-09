package anikoto

import "encoding/json"

// searchResp represents the response body returned by Anikoto's /search endpoint.
type searchResp struct {
	Results []searchResultItem `json:"results"`
}

// searchResultItem represents one anime search match. ID uses json.Number
// to tolerate both numeric and string IDs from upstream AniList/Anikoto proxies.
type searchResultItem struct {
	ID     json.Number `json:"id"`
	Name   string      `json:"name"`
	Format string      `json:"format"`
	Year   int         `json:"year"`
}

// episodeResp represents one episode item returned by Anikoto's /episodes endpoint.
type episodeResp struct {
	ID          string  `json:"id"`
	Number      float64 `json:"number"`
	Category    string  `json:"category"`
	RawCategory string  `json:"rawCategory"`
	Title       string  `json:"title"`
	Image       string  `json:"image"`
	AirDate     string  `json:"airDate"`
	Description string  `json:"description"`
	Filler      bool    `json:"filler"`
	FillerType  string  `json:"fillerType"`
	Provider    string  `json:"provider"`
	MAL         string  `json:"mal"`
}

// linkResp represents the stream resolution payload returned by Anikoto's /link endpoint.
type linkResp struct {
	Streams   []linkStream   `json:"streams"`
	Subtitles []linkSubtitle `json:"subtitles"`
	Intro     []float64      `json:"intro"`
	Outro     []float64      `json:"outro"`
}

// linkStream represents a playable media stream option in linkResp.
type linkStream struct {
	URL         string            `json:"url"`
	Type        string            `json:"type"`
	Quality     string            `json:"quality"`
	Referer     string            `json:"referer"`
	Server      string            `json:"server"`
	Provider    string            `json:"provider"`
	Priority    int               `json:"priority"`
	Verified    bool              `json:"verified"`
	Default     bool              `json:"default"`
	Headers     map[string]string `json:"headers"`
	HTTPHeaders map[string]string `json:"httpHeaders"`
	MPV         *linkMPV          `json:"mpv"`
}

// linkMPV carries player launch flags when provided by the backend.
type linkMPV struct {
	URL  string   `json:"url"`
	Args []string `json:"args"`
}

// linkSubtitle represents an external subtitle track provided with the stream.
type linkSubtitle struct {
	File     string `json:"file"`
	Label    string `json:"label"`
	Kind     string `json:"kind"`
	Default  bool   `json:"default"`
	Language string `json:"language"`
	Format   string `json:"format"`
}
