package jellyfin

type searchHintsResult struct {
	SearchHints []searchHint `json:"SearchHints"`
}

type searchHint struct {
	ItemID         string `json:"Id"`
	Name           string `json:"Name"`
	Type           string `json:"Type"`
	ProductionYear int    `json:"ProductionYear"`
	Series         string `json:"Series"`
	SeriesID       string `json:"SeriesId"`
	RunTimeTicks   int64  `json:"RunTimeTicks"`
}

type itemsResult struct {
	Items []item `json:"Items"`
}

type item struct {
	ID             string `json:"Id"`
	Name           string `json:"Name"`
	SeasonNumber   int    `json:"ParentIndexNumber"`
	EpisodeNumber  int    `json:"IndexNumber"`
	SeriesID       string `json:"SeriesId"`
	SeriesName     string `json:"SeriesName"`
	RunTimeTicks   int64  `json:"RunTimeTicks"`
	Type           string `json:"Type"`
	ProductionYear int    `json:"ProductionYear"`
}
