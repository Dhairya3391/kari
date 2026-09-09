package anilight

import "encoding/json"

// searchResp represents the response payload returned by AniLight's /search endpoint.
type searchResp struct {
	Results []searchResultItem `json:"results"`
}

// searchResultItem represents one anime match in AniLight catalog.
type searchResultItem struct {
	ID          int         `json:"id"`
	Slug        string      `json:"slug"`
	AniListID   json.Number `json:"anilistId"`
	IDMal       json.Number `json:"idMal"`
	Title       searchTitle `json:"title"`
	CoverImage  searchImage `json:"coverImage"`
	BannerImage string      `json:"bannerImage"`
	Episodes    int         `json:"episodes"`
	Format      string      `json:"format"`
	SeasonYear  int         `json:"seasonYear"`
	StartDate   searchDate  `json:"startDate"`
}

// searchTitle holds multi-language title variants.
type searchTitle struct {
	Romaji  string `json:"romaji"`
	English string `json:"english"`
	Native  string `json:"native"`
}

// searchImage carries cover image URLs.
type searchImage struct {
	Large      string `json:"large"`
	ExtraLarge string `json:"extraLarge"`
}

// searchDate holds airing release date components.
type searchDate struct {
	Year int `json:"year"`
}

// episodesResp represents the episode manifest returned by AniLight's /episodes/{anilist_id} endpoint.
type episodesResp struct {
	ID           int           `json:"id"`
	Slug         string        `json:"slug"`
	AniListID    json.Number   `json:"anilistId"`
	HasSub       bool          `json:"hasSub"`
	HasDub       bool          `json:"hasDub"`
	SubProviders []string      `json:"subProviders"`
	DubProviders []string      `json:"dubProviders"`
	Episodes     []episodeItem `json:"episodes"`
}

// episodeItem represents a single episode entry in episodesResp.
type episodeItem struct {
	Number       float64  `json:"number"`
	Title        string   `json:"title"`
	JPTitle      string   `json:"jp_title"`
	Description  string   `json:"description"`
	Image        string   `json:"image"`
	IsFiller     bool     `json:"isFiller"`
	HasSub       bool     `json:"hasSub"`
	HasDub       bool     `json:"hasDub"`
	SubProviders []string `json:"sub_providers"`
	DubProviders []string `json:"dub_providers"`
}

// watchResp represents stream resolution payload returned by AniLight's /watch endpoint.
type watchResp struct {
	Streams       []watchStream   `json:"streams"`
	Subtitles     []watchSubtitle `json:"subtitles"`
	Chapters      []watchChapter  `json:"chapters"`
	Provider      string          `json:"provider"`
	Category      string          `json:"category"`
	EpisodeNumber int             `json:"episodeNumber"`
	AniListID     json.Number     `json:"anilistId"`
	Slug          string          `json:"slug"`
}

// watchStream represents a direct or HLS video stream option.
type watchStream struct {
	URL      string            `json:"url"`
	Quality  string            `json:"quality"`
	Type     string            `json:"type"`
	Provider string            `json:"provider"`
	Headers  map[string]string `json:"headers"`
	MPV      *watchMPV         `json:"mpv"`
	IsDirect bool              `json:"isDirect"`
	Proxy    bool              `json:"proxy"`
}

// watchMPV carries player launch flags when provided by the backend.
type watchMPV struct {
	URL  string   `json:"url"`
	Args []string `json:"args"`
}

// watchSubtitle represents an external subtitle track provided with the stream.
type watchSubtitle struct {
	URL     string `json:"url"`
	Lang    string `json:"lang"`
	Label   string `json:"label"`
	Kind    string `json:"kind"`
	Default bool   `json:"default"`
}

// watchChapter holds chapter markers like opening and ending boundaries.
type watchChapter struct {
	Title string  `json:"title"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}
