package miruro

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kari/internal/provider"
)

// searchFixture is a search __data.json payload with two catalog entries:
// Naruto (AniList 20) and a record without any AniList mapping.
const searchFixture = `{"type":"data","nodes":[{"type":"data","data":[0]},{"type":"data","data":[{"mode":1,"items":2},"anime",[3,13],{"id":4,"external_ids":5,"title":7,"format":10,"season_year":11,"cover_url":12},"SHOWID",{"anilist":6},["20"],{"english":8,"romaji":9},"Naruto","NARUTO","TV",2002,"https://cover.example.com/naruto.jpg",{"id":14,"external_ids":15,"title":16,"format":10,"season_year":17,"cover_url":18},"OTHER",{"mal":["1"]},"No Game",2014,"https://cover.example.com/nogame.jpg"]}]}`

// episodesChunk is the streamed episode-list chunk of a watch payload.
const episodesChunk = `{"type":"chunk","id":1,"data":[{"episodes":1,"currentEpisode":2},[3],1,{"id":4,"number":5,"title":6,"filler":7},"ep1",1,"Enter",false]}`

// tracksChunk is the streamed source chunk: one ssub pool (bee) with a
// signed HLS master, external subtitles, and the upstream ranking.
const tracksChunk = `{"type":"chunk","id":2,"data":[{"episodeNumber":1,"tracks":1,"providerOrder":2},[3],["bee"],{"track":4,"providers":5},"ssub",[6],{"provider":7,"servers":8,"subtitles":19},"bee",[9],{"server":10,"headers":11,"streams":13,"embed":18},"Vidstream-1",{"Referer":12},"https://megaplay.buzz/",[14],{"url":15,"format":16,"quality":17},"https://cdn.example.com/master.m3u8?token=abc","hls","",null,[20],{"language":21,"label":22,"file":23,"default":24},"en","English","https://cdn.example.com/en.vtt",true]}`

// newFixture serves canned __data.json payloads: search matches, a slug
// redirect for the placeholder slug, and the multi-line watch payload for
// the canonical slug.
func newFixture(t *testing.T) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/search/__data.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, searchFixture)
	})
	mux.HandleFunc("/watch/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/x/") {
			fmt.Fprint(w, `{"type":"redirect","location":"/watch/SHOWID/naruto?ep=1"}`)
			return
		}
		fmt.Fprint(w, `{"type":"data","nodes":[{"type":"data","data":[0]}]}`+"\n"+episodesChunk+"\n"+tracksChunk+"\n")
	})
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"Media":{"id":20,"title":{"romaji":"NARUTO","english":"Naruto","userPreferred":"Naruto","native":"NARUTO"},"synonyms":[],"seasonYear":2002,"format":"TV"}}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, err := NewClientWithBases(srv.URL, srv.URL+"/graphql")
	if err != nil {
		t.Fatalf("NewClientWithBases: %v", err)
	}
	return c
}

func TestMiruroCapabilities(t *testing.T) {
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.Name() != "miruro" || c.Alias() == "" {
		t.Errorf("identity wrong: %q %q", c.Name(), c.Alias())
	}
	if len(c.Modes()) != 1 || c.Modes()[0].Name != provider.ModeAnime {
		t.Errorf("modes = %+v, want anime only", c.Modes())
	}
	if !c.RequiresEpisodeListForMovies() {
		t.Error("movies must go through the episode flow")
	}
	if !c.Features(provider.ModeAnime).AudioSelection {
		t.Error("anime must declare audio selection")
	}
	if got := c.Features(provider.ModeMovies); got.AudioSelection {
		t.Error("non-anime modes must not declare audio selection")
	}
}

func TestMiruroSearchKeepsAniListIDs(t *testing.T) {
	c := newFixture(t)
	results, err := c.Search(context.Background(), "naruto", provider.ModeAnime)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1 (unmapped entry skipped)", len(results))
	}
	got := results[0]
	if got.ID != "20" {
		t.Errorf("ID = %q, want AniList id 20", got.ID)
	}
	if got.Title != "Naruto" {
		t.Errorf("Title = %q, want Naruto", got.Title)
	}
	if got.Year != "2002" {
		t.Errorf("Year = %q, want 2002", got.Year)
	}
	if got.MediaType != provider.MediaTypeAnime {
		t.Errorf("MediaType = %q, want anime", got.MediaType)
	}
	if got.CoverURL == "" {
		t.Error("CoverURL must be stamped from the catalog")
	}
}

func TestMiruroSearchEmptyQuery(t *testing.T) {
	c := newFixture(t)
	if _, err := c.Search(context.Background(), "  ", provider.ModeAnime); err == nil {
		t.Error("empty query must fail")
	}
}

func TestMiruroFetchEpisodes(t *testing.T) {
	c := newFixture(t)
	eps, err := c.FetchEpisodes(context.Background(), provider.SearchResult{ID: "20", Title: "Naruto"})
	if err != nil {
		t.Fatalf("FetchEpisodes: %v", err)
	}
	if len(eps) != 2 {
		t.Fatalf("episodes = %d, want sub+dub pair", len(eps))
	}
	if eps[0].Audio != provider.AudioSub || eps[1].Audio != provider.AudioDub {
		t.Errorf("audios = %q %q, want sub dub", eps[0].Audio, eps[1].Audio)
	}
	for _, ep := range eps {
		if ep.Episode != 1 || ep.Season != 1 {
			t.Errorf("episode = S%dE%d, want S1E1", ep.Season, ep.Episode)
		}
		if !strings.HasPrefix(ep.ID, "watch/miruro/SHOWID/") {
			t.Errorf("episode ID = %q, want miruro handle", ep.ID)
		}
	}
	if eps[0].Title != "Enter" {
		t.Errorf("title = %q, want Enter", eps[0].Title)
	}
}

func TestMiruroResolveSub(t *testing.T) {
	c := newFixture(t)
	sources, err := c.ResolveSource(context.Background(), "20", provider.Episode{
		ID:      "watch/miruro/SHOWID/sub/1",
		Episode: 1,
		Audio:   provider.AudioSub,
	})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("sources = %d, want 1", len(sources))
	}
	s := sources[0]
	if s.URL != "https://cdn.example.com/master.m3u8?token=abc" {
		t.Errorf("URL = %q", s.URL)
	}
	if s.Quality != "Auto (Vidstream-1)" {
		t.Errorf("Quality = %q, want server-tagged auto", s.Quality)
	}
	if s.Referer != "https://megaplay.buzz/" {
		t.Errorf("Referer = %q", s.Referer)
	}
	if s.Type != provider.SourceTypeHLS {
		t.Errorf("Type = %q, want hls", s.Type)
	}
	if s.SubType != provider.SubTypeSoft {
		t.Errorf("SubType = %q, want soft", s.SubType)
	}
	if len(s.ExtraArgs) != 0 {
		t.Errorf("extensioned playlists need no demuxer quirk, got %v", s.ExtraArgs)
	}
	if len(s.Subtitles) != 1 || s.Subtitles[0].URL != "https://cdn.example.com/en.vtt" {
		t.Errorf("Subtitles = %+v, want the english vtt", s.Subtitles)
	}
}

func TestMiruroResolveDubUnavailable(t *testing.T) {
	c := newFixture(t)
	_, err := c.ResolveSource(context.Background(), "20", provider.Episode{
		ID:      "watch/miruro/SHOWID/dub/1",
		Episode: 1,
		Audio:   provider.AudioDub,
	})
	if !errors.Is(err, provider.ErrAudioUnavailable) {
		t.Fatalf("err = %v, want ErrAudioUnavailable", err)
	}
}

func TestMiruroResolveCrossProviderEpisode(t *testing.T) {
	c := newFixture(t)
	// Another provider's episode handle carries no show ID; resolution
	// falls back to the AniList search match (cache is cold here).
	sources, err := c.ResolveSource(context.Background(), "20", provider.Episode{
		Episode: 1,
		Audio:   provider.AudioSub,
	})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("sources = %d, want 1", len(sources))
	}
}

func TestServerSourceSubTypes(t *testing.T) {
	mk := func(track string) providerEntry {
		return providerEntry{
			name:  "bee",
			track: track,
			servers: []serverEntry{{
				server:  "Vidstream-1",
				referer: "https://megaplay.buzz/",
				streams: []streamEntry{{url: "https://cdn.example.com/master.m3u8", format: "hls"}},
			}},
		}
	}
	for _, tc := range []struct {
		track string
		want  string
	}{
		{"ssub", provider.SubTypeSoft},
		{"sub", provider.SubTypeHard},
		{"dub", ""},
	} {
		got := serverSources(mk(tc.track))
		if len(got) != 1 {
			t.Fatalf("track %q: sources = %d, want 1", tc.track, len(got))
		}
		if got[0].SubType != tc.want {
			t.Errorf("track %q: SubType = %q, want %q", tc.track, got[0].SubType, tc.want)
		}
	}
}

func TestDeadHost(t *testing.T) {
	if !deadHost("https://megap.shiora.top/abc/master.m3u8?token=x") {
		t.Error("shiora host must be dropped (serves ad slideshows)")
	}
	if !deadHost("https://x.megap.shiora.top/abc/master.m3u8") {
		t.Error("shiora subdomains must be dropped")
	}
	if deadHost("https://fetch.nexabloom.top/anime/abc/master.m3u8?token=x") {
		t.Error("working hosts must be kept")
	}
	if deadHost("not a url \x7f") {
		t.Error("unparsable URLs must be kept (fail open)")
	}
}

func TestDemuxerQuirk(t *testing.T) { // Extensionless HLS (Vidplay answers image/jpeg) needs forcing.
	if got := demuxerQuirk("https://cdn.example.com/cdn/abc123?t.m3u8", provider.SourceTypeHLS); len(got) != 2 {
		t.Errorf("quirk = %v, want lavf+hls forcing", got)
	}
	if got := demuxerQuirk("https://cdn.example.com/master.m3u8?token=abc", provider.SourceTypeHLS); len(got) != 0 {
		t.Errorf("quirk = %v, want none for extensioned playlists", got)
	}
	if got := demuxerQuirk("https://cdn.example.com/v.mp4", provider.SourceTypeMP4); len(got) != 0 {
		t.Errorf("quirk = %v, want none for mp4", got)
	}
}

func TestStreamType(t *testing.T) {
	if got := streamType(streamEntry{url: "https://cdn.example.com/m.m3u8", format: "hls"}); got != provider.SourceTypeHLS {
		t.Errorf("hls = %q", got)
	}
	if got := streamType(streamEntry{url: "https://cdn.example.com/v", format: "mp4"}); got != provider.SourceTypeMP4 {
		t.Errorf("mp4 = %q", got)
	}
	if got := streamType(streamEntry{url: "https://cdn.example.com/v.mp4"}); got != provider.SourceTypeMP4 {
		t.Errorf("mp4 url = %q", got)
	}
	if got := streamType(streamEntry{url: "https://cdn.example.com/embed"}); got != "" {
		t.Errorf("embed = %q, want dropped", got)
	}
}

func TestRedirectSlug(t *testing.T) {
	slug := redirectSlug([]byte(`{"type":"redirect","location":"/watch/SHOWID/naruto?ep=1"}`))
	if slug != "naruto" {
		t.Errorf("slug = %q, want naruto", slug)
	}
	if slug := redirectSlug([]byte(`{"type":"data","nodes":[]}`)); slug != "" {
		t.Errorf("slug = %q, want empty for data docs", slug)
	}
}
