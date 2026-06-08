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
	URL      string `json:"url"`
	Type     string `json:"type"`
	Quality  string `json:"quality"`
	Referer  string `json:"referer"`
	Server   string `json:"server"`
	Priority int    `json:"priority"`
	Default  bool   `json:"default"`
}

type linkSubtitle struct {
	File     string `json:"file"`
	Label    string `json:"label"`
	Kind     string `json:"kind"`
	Default  bool   `json:"default"`
	Language string `json:"language"`
	Format   string `json:"format"`
}
