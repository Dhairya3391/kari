package thedaddy

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kari/internal/provider"
)

// homeFixture is the scraped homepage: one inline schedule day plus a section
// whose days arrive from schedule-api.php.
const homeFixture = `<html><body>
<a class="card" href="/watch.php?id=51" data-title="abc usa"><div class="card__title">ABC USA</div></a>
<section>
  <h2 class="collapsible-header">Schedule</h2>
  <div class="schedule">
    <div class="schedule__day">
      <div class="schedule__dayTitle">Tuesday 17th Nov 2026 - Schedule Time UK GMT</div>
      <div class="schedule__category is-expanded">
        <div class="schedule__catHeader"><div class="card__meta">Motorsport</div></div>
        <div class="schedule__categoryBody">
          <div class="schedule__event">
            <div class="schedule__eventHeader">
              <span class="schedule__time" data-time="13:30">13:30</span>
              <span class="schedule__eventTitle">Formula 1 Qatar Grand Prix</span>
            </div>
            <div class="schedule__channels">
              <a href="/watch.php?id=60">Sky Sports F1 UK</a>
              <a href="/watch.php?id=61">beIN SPORTS 1 English</a>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</section>
<section>
  <h2 class="collapsible-header">Extra Schedule</h2>
  <div class="schedule" data-endpoint="/schedule-api.php?source=extra"></div>
</section>
</body></html>`

// extraFixture is the extra schedule fragment returned by the JSON endpoint.
const extraFixture = `{"success":true,"html":"<div class=\"schedule__day\"><div class=\"schedule__dayTitle\">Tuesday 17th Nov 2026 - Schedule Time UK GMT</div><div class=\"schedule__category is-expanded\"><div class=\"card__meta\">Extra Sports</div><div class=\"schedule__event\"><span class=\"schedule__time\" data-time=\"15:00\">15:00</span><span class=\"schedule__eventTitle\">Darts Final</span><div class=\"schedule__channels\"><a href=\"/watch.php?id=51\">ABC USA</a></div></div></div></div>"}`

// site is a fake thedaddy.li used by the tests.
type site struct {
	server      *httptest.Server
	homeHits    atomic.Int32
	siteURL     string
	castingCode int
	playerMode  string
}

// newSite starts a fake directory. playerMode selects how the second player
// answers: "" serves 404, "wideiptv" serves a streamUrl embed, "dembed"
// serves the premiumtv token flow.
func newSite(t *testing.T, playerMode string) *site {
	t.Helper()
	fake := &site{playerMode: playerMode, castingCode: http.StatusOK}
	if playerMode != "" && playerMode != "casting" {
		fake.castingCode = http.StatusNotFound
	}
	mux := http.NewServeMux()
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	fake.siteURL = fake.server.URL

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		fake.homeHits.Add(1)
		_, _ = w.Write([]byte(homeFixture))
	})
	mux.HandleFunc("/24-7-channels.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<div class="grid"><a class="card" href="/watch.php?id=51"><div class="card__title">ABC USA</div></a></div>`))
	})
	mux.HandleFunc("/schedule-api.php", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(extraFixture))
	})
	mux.HandleFunc("/casting/stream-60.php", func(w http.ResponseWriter, r *http.Request) {
		if fake.castingCode != http.StatusOK {
			http.Error(w, "gone", fake.castingCode)
			return
		}
		fmt.Fprintf(w, `<iframe class="video responsive" src="%s/embed.php?c=60"></iframe>`, fake.siteURL)
	})
	mux.HandleFunc("/cast/stream-60.php", func(w http.ResponseWriter, r *http.Request) {
		if fake.playerMode != "econfig" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `<iframe src="%s/embed.php?e=60" id="thatframe"></iframe>`, fake.siteURL)
	})
	mux.HandleFunc("/player/stream-60.php", func(w http.ResponseWriter, r *http.Request) {
		if fake.playerMode != "wideiptv" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `<iframe class="video responsive" src="%s/embed.php?p=60"></iframe>`, fake.siteURL)
	})
	mux.HandleFunc("/stream/stream-60.php", func(w http.ResponseWriter, r *http.Request) {
		if fake.playerMode != "dembed" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `<iframe class="video responsive" src="%s/embed.php?d=60"></iframe>`, fake.siteURL)
	})
	mux.HandleFunc("/watch/stream-60.php", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/embed.php", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("e") == "60" {
			payload := encodeEconfig(t, `{"stream_url":"`+fake.siteURL+`/live/index.m3u8?token=abc"}`)
			fmt.Fprintf(w, `<script>window._econfig = '%s';</script>`, payload)
			return
		}
		switch r.URL.Query().Get("p") {
		case "60":
			fmt.Fprintf(w, `<script>streamUrl: "%s/live/index.m3u8?token=abc", tokenExpiresIn: 120</script>`, fake.siteURL)
			return
		}
		if r.URL.Query().Get("d") == "60" {
			fmt.Fprintf(w, `<script>const SRC = "%s/premium60/index.m3u8"; const EDGE = "%s"; const CHANNEL = "premium60";</script>`, fake.siteURL, fake.siteURL)
			return
		}
		fmt.Fprintf(w, `<script>PulsePlayer({ src: "%s/live/index.m3u8?token=abc" });</script>`, fake.siteURL)
	})
	mux.HandleFunc("/api/playlist-token", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("slug") != "premium60" || r.Header.Get("Referer") == "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"minted","ttl":120}`))
	})
	playlist := func(token string) string {
		suffix := ""
		if token != "" {
			suffix = "?token=" + token
		}
		return "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000\n" +
			"tracks-v1/mono.m3u8" + suffix + "\n"
	}
	mux.HandleFunc("/live/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "abc" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(playlist("")))
	})
	mux.HandleFunc("/premium60/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") != "minted" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(playlist("minted")))
	})
	mux.HandleFunc("/live/tracks-v1/mono.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2,\nseg1.ts\n"))
	})
	mux.HandleFunc("/premium60/tracks-v1/mono.m3u8", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "minted" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2,\nseg1.ts\n"))
	})
	mux.HandleFunc("/live/tracks-v1/seg1.ts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		_, _ = w.Write([]byte{0x47, 0x40, 0x00, 0x10})
	})
	mux.HandleFunc("/premium60/tracks-v1/seg1.ts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		_, _ = w.Write([]byte{0x47, 0x40, 0x00, 0x10})
	})
	return fake
}

// newTestClient returns a client pointed at a fake site.
func newTestClient(t *testing.T, playerMode string) (*Client, *site) {
	t.Helper()
	fake := newSite(t, playerMode)
	client, err := NewClient(fake.siteURL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client, fake
}

func TestTheDaddyMetadata(t *testing.T) {
	client, _ := newTestClient(t, "")
	if client.Name() != "thedaddy" {
		t.Errorf("Name = %q, want thedaddy", client.Name())
	}
	if client.Alias() != "TheDaddy" {
		t.Errorf("Alias = %q, want TheDaddy", client.Alias())
	}
	modes := client.Modes()
	if len(modes) != 1 || modes[0].Name != provider.ModeLive || modes[0].Priority != 1 {
		t.Fatalf("Modes = %+v, want live at priority 1", modes)
	}
	features := client.Features(provider.ModeLive)
	if !features.AllowEmptyQuery || features.SearchPlaceholder == "" {
		t.Errorf("live features = %+v, want browse-friendly live features", features)
	}
	if client.Features(provider.ModeTV) != (provider.Features{}) {
		t.Errorf("non-live features should be zero")
	}
}

func TestSearchScrapesGuide(t *testing.T) {
	client, fake := newTestClient(t, "")
	results, err := client.Search(context.Background(), "", provider.ModeLive)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// Two events (homepage + extra section) and one 24/7 channel.
	if len(results) != 3 {
		t.Fatalf("Search returned %d results, want 3: %+v", len(results), results)
	}

	first := results[0]
	if first.Title != "Formula 1 Qatar Grand Prix" {
		t.Fatalf("first result = %q", first.Title)
	}
	if first.ID != "td:ev:60+61" {
		t.Errorf("event ID = %q, want td:ev:60+61", first.ID)
	}
	if first.Group != "Motorsport" || first.Type != provider.ModeLive || first.MediaType != provider.MediaTypeLive {
		t.Errorf("event fields = %+v", first)
	}
	wantStart := time.Date(2026, time.November, 17, 13, 30, 0, 0, scheduleLocation())
	if !first.StartsAt.Equal(wantStart) {
		t.Errorf("event StartsAt = %v, want %v", first.StartsAt, wantStart)
	}
	if len(first.Genres) != 2 || first.Genres[1] != "Schedule" {
		t.Errorf("event Genres = %v", first.Genres)
	}

	var extra *provider.SearchResult
	for i := range results {
		if results[i].Title == "Darts Final" {
			extra = &results[i]
		}
	}
	if extra == nil {
		t.Fatalf("extra schedule event missing: %+v", results)
	}
	if extra.ID != "td:ev:51" || extra.Group != "Extra Sports" {
		t.Errorf("extra event = %+v", extra)
	}
	if !extra.StartsAt.Equal(time.Date(2026, time.November, 17, 15, 0, 0, 0, scheduleLocation())) {
		t.Errorf("extra StartsAt = %v", extra.StartsAt)
	}

	var channel *provider.SearchResult
	for i := range results {
		if results[i].ID == "td:ch:51" {
			channel = &results[i]
		}
	}
	if channel == nil || channel.Title != "ABC USA" || channel.Group != "Channel" || !channel.Live {
		t.Errorf("channel result = %+v", channel)
	}

	filtered, err := client.Search(context.Background(), "qatar", provider.ModeLive)
	if err != nil {
		t.Fatalf("Search(qatar): %v", err)
	}
	if len(filtered) != 1 || filtered[0].Title != "Formula 1 Qatar Grand Prix" {
		t.Errorf("filtered results = %+v", filtered)
	}
	if got := fake.homeHits.Load(); got != 1 {
		t.Errorf("homepage fetched %d times, want 1 (guide cache)", got)
	}
}

func TestSearchRejectsOtherModes(t *testing.T) {
	client, fake := newTestClient(t, "")
	if _, err := client.Search(context.Background(), "abc", provider.ModeTV); !errors.Is(err, provider.ErrNoResults) {
		t.Errorf("Search(ModeTV) err = %v, want ErrNoResults", err)
	}
	if fake.homeHits.Load() != 0 {
		t.Error("non-live search should not scrape the site")
	}
}

func TestEpisodesAndResolveCasting(t *testing.T) {
	client, _ := newTestClient(t, "")
	series := provider.SearchResult{Title: "Formula 1", ID: "td:ev:60+61"}
	episodes, err := client.FetchEpisodes(context.Background(), series)
	if err != nil {
		t.Fatalf("FetchEpisodes: %v", err)
	}
	if len(episodes) != 1 || episodes[0].ID != series.ID {
		t.Fatalf("FetchEpisodes = %+v", episodes)
	}
	if _, err := client.FetchEpisodes(context.Background(), provider.SearchResult{}); !errors.Is(err, provider.ErrNoEpisodes) {
		t.Errorf("empty series err = %v, want ErrNoEpisodes", err)
	}

	sources, err := client.ResolveSource(context.Background(), series.ID, episodes[0])
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("ResolveSource returned %d sources, want 1", len(sources))
	}
	source := sources[0]
	if source.Type != provider.SourceTypeHLS || source.Backend != "PLAYER 5" || source.Quality != "Auto" {
		t.Errorf("source = %+v", source)
	}
	if !strings.Contains(source.URL, "/live/index.m3u8?token=abc") {
		t.Errorf("source URL = %q, want the signed casting playlist", source.URL)
	}
	if source.Referer != "" {
		t.Errorf("casting lane should not send a Referer, got %q", source.Referer)
	}

	if _, err := client.ResolveSource(context.Background(), "td:ev:", provider.Episode{}); !errors.Is(err, provider.ErrNoSources) {
		t.Errorf("invalid handle err = %v, want ErrNoSources", err)
	}
}

func TestResolveFallsBackToPlayer(t *testing.T) {
	client, _ := newTestClient(t, "wideiptv")
	sources, err := client.ResolveSource(context.Background(), "td:ch:60", provider.Episode{})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if sources[0].Backend != "PLAYER 6" {
		t.Errorf("backend = %q, want PLAYER 6", sources[0].Backend)
	}
}

func TestResolveDecryptsEconfig(t *testing.T) {
	client, _ := newTestClient(t, "econfig")
	sources, err := client.ResolveSource(context.Background(), "td:ch:60", provider.Episode{})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if sources[0].Backend != "PLAYER 2" || !strings.Contains(sources[0].URL, "/live/index.m3u8?token=abc") {
		t.Errorf("econfig source = %+v", sources[0])
	}
}

// encodeEconfig reverses the site's chunk shuffle so the decryptor can be
// tested against a payload it did not itself produce.
func encodeEconfig(t *testing.T, payload string) string {
	t.Helper()
	inner := base64.StdEncoding.EncodeToString([]byte(payload))
	fragmentSize := len(inner) / econfigParts
	chunks := make([]string, econfigParts)
	for i := 0; i < econfigParts; i++ {
		start := econfigOrder[i] * fragmentSize
		fragment := inner[start : start+fragmentSize]
		encoded := base64.StdEncoding.EncodeToString([]byte(fragment))
		// The decoder drops the fourth character of every chunk.
		chunks[i] = encoded[:3] + "A" + encoded[3:]
	}
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(chunks, "")))
}

func TestResolveMintsDembedToken(t *testing.T) {
	client, _ := newTestClient(t, "dembed")
	sources, err := client.ResolveSource(context.Background(), "td:ch:60", provider.Episode{})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if sources[0].Backend != "PLAYER 1" || !strings.Contains(sources[0].URL, "t=minted") {
		t.Errorf("dembed source = %+v", sources[0])
	}
	if sources[0].Referer == "" || !sources[0].SuppressOrigin {
		t.Errorf("dembed lane should carry its embed Referer: %+v", sources[0])
	}
}

func TestResolveWithoutPlayableLanes(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.ResolveSource(context.Background(), "td:ch:60", provider.Episode{}); !errors.Is(err, provider.ErrNoSources) {
		t.Errorf("err = %v, want ErrNoSources", err)
	}
}

func TestSearchReportsUpstreamFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.Search(context.Background(), "", provider.ModeLive); err == nil {
		t.Error("expected an error when the site is down")
	}
}

func TestNewClientValidatesBaseURL(t *testing.T) {
	if _, err := NewClient(""); err == nil {
		t.Error("expected an error for an empty base URL")
	}
	if _, err := NewClient("not a url"); err == nil {
		t.Error("expected an error for an invalid base URL")
	}
}
