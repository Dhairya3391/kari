package pengu

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kari/internal/provider"
)

func TestBuildConfigSegment(t *testing.T) {
	token := "test_auth_token_12345"
	seg, err := buildConfigSegment(token)
	if err != nil {
		t.Fatalf("buildConfigSegment: %v", err)
	}

	if !strings.HasPrefix(seg, "z") {
		t.Fatalf("segment must start with 'z', got %q", seg)
	}

	rawB64 := strings.TrimPrefix(seg, "z")
	data, err := base64.RawURLEncoding.DecodeString(rawB64)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}

	fr := flate.NewReader(bytes.NewReader(data))
	defer fr.Close()
	decompressed, err := io.ReadAll(fr)
	if err != nil {
		t.Fatalf("flate decompress: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(decompressed, &parsed); err != nil {
		t.Fatalf("unmarshal decompressed json: %v", err)
	}

	if parsed["auth_token"] != token {
		t.Errorf("auth_token = %v, want %v", parsed["auth_token"], token)
	}
	if parsed["source_4khdhub"] != "on" {
		t.Errorf("source_4khdhub = %v, want on", parsed["source_4khdhub"])
	}
	// Cinejoy and live-sports are backends enabled in Kari.
	if parsed["source_cinejoy"] != "on" {
		t.Errorf("source_cinejoy = %v, want on", parsed["source_cinejoy"])
	}
	if parsed["source_live-sports"] != "on" {
		t.Errorf("source_live-sports = %v, want on", parsed["source_live-sports"])
	}
	// 111477 and experimental are ignored/omitted.
	if parsed["source_111477"] == "on" {
		t.Errorf("source_111477 should not be enabled")
	}
	if parsed["source_experimental"] == "on" {
		t.Errorf("source_experimental should not be enabled")
	}
	// These backends play via Kari's header handling (verified with live
	// mpv probes), so they stay enabled.
	for _, key := range []string{"source_moviebox", "source_2peckle", "source_4khdhub", "source_vegamovies", "source_vaplayer", "source_vidfast", "source_cinefreak", "source_cinejoy", "source_kisskh", "source_hdhub4u", "source_moviesdrives", "source_vidy", "source_toonworld4all"} {
		if parsed[key] != "on" {
			t.Errorf("%s = %v, want on", key, parsed[key])
		}
	}
	for _, key := range []string{"source_vidlink", "source_arctic", "source_reanime", "source_miruro", "source_anikoto", "source_anipm"} {
		if parsed[key] != "off" {
			t.Errorf("%s = %v, want off", key, parsed[key])
		}
	}
	for _, key := range []string{"source_hdghartv"} {
		if _, ok := parsed[key]; ok {
			t.Errorf("%s should not be present in Pengu config", key)
		}
	}
	if parsed["res_1080"] != "on" {
		t.Errorf("res_1080 = %v, want on", parsed["res_1080"])
	}
	// Movy.sx must NOT be requested from Pengu (Kari handles it independently).
	if parsed["source_movysx"] == "on" {
		t.Errorf("source_movysx should not be enabled in Pengu config")
	}
}
func TestBuildAnimeConfigSegment(t *testing.T) {
	seg, err := buildAnimeConfigSegment("tok123")
	if err != nil {
		t.Fatalf("buildAnimeConfigSegment: %v", err)
	}
	rawB64 := strings.TrimPrefix(seg, "z")
	data, err := base64.RawURLEncoding.DecodeString(rawB64)
	if err != nil {
		t.Fatalf("decode b64: %v", err)
	}
	zr := flate.NewReader(bytes.NewReader(data))
	raw, err := io.ReadAll(zr)
	_ = zr.Close()
	if err != nil {
		t.Fatalf("read deflated: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"source_reanime", "source_2peckle"} {
		if parsed[key] != "on" {
			t.Errorf("%s = %v, want on", key, parsed[key])
		}
	}
	for _, key := range []string{"source_anipm", "source_anikoto", "source_miruro", "source_kisskh", "source_moviebox", "source_4khdhub", "source_vegamovies", "source_moviesdrives", "source_vaplayer", "source_vidfast", "source_cinefreak", "source_cinejoy", "source_hdhub4u", "source_vidy", "source_toonworld4all", "source_arctic", "source_live-sports"} {
		if parsed[key] != "off" {
			t.Errorf("%s = %v, want off", key, parsed[key])
		}
	}
}

func TestParseStreamItem(t *testing.T) {
	item := penguStreamItem{
		Name: "🐧 PenguPlay 4K • 4KHDHub",
		Description: `🍿 Inception
🎞️ 4K • HLS
🛰️ Source: 4KHDHub
💾 12.4 GB
🎧 Audio: Hindi, English
📝 Subtitles: English`,
		URL: "https://example.com/stream.m3u8",
		BehaviorHints: penguBehaviorHints{
			Headers: map[string]string{
				"Referer":    "https://example.com/ref",
				"User-Agent": "CustomUA/1.0",
				"Cookie":     "sess=xyz",
			},
		},
		Subtitles: []penguSubtitle{
			{ID: "1", URL: "https://example.com/en.srt", Lang: "eng"},
		},
	}

	src := parseStreamItem(item)

	if src.Quality != "4K [4KHDHub] (Hindi)" {
		t.Errorf("Quality = %q, want %q", src.Quality, "4K [4KHDHub] (Hindi)")
	}
	if src.Type != provider.SourceTypeHLS {
		t.Errorf("Type = %q, want %q", src.Type, provider.SourceTypeHLS)
	}
	if src.Referer != "https://example.com/ref" {
		t.Errorf("Referer = %q, want https://example.com/ref", src.Referer)
	}
	if src.UserAgent != "CustomUA/1.0" {
		t.Errorf("UserAgent = %q, want CustomUA/1.0", src.UserAgent)
	}
	if src.CookieHeader != "sess=xyz" {
		t.Errorf("CookieHeader = %q, want sess=xyz", src.CookieHeader)
	}
	if src.Language != "hi" {
		t.Errorf("Language = %q, want hi", src.Language)
	}
	if len(src.Subtitles) != 1 || src.Subtitles[0].Language != "en" {
		t.Errorf("Subtitles = %+v, want 1 english track", src.Subtitles)
	}
}

func TestParseStreamItemNewBackends(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend string
	}{
		{"🐧 PenguPlay ❄️ 4K • Cinejoy · Lisbon", "Cinejoy"},
		{"🐧 PenguPlay 🧊 1080p • MovieBox", "MovieBox"},
		{"🐧 PenguPlay 🧊 1080p • Anikoto", "Anikoto"},
		{"🐧 PenguPlay 🧊 1080p • 2Peckle", "2Peckle"},
		{"🐧 PenguPlay 🧊 1080p • Miruro · Animegg", "Miruro"},
		{"🐧 PenguPlay 🧊 1080p • Arctic", "Arctic"},
		{"🐧 PenguPlay 🧊 1080p • VixSrc", "VixSrc"},
		{"🐧 PenguPlay ❄️ 4K • Vidy · Miami", "Vidy"},
		{"🐧 PenguPlay 🧊 1080p • ToonWorld4All", "ToonWorld4All"},
		{"🐧 PenguPlay 🧊 1080p • Reanime · HD-1 · Sub", "Reanime"},
		{"🐧 PenguPlay 🧊 1080p • Ani.pm · HD-1 · Sub", "Ani.pm"},
		{"🐧 PenguPlay 🧊 1080p • Anipm · HD-1 · Sub", "Ani.pm"},
		{"🐧 PenguPlay 🧊 1080p • MoviesDrives · PixelDrain", "MoviesDrives"},
		{"🐧 PenguPlay 🐧 360p • PlayFy · Fallback", "PlayFy"},
	} {
		src := parseStreamItem(penguStreamItem{Name: tc.name, URL: "https://example.com/stream.m3u8"})
		want := "1080p [" + tc.backend + "]"
		if (tc.backend == "Cinejoy" || tc.backend == "Vidy") && strings.Contains(tc.name, "4K") {
			want = "4K [" + tc.backend + "]"
		} else if tc.backend == "PlayFy" {
			want = "360p [" + tc.backend + "]"
		}
		if src.Quality != want {
			t.Errorf("Quality for %q = %q, want %q", tc.name, src.Quality, want)
		}
	}
}

// Ani.pm serves each title in soft and hard subtitle variants. The soft
// rows must not mistake the "Subtitles: English, French, …" list for dub
// audio, hard rows must keep distinct quality labels (same filename/size
// would otherwise dedupe them away), and SubType must mark burned-in vs
// toggleable tracks for anime ranking.
func TestParseStreamItemAniPmHardSoft(t *testing.T) {
	mk := func(name, desc string, subs []penguSubtitle) penguStreamItem {
		return penguStreamItem{
			Name:        name,
			Description: desc,
			URL:         "https://pengu.uk/hls/anipm/playlist/abc",
			BehaviorHints: penguBehaviorHints{
				Filename: "1080pHLS1080phls.pad-Ani.pm",
			},
			Subtitles: subs,
		}
	}
	softSubs := []penguSubtitle{{ID: "anipm-0", URL: "https://pengu.uk/subs/anipm/abc", Lang: "English"}}
	for _, tc := range []struct {
		name        string
		desc        string
		subs        []penguSubtitle
		wantQuality string
		wantLang    string
		wantSubType string
	}{
		{
			"🐧 PenguPlay 🧊 1080p • Ani.pm · Sub",
			"📡 Naruto: Shippuden - Episode 1 • S01E01\n🎞️ 1080p • HLS\n🛰️ Source: Ani.pm · Sub\n📝 Subtitles: English, French, Arabic",
			softSubs, "1080p [Ani.pm]", "", provider.SubTypeSoft,
		},
		{
			"🐧 PenguPlay 🧊 1080p • Ani.pm · Dub",
			"📡 Naruto: Shippuden - Episode 1 • S01E01\n🎞️ 1080p • HLS\n🛰️ Source: Ani.pm · Dub\n🎧 Audio: English\n📝 Subtitles: English CC (Dub), English",
			softSubs, "1080p [Ani.pm] (Dub)", "en", provider.SubTypeSoft,
		},
		{
			"🐧 PenguPlay 🧊 1080p • Ani.pm · Hard Sub",
			"📡 Naruto: Shippuden - Episode 1 • S01E01\n🎞️ 1080p • HLS\n🛰️ Source: Ani.pm · Hard Sub\n🙊 No included subtitles",
			nil, "1080p [Ani.pm] (Hard Sub)", "", provider.SubTypeHard,
		},
		{
			"🐧 PenguPlay 🧊 1080p • Ani.pm · Hard Dub",
			"📡 Naruto: Shippuden - Episode 1 • S01E01\n🎞️ 1080p • HLS\n🛰️ Source: Ani.pm · Hard Dub\n🎧 Audio: English\n🙊 No included subtitles",
			nil, "1080p [Ani.pm] (Hard Dub)", "en", provider.SubTypeHard,
		},
	} {
		src := parseStreamItem(mk(tc.name, tc.desc, tc.subs))
		if src.Quality != tc.wantQuality {
			t.Errorf("Quality(%q) = %q, want %q", tc.name, src.Quality, tc.wantQuality)
		}
		if src.Language != tc.wantLang {
			t.Errorf("Language(%q) = %q, want %q", tc.name, src.Language, tc.wantLang)
		}
		if src.SubType != tc.wantSubType {
			t.Errorf("SubType(%q) = %q, want %q", tc.name, src.SubType, tc.wantSubType)
		}
		if src.Type != provider.SourceTypeHLS {
			t.Errorf("Type(%q) = %q, want hls", tc.name, src.Type)
		}
	}
}

// Pengu joins anime mode at P3: the direct scrapers win same-height
// ties while Pengu's aggregated backends (Ani.pm, Reanime, …) add
// fallback depth.
func TestModesIncludesAnime(t *testing.T) {
	c := &Client{}
	var found *provider.Mode
	for _, m := range c.Modes() {
		if m.Name == provider.ModeAnime {
			m := m
			found = &m
		}
	}
	if found == nil {
		t.Fatal("expected ModeAnime in Modes()")
	}
	if found.Priority != 3 {
		t.Errorf("anime priority = %d, want 3", found.Priority)
	}
}

// Anime search uses the shared AniList index with bare numeric IDs so
// merged results dedupe against the direct providers' rows.
func TestSearchAnimeUsesAnilist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"Page":{"media":[
			{"id":21,"title":{"romaji":"One Piece","english":"ONE PIECE","userPreferred":"ONE PIECE","native":"ワンピース"},"seasonYear":1999,"format":"TV"}
		]}}}`)
	}))
	defer server.Close()

	c := &Client{httpClient: server.Client(), anilistBase: server.URL}
	results, err := c.Search(context.Background(), "one piece", provider.ModeAnime)
	if err != nil {
		t.Fatalf("Search(ModeAnime): %v", err)
	}
	if len(results) != 1 || results[0].ID != "21" || results[0].Type != provider.ModeAnime {
		t.Fatalf("unexpected results: %+v", results)
	}
	if _, err := c.Search(context.Background(), "   ", provider.ModeAnime); !errors.Is(err, provider.ErrNoResults) {
		t.Errorf("empty anime query = %v, want ErrNoResults", err)
	}
}

// Anime episodes synthesize from AniList metadata with anilist:-prefixed
// IDs so resolution addresses Pengu's anime catalog directly.
func TestFetchAnimeEpisodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"Media":{"id":21,"idMal":21,"title":{"romaji":"One Piece","english":"ONE PIECE","userPreferred":"ONE PIECE","native":"ワンピース"},"format":"TV","status":"RELEASING","seasonYear":1999,"episodes":3,"synonyms":[],"nextAiringEpisode":null}}}`)
	}))
	defer server.Close()

	c := &Client{httpClient: server.Client(), anilistBase: server.URL}
	eps, err := c.FetchEpisodes(context.Background(), provider.SearchResult{Title: "ONE PIECE", ID: "21", Type: provider.ModeAnime})
	if err != nil {
		t.Fatalf("FetchEpisodes(anime): %v", err)
	}
	if len(eps) != 3 {
		t.Fatalf("episodes = %d, want 3", len(eps))
	}
	if eps[0].ID != "anilist:21:1" || eps[2].Episode != 3 || eps[2].Season != 1 {
		t.Errorf("unexpected episodes: %+v", eps)
	}
	for _, ep := range eps {
		if ep.Audio != "" {
			t.Errorf("episode audio = %q, want empty (match-all)", ep.Audio)
		}
	}
}

// Anime movies resolve to a single anilist:{id} handle for direct resolution.
func TestFetchAnimeEpisodesMovie(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"Media":{"id":23273,"idMal":23273,"title":{"romaji":"Kimi no Na wa.","english":"Your Name.","userPreferred":"Your Name.","native":"君の名は。"},"format":"MOVIE","status":"FINISHED","seasonYear":2016,"episodes":1,"synonyms":[],"nextAiringEpisode":null}}}`)
	}))
	defer server.Close()

	c := &Client{httpClient: server.Client(), anilistBase: server.URL}
	eps, err := c.FetchEpisodes(context.Background(), provider.SearchResult{Title: "Your Name.", ID: "23273", Type: provider.ModeAnime, MediaType: provider.MediaTypeMovie})
	if err != nil {
		t.Fatalf("FetchEpisodes(anime movie): %v", err)
	}
	if len(eps) != 1 || eps[0].ID != "anilist:23273" {
		t.Fatalf("unexpected episodes: %+v", eps)
	}
}

// Cross-provider anime resolution (bare AniList handle + sub/dub episode
// from a direct provider) must hit the anime catalog — never another
// title's TMDB ID.
func TestResolveAnimeUsesAnilistID(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(penguResponse{
			Streams: []penguStreamItem{
				{
					Name: "🐧 PenguPlay 🧊 1080p • Reanime · Sub",
					URL:  "https://cdn.example.com/a.m3u8",
					BehaviorHints: penguBehaviorHints{
						Filename: "1080pHLS1080phls.pad-Reanime",
					},
					Subtitles: []penguSubtitle{{ID: "reanime-0", URL: "https://pengu.uk/subs/x", Lang: "English"}},
				},
				{
					Name: "🐧 PenguPlay 🧊 1080p • Reanime · Hard Sub",
					URL:  "https://cdn.example.com/b.m3u8",
					BehaviorHints: penguBehaviorHints{
						Filename: "1080pHLS1080phls.pad-Reanime",
					},
				},
			},
		})
	}))
	defer server.Close()

	origBase := penguAPIBase
	defer func() { penguAPIBase = origBase }()
	penguAPIBase = server.URL
	c := &Client{httpClient: server.Client(), configSegment: "ztest", segAnime: "zanime"}

	sources, err := c.ResolveSource(context.Background(), "21", provider.Episode{
		Season: 1, Episode: 1180, Audio: "sub", ID: "watch/anikoto/21/sub/1180",
	})
	if err != nil {
		t.Fatalf("ResolveSource(anime): %v", err)
	}
	if !strings.Contains(gotPath, "/stream/series/anilist:21:1180.json") {
		t.Errorf("request path = %q, want anilist:21:1180 series stream", gotPath)
	}
	if !strings.Contains(gotPath, "zanime") {
		t.Errorf("request path = %q, want anime config segment", gotPath)
	}
	// Soft and hard variants stay distinct rows with their subtitle kinds.
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want 2: %+v", len(sources), sources)
	}
	if sources[0].SubType != provider.SubTypeSoft || sources[1].SubType != provider.SubTypeHard {
		t.Errorf("subtypes = %q %q, want soft hard", sources[0].SubType, sources[1].SubType)
	}
}

// Prefixed handles and movie handles resolve to the matching anime catalog IDs.
func TestAnimeStremioIDShapes(t *testing.T) {
	for _, tc := range []struct {
		mediaID string
		episode provider.Episode
		want    string
		ok      bool
	}{
		{"anilist:21", provider.Episode{Season: 1, Episode: 1180}, "anilist:21:1180", true},
		{"21", provider.Episode{Season: 1, Episode: 1180, Audio: "sub", ID: "watch/anikoto/21/sub/1180"}, "anilist:21:1180", true},
		{"anilist:21", provider.Episode{}, "anilist:21", true},
		{"tmdb:37854:1:1180", provider.Episode{Season: 1, Episode: 1180, Audio: "sub"}, "", false},
		{"", provider.Episode{Season: 1, Episode: 1180, Audio: "sub"}, "", false},
	} {
		got, ok := animeStremioID(tc.mediaID, tc.episode)
		if got != tc.want || ok != tc.ok {
			t.Errorf("animeStremioID(%q) = (%q, %v), want (%q, %v)", tc.mediaID, got, ok, tc.want, tc.ok)
		}
	}
}

func TestIsHousekeepingEntry(t *testing.T) {
	promo := penguStreamItem{
		Name:        "✨ | support the project!",
		Description: "pengu.uk/donate (donating hides this message)",
		ExternalURL: "https://pengu.uk/donate",
	}
	if !isHousekeepingEntry(promo) {
		t.Errorf("isHousekeepingEntry want true for donation banner")
	}

	real := penguStreamItem{
		Name: "🐧 PenguPlay 🧊 1080p • 4KHDHub",
		URL:  "https://pengu.uk/direct/external/stream",
	}
	if isHousekeepingEntry(real) {
		t.Errorf("isHousekeepingEntry want false for real stream")
	}
}

func TestResolveSourceKeepsDistinctEncodes(t *testing.T) {
	origBase := penguAPIBase
	defer func() { penguAPIBase = origBase }()

	payload := `{"streams":[
		{"name":"✨ | support the project!","description":"pengu.uk/donate","externalUrl":"https://pengu.uk/donate"},
		{"name":"🐧 PenguPlay 🧊 1080p • 4KHDHub · PixelDrain","url":"https://cdn.example.com/a","behaviorHints":{"filename":"encode-a.mkv","videoSize":6764573491}},
		{"name":"🐧 PenguPlay 🧊 1080p • 4KHDHub · PixelDrain","url":"https://cdn.example.com/b","behaviorHints":{"filename":"encode-b.mkv","videoSize":3972844749}},
		{"name":"🐧 PenguPlay 🧊 1080p • 4KHDHub · PixelDrain","url":"https://cdn.example.com/b","behaviorHints":{"filename":"encode-b.mkv","videoSize":3972844749}},
		{"name":"🐧 PenguPlay ❄️ 4K • Cinejoy · Lisbon","url":"https://cdn.example.com/c","behaviorHints":{"filename":"cinejoy-4k","videoSize":16320000000}},
		{"name":"🐧 PenguPlay 1080p • Movy.sx","description":"Source: Movy.sx","url":"https://cdn.example.com/msx"}
	]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	penguAPIBase = server.URL
	c := &Client{httpClient: server.Client(), configSegment: "ztest"}

	sources, err := c.ResolveSource(context.Background(), "603", provider.Episode{})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	// promo skipped, exact URL duplicate collapsed, Movy.sx excluded:
	// 2 distinct 4KHDHub encodes + 1 Cinejoy.
	if len(sources) != 3 {
		t.Fatalf("sources = %d, want 3: %+v", len(sources), sources)
	}
	if sources[0].Quality != "1080p [4KHDHub]" {
		t.Errorf("sources[0].Quality = %q, want %q", sources[0].Quality, "1080p [4KHDHub]")
	}
	if sources[2].Quality != "4K [Cinejoy]" {
		t.Errorf("sources[2].Quality = %q, want %q", sources[2].Quality, "4K [Cinejoy]")
	}
}

func TestResolveAnimeMALPriority(t *testing.T) {
	origBase := penguAPIBase
	defer func() { penguAPIBase = origBase }()

	var queriedPath string
	penguServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queriedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"streams":[
			{"name":"🐧 PenguPlay 🧊 1080p • 2Peckle · Sub","url":"https://cdn.example.com/2peckle.m3u8"},
			{"name":"🐧 PenguPlay 🧊 1080p • Reanime · HD-1 · Sub","url":"https://cdn.example.com/reanime.m3u8"}
		]}`))
	}))
	defer penguServer.Close()
	penguAPIBase = penguServer.URL

	anilistServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"Media":{"id":21,"idMal":21,"format":"TV","status":"RELEASING"}}}`))
	}))
	defer anilistServer.Close()

	c := &Client{
		httpClient:    penguServer.Client(),
		anilistBase:   anilistServer.URL,
		configSegment: "cfg",
		segAnime:      "segAnime",
		malCache:      make(map[int]int),
	}

	sources, err := c.ResolveSource(context.Background(), "21", provider.Episode{Episode: 1180, Audio: "sub"})
	if err != nil {
		t.Fatalf("ResolveSource failed: %v", err)
	}
	if !strings.Contains(queriedPath, "mal:21:1180") {
		t.Errorf("queriedPath = %q, want path containing mal:21:1180", queriedPath)
	}
	if len(sources) != 2 {
		t.Fatalf("got %d sources, want 2", len(sources))
	}
}

func TestMapAudioLanguageUsesFirstListedLanguage(t *testing.T) {
	if got := mapAudioLanguage("Hindi, English"); got != "hi" {
		t.Errorf("mapAudioLanguage(Hindi, English) = %q, want hi", got)
	}
}

func TestRedactRequestError(t *testing.T) {
	segment := "zcustom_config_segment"
	err := redactRequestError(errors.New("Get https://pengu.uk/"+segment+"/stream/movie/tmdb:1.json: timeout"), segment)
	if strings.Contains(err.Error(), segment) {
		t.Fatalf("redacted error contains configuration segment: %q", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Errorf("redacted error = %q, want redaction marker", err)
	}
}

func TestParseStreamItemFilenameLanguage(t *testing.T) {
	item := penguStreamItem{
		Name: "🐧 PenguPlay 1080p • HDHub4u",
		URL:  "https://example.com/stream.m3u8",
		BehaviorHints: penguBehaviorHints{
			Filename: "The.Boys.S01E01.1080p.Hindi.DD5.1-English.5.1.HEVC.x265-HDHub4u.mkv",
		},
	}
	src := parseStreamItem(item)
	if src.Language != "hi" {
		t.Errorf("Language = %q, want hi", src.Language)
	}
	if src.Quality != "1080p [HDHub4u] (Hindi)" {
		t.Errorf("Quality = %q, want %q", src.Quality, "1080p [HDHub4u] (Hindi)")
	}
}

// A direct matroska file must type as progressive (mp4), never HLS:
// players and downloaders branch on Type, and an HLS label on an MKV
// body breaks manifest handling.
func TestParseStreamItemMatroskaTypesProgressive(t *testing.T) {
	for _, u := range []string{
		"https://cdn.example.com/ep.mkv",
		"https://cdn.example.com/ep.mkv?token=abc",
		"https://cdn.example.com/EP.WEBM",
	} {
		src := parseStreamItem(penguStreamItem{Name: "x 1080p", URL: u})
		if src.Type != provider.SourceTypeMP4 {
			t.Errorf("Type(%q) = %q, want mp4", u, src.Type)
		}
	}
	for _, u := range []string{
		"https://cdn.example.com/master.m3u8",
		"https://cdn.example.com/manifest?token=abc",
	} {
		src := parseStreamItem(penguStreamItem{Name: "x 1080p", URL: u})
		if src.Type != provider.SourceTypeHLS {
			t.Errorf("Type(%q) = %q, want hls", u, src.Type)
		}
	}
}

func TestIsExcludedStream(t *testing.T) {
	movyStream := penguStreamItem{
		Name:        "🐧 PenguPlay 1080p • Movy.sx",
		Description: "Source: Movy.sx",
		URL:         "https://pengu.uk/hls/movysx/stream.m3u8",
	}
	if !isExcludedStream(movyStream) {
		t.Errorf("isExcludedStream want true for Movy.sx stream")
	}

	twoPeckleStream := penguStreamItem{
		Name:        "🐧 PenguPlay 1080p • 2Peckle",
		Description: "Source: 2Peckle",
		URL:         "https://pengu.uk/direct/external/stream.mp4",
	}
	if isExcludedStream(twoPeckleStream) {
		t.Errorf("isExcludedStream want false for 2Peckle stream")
	}

	allowedStream := penguStreamItem{
		Name:        "🐧 PenguPlay 1080p • 4KHDHub",
		Description: "Source: 4KHDHub",
		URL:         "https://pengu.uk/direct/external/stream.mp4",
	}
	if isExcludedStream(allowedStream) {
		t.Errorf("isExcludedStream want false for 4KHDHub stream")
	}
}

func TestIsAuthPrompt(t *testing.T) {
	authStreams := []penguStreamItem{
		{
			Name:        "PenguPlay",
			Title:       "You must sign in",
			Description: "Your PenguPlay authentication is missing, invalid, or revoked.",
			URL:         "https://pengu.uk/signin.mp4",
		},
	}

	if !isAuthPrompt(authStreams) {
		t.Errorf("isAuthPrompt want true for signin prompt")
	}

	realStreams := []penguStreamItem{
		{
			Name: "Stream 1",
			URL:  "https://cdn.example.com/video.mp4",
		},
	}
	if isAuthPrompt(realStreams) {
		t.Errorf("isAuthPrompt want false for real stream")
	}
}

func TestAudioLanguagesCoverage(t *testing.T) {
	c := &Client{}
	langs := c.AudioLanguages()
	if len(langs) == 0 {
		t.Fatal("expected non-empty audio languages")
	}

	for _, l := range langs {
		if l.Code == "" || l.Display == "" {
			t.Errorf("invalid audio language: %+v", l)
		}
	}
}

func TestFetchPenguStreamsRateLimit(t *testing.T) {
	origBase := penguAPIBase
	defer func() { penguAPIBase = origBase }()

	t.Run("429 status code returns ErrRateLimited", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate_limited"}`))
		}))
		defer server.Close()

		penguAPIBase = server.URL
		c := &Client{
			httpClient:    server.Client(),
			configSegment: "ztest",
		}

		_, err := c.fetchPenguStreams(context.Background(), provider.MediaTypeMovie, "tmdb:550", false)
		if !errors.Is(err, provider.ErrRateLimited) {
			t.Fatalf("fetchPenguStreams want ErrRateLimited, got %v", err)
		}
	})

	t.Run("200 status code with rate_limited error returns ErrRateLimited", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"streams":[],"error":"rate_limited"}`))
		}))
		defer server.Close()

		penguAPIBase = server.URL
		c := &Client{
			httpClient:    server.Client(),
			configSegment: "ztest",
		}

		_, err := c.fetchPenguStreams(context.Background(), provider.MediaTypeMovie, "tmdb:550", false)
		if !errors.Is(err, provider.ErrRateLimited) {
			t.Fatalf("fetchPenguStreams want ErrRateLimited, got %v", err)
		}
	})
}

func TestLiveFeaturesAndModes(t *testing.T) {
	c := &Client{}
	modes := c.Modes()
	var hasLive bool
	for _, m := range modes {
		if m.Name == provider.ModeLive {
			hasLive = true
			break
		}
	}
	if !hasLive {
		t.Fatal("expected ModeLive in Modes()")
	}

	f := c.Features(provider.ModeLive)
	if !f.AllowEmptyQuery {
		t.Errorf("expected AllowEmptyQuery=true for ModeLive")
	}
	if !f.NoCachedSearches {
		t.Errorf("expected NoCachedSearches=true for ModeLive")
	}
	if f.SearchPlaceholder == "" {
		t.Errorf("expected non-empty SearchPlaceholder for ModeLive")
	}
}

func TestSearchLiveCatalog(t *testing.T) {
	origBase := penguAPIBase
	defer func() { penguAPIBase = origBase }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "pp-live-now") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(penguCatalogResponse{
				Metas: []penguCatalogMeta{
					{
						ID:          "pp-live:daddylive~123",
						Name:        "🔴 LIVE: Match 1",
						ReleaseInfo: "2026-09-20 10:00",
						Poster:      "https://example.com/p1.jpg",
						Description: "Match 1 live description",
						Genres:      []string{"Cricket"},
					},
				},
			})
			return
		}
		if strings.Contains(r.URL.Path, "pp-live-channels") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(penguCatalogResponse{
				Metas: []penguCatalogMeta{
					{
						ID:          "pp-live:binged~espn",
						Name:        "📺 ESPN",
						ReleaseInfo: "24/7",
						Poster:      "https://example.com/espn.jpg",
						Description: "ESPN 24/7 Channel",
						Genres:      []string{"Sports"},
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(penguCatalogResponse{Metas: []penguCatalogMeta{}})
	}))
	defer server.Close()

	penguAPIBase = server.URL
	c := &Client{
		httpClient:    server.Client(),
		configSegment: "ztest",
	}

	results, err := c.Search(context.Background(), "", provider.ModeLive)
	if err != nil {
		t.Fatalf("Search(ModeLive) error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].ID != "pp-live:daddylive~123" || results[0].Title != "LIVE: Match 1" || results[0].Type != provider.ModeLive || results[0].MediaType != provider.MediaTypeLive {
		t.Errorf("unexpected results[0]: %+v", results[0])
	}
	if results[1].ID != "pp-live:binged~espn" || results[1].Title != "ESPN" {
		t.Errorf("unexpected results[1]: %+v", results[1])
	}
}

// One stalled catalog (the real channel list is ~1.5 MB) must not sink the
// fast one: an empty live browse still surfaces live-now results.
func TestSearchLiveReturnsPartialWhenCatalogStalls(t *testing.T) {
	origBase := penguAPIBase
	defer func() { penguAPIBase = origBase }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "pp-live-now") {
			_ = json.NewEncoder(w).Encode(penguCatalogResponse{
				Metas: []penguCatalogMeta{{ID: "pp-live:daddylive~1", Name: "🔴 LIVE: Match", ReleaseInfo: "2026-09-20 10:00"}},
			})
			return
		}
		// Stall the channel catalog until the caller's context expires.
		<-r.Context().Done()
	}))
	defer server.Close()

	penguAPIBase = server.URL
	c := &Client{httpClient: server.Client(), configSegment: "ztest"}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	results, err := c.Search(ctx, "", provider.ModeLive)
	if err != nil {
		t.Fatalf("Search(ModeLive) error: %v", err)
	}
	if len(results) != 1 || results[0].ID != "pp-live:daddylive~1" {
		t.Fatalf("got %+v, want the live-now result despite the stalled channel catalog", results)
	}
}

// The upstream releaseInfo carries a GMT offset suffix; it must parse
// into local time instead of dropping to zero (which hid every timed
// event from the schedule).
func TestParseLiveStart(t *testing.T) {
	// RFC3339 in released field wins with high precision
	if got := parseLiveStart("Oct 9, 13:00", "2026-10-09T07:30:00.000Z"); got.IsZero() || !got.UTC().Equal(time.Date(2026, 10, 9, 7, 30, 0, 0, time.UTC)) {
		t.Errorf("RFC3339 released parse = %v, want 2026-10-09 07:30 UTC", got.UTC())
	}
	got := parseLiveStart("2026-09-21 21:00 GMT+5:30", "")
	if got.IsZero() {
		t.Fatal("GMT-offset releaseInfo must parse")
	}
	// 21:00 at +05:30 is 15:30 UTC.
	want := time.Date(2026, 9, 21, 15, 30, 0, 0, time.UTC)
	if !got.UTC().Equal(want) {
		t.Errorf("parse = %v, want %v", got.UTC(), want)
	}
	if got := parseLiveStart("2026-09-20 10:00", ""); got.IsZero() {
		t.Error("bare datetime must parse")
	}
	if got := parseLiveStart("24/7", ""); !got.IsZero() {
		t.Errorf("channel marker must stay zero, got %v", got)
	}
	if got := parseLiveStart("", ""); !got.IsZero() {
		t.Errorf("blank must stay zero, got %v", got)
	}
}

func TestResolveLiveSource(t *testing.T) {
	origBase := penguAPIBase
	defer func() { penguAPIBase = origBase }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/stream/tv/pp-live:daddylive~123.json") {
			t.Errorf("unexpected request path: %q, want /stream/tv/...", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(penguResponse{
			Streams: []penguStreamItem{
				{
					Name:  "🐧 PenguPlay 🐧 Auto • DaddyLive · tv1 123",
					Title: "Match Stream",
					URL:   "https://pengu.uk/hls/live-sports/playlist/abc/index.m3u8",
					BehaviorHints: penguBehaviorHints{
						ProxyHeaders: &penguProxyHeader{
							Request: map[string]string{
								"Referer": "https://tiestep.top/",
							},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	penguAPIBase = server.URL
	c := &Client{
		httpClient:    server.Client(),
		configSegment: "ztest",
	}

	sources, err := c.ResolveSource(context.Background(), "pp-live:daddylive~123", provider.Episode{Title: "Match 1"})
	if err != nil {
		t.Fatalf("ResolveSource() error: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("got %d sources, want 1", len(sources))
	}
	if sources[0].Referer != "https://tiestep.top/" {
		t.Errorf("Referer = %q, want https://tiestep.top/", sources[0].Referer)
	}
	if sources[0].Type != provider.SourceTypeHLS {
		t.Errorf("Type = %q, want hls", sources[0].Type)
	}
	if !strings.Contains(sources[0].Quality, "DaddyLive") {
		t.Errorf("Quality = %q, want DaddyLive in source name", sources[0].Quality)
	}
}

func TestFetchEpisodesLive(t *testing.T) {
	c := &Client{}
	series := provider.SearchResult{
		Title:     "🔴 LIVE: Match",
		ID:        "pp-live:123",
		Type:      provider.ModeLive,
		MediaType: provider.MediaTypeLive,
	}
	eps, err := c.FetchEpisodes(context.Background(), series)
	if err != nil {
		t.Fatalf("FetchEpisodes() error: %v", err)
	}
	if len(eps) != 1 || eps[0].ID != "pp-live:123" || eps[0].Title != "🔴 LIVE: Match" {
		t.Errorf("unexpected episodes: %+v", eps)
	}
}
