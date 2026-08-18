package piratex

type searchResp struct {
	Status  string       `json:"status"`
	Results []searchItem `json:"results"`
}

type searchItem struct {
	Slug   string `json:"slug"`
	Type   string `json:"type"`
	Name   string `json:"name"`
	Year   any    `json:"year"`
	Rating any    `json:"rating"`
	Poster string `json:"poster"`
}

type seriesResp struct {
	Slug         string        `json:"slug"`
	Name         string        `json:"name"`
	TMDBID       string        `json:"tmdb_id"`
	Season       int           `json:"season"`
	Seasons      []season      `json:"seasons"`
	EpisodeCount int           `json:"episode_count"`
	Episodes     []seriesEvent `json:"episodes"`
}

type season struct {
	Number int    `json:"number"`
	Slug   string `json:"slug"`
}

type seriesEvent struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	Season int    `json:"season"`
	URL    string `json:"url"`
}

type watchResp struct {
	ID        string        `json:"id"`
	Slug      string        `json:"slug"`
	Season    int           `json:"season"`
	Episode   int           `json:"episode"`
	Streams   []watchStream `json:"streams"`
	Audio     []audioTrack  `json:"audio"`
	Subtitles []apiSubtitle `json:"subtitles"`
}

type watchStream struct {
	Type     string `json:"type"`
	URL      string `json:"url"`
	Quality  string `json:"quality"`
	Height   int    `json:"height"`
	Server   string `json:"server"`
	Master   bool   `json:"master"`
	Priority int    `json:"priority"`
	Default  bool   `json:"default"`
}

type audioTrack struct {
	Language string `json:"language"`
	Name     string `json:"name"`
	Server   string `json:"server"`
	URL      string `json:"url"`
	Play     string `json:"play"`
}

type apiSubtitle struct {
	Language string `json:"language"`
	Name     string `json:"name"`
	URL      string `json:"url"`
}
