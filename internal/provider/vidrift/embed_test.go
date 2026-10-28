package vidrift

import (
	"testing"

	"kari/internal/config"
	"kari/internal/provider"
)

func TestParseEmbedPage(t *testing.T) {
	body := []byte(embedHTML(
		`{"tmdbId":"1429","type":"tv","season":1,"episode":3,"playbackToken":"tok-anime",`+
			`"anime":["anime","animedub"],"bigbangUrl":"","bigbangSubtitles":[],"evionUrl":"","vexonUrl":"","selfhostUrl":null}`,
		`[{"code":"en","label":"English","lang":"en","url":"/api/subtitles/tv/1429/1/3/en"}]`,
	))

	page, err := parseEmbedPage(body)
	if err != nil {
		t.Fatalf("parseEmbedPage: %v", err)
	}
	if !page.Meta.isAnime() {
		t.Error("expected anime metadata")
	}
	if !page.Meta.hasAnimeLane(providerAnime) || !page.Meta.hasAnimeLane(providerAnimeDub) {
		t.Errorf("anime lanes = %v", page.Meta.animeLanes())
	}
	if got := page.Meta.sourcePath(); got != "tv/1429/1/3" {
		t.Errorf("sourcePath = %q", got)
	}
	if len(page.Subtitles) != 1 || page.Subtitles[0].Lang != "en" {
		t.Errorf("subtitles = %+v", page.Subtitles)
	}
}

func TestParseEmbedPageAnimeForms(t *testing.T) {
	tests := []struct {
		name     string
		anime    string
		want     bool
		wantBoth bool
	}{
		{name: "false", anime: "false", want: false},
		{name: "array", anime: `["anime"]`, want: true},
		{name: "true falls back to both lanes", anime: "true", want: true, wantBoth: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`<script>var embedMeta = {"tmdbId":"1","type":"movie","playbackToken":"t","anime":` +
				tt.anime + `};</script>`)
			page, err := parseEmbedPage(body)
			if err != nil {
				t.Fatalf("parseEmbedPage: %v", err)
			}
			if got := page.Meta.isAnime(); got != tt.want {
				t.Errorf("isAnime = %t, want %t", got, tt.want)
			}
			if tt.wantBoth && !page.Meta.hasAnimeLane(providerAnimeDub) {
				t.Errorf("lanes = %v, want both", page.Meta.animeLanes())
			}
			if page.Subtitles != nil {
				t.Errorf("missing subtitle list should stay nil, got %+v", page.Subtitles)
			}
		})
	}
}

func TestParseEmbedPageRequiresMeta(t *testing.T) {
	if _, err := parseEmbedPage([]byte("<html>nothing here</html>")); err == nil {
		t.Fatal("expected an error when embedMeta is missing")
	}
}

func TestEmbedMetaSourcePath(t *testing.T) {
	season, episode := 2, 5
	meta := embedMeta{TMDBID: "1399", Type: provider.MediaTypeTV, Season: &season, Episode: &episode}
	if got := meta.sourcePath(); got != "tv/1399/2/5" {
		t.Errorf("TV sourcePath = %q", got)
	}

	meta = embedMeta{TMDBID: "27205", Type: provider.MediaTypeMovie}
	if got := meta.sourcePath(); got != "movie/27205" {
		t.Errorf("movie sourcePath = %q", got)
	}

	// A TV-typed payload without coordinates still resolves as a movie
	// route, matching VidRift's movie embeds.
	meta = embedMeta{TMDBID: "27205", Type: provider.MediaTypeTV}
	if got := meta.sourcePath(); got != "movie/27205" {
		t.Errorf("malformed TV sourcePath = %q", got)
	}
}

func TestStreamLanguage(t *testing.T) {
	tests := []struct {
		name     string
		stream   apiStream
		wantCode string
	}{
		{name: "prefixed label", stream: apiStream{Name: "Orion · Hindi"}, wantCode: "hi"},
		{name: "original label", stream: apiStream{Name: "Orion · Original"}, wantCode: ""},
		{name: "bare label", stream: apiStream{Name: "Tamil", Provider: "Titan"}, wantCode: "ta"},
		{name: "multi audio", stream: apiStream{Name: "Multi-audio", Provider: "Titan"}, wantCode: ""},
		{name: "anime lane", stream: apiStream{Provider: "Anime"}, wantCode: "ja"},
		{name: "anime dub lane", stream: apiStream{Provider: "Anime Dub"}, wantCode: "en"},
		{name: "unknown", stream: apiStream{Provider: "Vela"}, wantCode: ""},
		{name: "unknown label", stream: apiStream{Name: "Klingon", Provider: "Lyra"}, wantCode: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := streamLanguage(tt.stream); got != tt.wantCode {
				t.Errorf("streamLanguage(%+v) = %q, want %q", tt.stream, got, tt.wantCode)
			}
		})
	}
}

func TestNormalizeLanguage(t *testing.T) {
	tests := map[string]string{
		"English":              "en",
		"Hindi":                "hi",
		"Tamil":                "ta",
		"Telugu":               "te",
		"Castilian Spanish":    "es",
		"Brazilian Portuguese": "pt",
		"Multi-audio":          "",
		"Original":             "",
		"Klingon":              "",
		"":                     "",
	}
	for raw, want := range tests {
		if got := normalizeLanguage(raw); got != want {
			t.Errorf("normalizeLanguage(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestStreamQuality(t *testing.T) {
	tests := []struct {
		name   string
		stream apiStream
		maxRes int
		want   string
	}{
		{name: "4K", stream: apiStream{Rungs: []streamRung{{Height: 2160}}}, want: "4K"},
		{name: "highest rung", stream: apiStream{Rungs: []streamRung{{Height: 480}, {Height: 1080}}}, want: "1080p"},
		{name: "lane max", stream: apiStream{}, maxRes: 720, want: "720p"},
		{name: "unknown", stream: apiStream{}, want: "Auto"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := streamQuality(tt.stream, tt.maxRes); got != tt.want {
				t.Errorf("streamQuality = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSubtitleOptions(t *testing.T) {
	tracks := []subtitleTrack{
		{Code: "en", Label: "English", Lang: "en", URL: "/api/subtitles/en"},
		{Code: "en-2", Label: "English 2", Lang: "en", URL: "/api/subtitles/en-2"},
		{Code: "hi", Label: "Hindi", Lang: "hi", URL: "https://cdn.example.com/hi.vtt"},
		{Code: "x2", Label: "(Crunchyroll)", Lang: "x2", URL: "/api/subtitles/unknown"},
	}
	options := dedupeSubtitles(subtitleOptions(tracks, "https://embed.vidrift.net"))
	if len(options) != 3 {
		t.Fatalf("options = %+v, want one per language plus unknown", options)
	}
	if options[0].URL != "https://embed.vidrift.net/api/subtitles/en" || options[0].Language != "en" || !options[0].Default {
		t.Errorf("english option = %+v", options[0])
	}
	if options[1].Language != "hi" || options[1].URL != "https://cdn.example.com/hi.vtt" {
		t.Errorf("hindi option = %+v", options[1])
	}
	if options[2].URL != "https://embed.vidrift.net/api/subtitles/unknown" || options[2].Language != "" {
		t.Errorf("unknown option = %+v", options[2])
	}
	for _, option := range options {
		if option.Referer != "https://embed.vidrift.net/" {
			t.Errorf("%s referer = %q", option.URL, option.Referer)
		}
	}
}

func TestDedupeSources(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "https://cdn.example.com/a.m3u8", Language: "en"},
		{URL: "https://cdn.example.com/a.m3u8", Language: "en"},
		{URL: "https://cdn.example.com/a.m3u8", Language: "hi"},
		{URL: ""},
		{URL: "https://cdn.example.com/b.m3u8"},
	}
	got := dedupeSources(sources)
	if len(got) != 3 {
		t.Fatalf("deduped = %+v, want 3", got)
	}
	if got[1].Language != "hi" || got[2].URL != "https://cdn.example.com/b.m3u8" {
		t.Errorf("deduped = %+v", got)
	}
}

func TestIsPlaceholderLanguage(t *testing.T) {
	for _, code := range []string{"x2", "x10"} {
		if !isPlaceholderLanguage(code) {
			t.Errorf("%q should be a placeholder", code)
		}
	}
	for _, code := range []string{"en", "x", "xx2", "x-2"} {
		if isPlaceholderLanguage(code) {
			t.Errorf("%q should not be a placeholder", code)
		}
	}
}

func TestResolveURL(t *testing.T) {
	tests := map[string]string{
		"":                                  "",
		"/api/orion/x/master.m3u8":          "https://embed.vidrift.net/api/orion/x/master.m3u8",
		"https://cdn.example.com/a.m3u8":    "https://cdn.example.com/a.m3u8",
		"//cdn.example.com/scheme-relative": "https://cdn.example.com/scheme-relative",
	}
	for raw, want := range tests {
		if got := resolveURL("https://embed.vidrift.net", raw); got != want {
			t.Errorf("resolveURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestMediaSourcePrefersDirectURL(t *testing.T) {
	stream := apiStream{
		URL:      "/api/orion/x/master.m3u8",
		ProxyURL: "https://proxy.example/lane",
		Type:     "hls",
		Name:     "Orion · Hindi",
		Rungs:    []streamRung{{Height: 1080}},
	}
	source := mediaSource(stream, laneResult{lane: "orion", maxRes: 1080}, "https://embed.vidrift.net", nil, false)

	if source.URL != "https://embed.vidrift.net/api/orion/x/master.m3u8" {
		t.Errorf("url = %q, want the resolved direct URL", source.URL)
	}
	if source.Language != "hi" || source.Quality != "1080p" || source.Type != provider.SourceTypeHLS {
		t.Errorf("source = %+v", source)
	}
	if source.Referer != "https://embed.vidrift.net/" || source.UserAgent != config.DesktopUserAgent || !source.SuppressOrigin {
		t.Errorf("headers = %+v", source)
	}
}
