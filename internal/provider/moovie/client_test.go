package moovie

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

func newFixture(t *testing.T) *Client {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/providers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"vaplayer": {"name":"Poseidon","enabled":true,"priority":1},
			"web": {"name":"Web","enabled":true,"isClientSide":true,"priority":5},
			"off": {"name":"Off","enabled":false,"priority":2}
		}`)
	})

	mux.HandleFunc("/scrape/source", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: update\ndata: {\"id\":\"vaplayer\",\"percentage\":50}\n\n")
		fmt.Fprint(w, `event: completed
data: {"stream":[
  {"type":"hls","playlist":"https://cdn.example.com/a.m3u8","captions":[{"language":"English","url":"https://cdn.example.com/a.en.vtt"}]},
  {"type":"hls","playlist":"https://cdn.example.com/b.m3u8","quality":"Hindi","captions":[]}
]}

event: done
data: ""
`)
	})

	mux.HandleFunc("/movie/27205", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"title":"Inception","release_date":"2010-07-16","imdb_id":"tt1375666","number_of_seasons":0,"external_ids":{"imdb_id":"tt1375666"}}`)
	})
	mux.HandleFunc("/tv/1396", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"name":"Breaking Bad","first_air_date":"2008-01-20","imdb_id":"tt0903747","number_of_seasons":5,"external_ids":{"imdb_id":"tt0903747"}}`)
	})

	mux.HandleFunc("/api/subtitles", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"captions":[
			{"language":"English","url":"https://cdn.example.com/main.en.vtt"},
			{"language":"Hindi","url":"https://cdn.example.com/main.hi.vtt","needsProxy":true}
		]}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, err := NewClientWithBaseURL(tmdb.NewKeyPool([]string{"k"}), srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL: %v", err)
	}
	return c
}

func TestMoovieCapabilities(t *testing.T) {
	c, err := NewClient(tmdb.NewKeyPool([]string{"k"}))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.Alias() == "" || c.Name() != "moovie" {
		t.Errorf("identity wrong: %q %q", c.Alias(), c.Name())
	}
	for _, m := range []provider.ContentType{provider.ModeMovies, provider.ModeTV, provider.ModeCartoon} {
		found := false
		for _, mode := range c.Modes() {
			if mode.Name == m && mode.Priority == 2 {
				found = true
			}
		}
		if !found {
			t.Errorf("mode %q missing at priority 2: %+v", m, c.Modes())
		}
	}
}

func TestParseSSE(t *testing.T) {
	events := parseSSE("event: update\ndata: {\"a\":1}\n\n: keep-alive\n\nevent: completed\ndata: {\"b\":2}\n")
	if len(events) != 2 || events[0].event != "update" || events[1].event != "completed" {
		t.Errorf("events = %+v", events)
	}
	multi := parseSSE("event: completed\ndata: {\"stream\":[\n  {\"url\":\"x\"}\n]}\n\n")
	if len(multi) != 1 || !strings.Contains(multi[0].data, `"url":"x"`) {
		t.Errorf("multi-line data must join: %+v", multi)
	}
}

func TestDecodeStreamListEmbeds(t *testing.T) {
	items := decodeStreamList(json.RawMessage(`[{"url":"https://cdn.example.com/embed.m3u8"}]`), nil)
	if len(items) != 1 || items[0].URL != "https://cdn.example.com/embed.m3u8" {
		t.Fatalf("embeds = %+v", items)
	}
	single := decodeStreamList(nil, json.RawMessage(`{"url":"https://cdn.example.com/single.m3u8"}`))
	if len(single) != 1 || single[0].URL != "https://cdn.example.com/single.m3u8" {
		t.Fatalf("single embed = %+v", single)
	}
}

func TestFetchProvidersFiltersClientSide(t *testing.T) {
	c := newFixture(t)
	providers, err := c.fetchProviders(context.Background())
	if err != nil {
		t.Fatalf("fetchProviders: %v", err)
	}
	if len(providers) != 1 || providers[0].id != "vaplayer" {
		t.Errorf("providers = %+v, want only vaplayer", providers)
	}
}

func TestMoovieResolve(t *testing.T) {
	c := newFixture(t)
	sources, err := c.ResolveSource(context.Background(), "27205", provider.Episode{TMDBID: 27205})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	// Hindi-dubbed row becomes an audio track; the main row stays.
	if len(sources) < 2 {
		t.Fatalf("expected main + Hindi audio, got %+v", sources)
	}
	if sources[0].URL != "https://cdn.example.com/a.m3u8" {
		t.Errorf("sources[0].URL = %s", sources[0].URL)
	}
	dub := sources[1]
	if dub.Language != "hi" {
		t.Errorf("dub Language = %q, want hi", dub.Language)
	}
	// needsProxy subtitle skipped; direct + item captions kept.
	if len(sources[0].Subtitles) != 2 {
		t.Errorf("subtitles = %+v, want main.en + item caption", sources[0].Subtitles)
	}
}

func TestMoovieResolveTVEpisode(t *testing.T) {
	c := newFixture(t)
	_, err := c.ResolveSource(context.Background(), "1396", provider.Episode{TMDBID: 1396, Season: 1, Episode: 2})
	if err != nil {
		t.Fatalf("ResolveSource tv: %v", err)
	}
}

func TestStreamType(t *testing.T) {
	if got := streamType("https://x/y.m3u8", ""); got != provider.SourceTypeHLS {
		t.Errorf("m3u8 = %q", got)
	}
	if got := streamType("https://x/y", "mp4"); got != provider.SourceTypeMP4 {
		t.Errorf("declared mp4 = %q", got)
	}
	if !strings.Contains("dash", streamType("https://x/y.mpd", "")) {
		t.Errorf("mpd = %q", streamType("https://x/y.mpd", ""))
	}
}
