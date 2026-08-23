package miruro

type searchResp struct {
	Results []struct {
		ID     int    `json:"id"`
		Name   string `json:"name"`
		Format string `json:"format"`
		Year   int    `json:"year"`
	} `json:"results"`
}

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
}

type linkResp struct {
	Streams   []linkStream   `json:"streams"`
	Subtitles []linkSubtitle `json:"subtitles"`
}
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
type linkMPV struct {
	URL  string   `json:"url"`
	Args []string `json:"args"`
}

type linkSubtitle struct {
	File     string `json:"file"`
	Label    string `json:"label"`
	Kind     string `json:"kind"`
	Default  bool   `json:"default"`
	Language string `json:"language"`
	Format   string `json:"format"`
}
