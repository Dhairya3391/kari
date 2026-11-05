package timstreams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kari/internal/provider"
	"kari/internal/provider/providertest"
)

func TestClientMetadata(t *testing.T) {
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.Name() != "timstreams" {
		t.Errorf("Name = %q, want timstreams", c.Name())
	}
	if c.Alias() != "TimStreams" {
		t.Errorf("Alias = %q, want TimStreams", c.Alias())
	}
	modes := c.Modes()
	if len(modes) != 1 || modes[0].Name != provider.ModeLive {
		t.Errorf("Modes = %+v, want ModeLive", modes)
	}
	feat := c.Features(provider.ModeLive)
	if !feat.AllowEmptyQuery || !feat.NoCachedSearches {
		t.Errorf("Features(ModeLive) = %+v, want AllowEmptyQuery and NoCachedSearches", feat)
	}
	if c.Features(provider.ModeTV) != (provider.Features{}) {
		t.Errorf("Features(ModeTV) should be empty")
	}
	deadlines := c.Deadlines(provider.ModeLive)
	if deadlines.Source != 40*time.Second {
		t.Errorf("Deadlines(ModeLive) = %+v, want 40s source deadline", deadlines)
	}
}

func TestClientSearch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/live-upcoming", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(eventCatalog{
			Events: []event{
				{
					URL:   "https://tim-streams.com/watch/event/match-one",
					Name:  "Team A vs Team B",
					Logo:  "https://example.com/logo.png",
					Genre: 1,
					Time:  "15:00",
					VIP:   false,
					Streams: []stream{
						{Name: "Stream 1", URL: "https://short.example.com/s1", VIP: false},
					},
				},
				{
					URL:   "https://tim-streams.com/watch/event/vip-match",
					Name:  "VIP Match",
					Genre: 1,
					VIP:   true,
					Streams: []stream{
						{Name: "VIP Stream", URL: "https://short.example.com/vip", VIP: true},
					},
				},
			},
			Genres: []genre{
				{ID: 1, Name: "Football"},
			},
		})
	})
	mux.HandleFunc("/channels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(channelCatalog{
			Channels: []event{
				{
					URL:   "https://tim-streams.com/watch/channel/sky-sports",
					Name:  "Sky Sports Main Event",
					Logo:  "https://example.com/sky.png",
					Genre: 2,
					Streams: []stream{
						{Name: "Channel Stream", URL: "https://short.example.com/ch1", VIP: false},
					},
				},
			},
			Genres: []genre{
				{ID: 2, Name: "Sports"},
			},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, err := NewClientWithURLs(srv.URL, "https://short.example.com", srv.Client())
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	results, err := client.Search(context.Background(), "", provider.ModeLive)
	if err != nil {
		t.Fatalf("Search empty: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (VIP omitted)", len(results))
	}

	// Filtered search
	filtered, err := client.Search(context.Background(), "Sky", provider.ModeLive)
	if err != nil {
		t.Fatalf("Search Sky: %v", err)
	}
	if len(filtered) != 1 || !strings.Contains(filtered[0].Title, "Sky Sports") {
		t.Errorf("filtered = %+v, want Sky Sports", filtered)
	}

	// Non-matching search
	_, err = client.Search(context.Background(), "Nonexistent", provider.ModeLive)
	if !errors.Is(err, provider.ErrNoResults) {
		t.Errorf("Search Nonexistent err = %v, want ErrNoResults", err)
	}

	// Wrong mode
	_, err = client.Search(context.Background(), "", provider.ModeMovies)
	if !errors.Is(err, provider.ErrNoResults) {
		t.Errorf("Search ModeMovies err = %v, want ErrNoResults", err)
	}
}

func TestClientFetchEpisodes(t *testing.T) {
	c, _ := NewClient()
	_, err := c.FetchEpisodes(context.Background(), provider.SearchResult{})
	if !errors.Is(err, provider.ErrNoEpisodes) {
		t.Errorf("FetchEpisodes(empty) err = %v, want ErrNoEpisodes", err)
	}

	eps, err := c.FetchEpisodes(context.Background(), provider.SearchResult{ID: "timstreams:event:123", Title: "Event 123"})
	if err != nil {
		t.Fatalf("FetchEpisodes: %v", err)
	}
	if len(eps) != 1 || eps[0].ID != "timstreams:event:123" || eps[0].Title != "Event 123" {
		t.Errorf("eps = %+v", eps)
	}
}

func TestClientResolveSource(t *testing.T) {
	mux := http.NewServeMux()

	// Short link page with encoded ticket URL loader
	// Pattern expected: var a = [bytes], b = key, c = offset,
	// Decoded: loadStreamUrl("https://ticket.sytes.net/token123")
	key := 42
	offset := 10
	plain := `loadStreamUrl("https://ticket.sytes.net/token123")`
	nums := make([]string, len(plain))
	for i := range plain {
		// Encoded such that: ((value ^ key) - offset + 256) & 0xff == byte
		// (val ^ key) & 0xff = (byte + offset) & 0xff
		val := ((int(plain[i]) + offset) & 0xff) ^ key
		nums[i] = fmt.Sprintf("%d", val)
	}
	pageHTML := fmt.Sprintf(`<html><body><script>var enc = [%s], k = %d, o = %d, dummy = 0; console.log("ready");</script></body></html>`,
		strings.Join(nums, ","), key, offset)

	mux.HandleFunc("/s1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(pageHTML))
	})

	// Ticket endpoint: returns encrypted URL for "https://stream.example.com/live/playlist.m3u8"
	// token is "token123" (path.Base)
	token := "token123"
	streamURL := "https://stream.example.com/live/playlist.m3u8"
	blob := make([]byte, len(streamURL)+1)
	blob[0] = 7 // seed
	for i := 1; i < len(blob); i++ {
		// decoded[i-1] = blob[i] ^ key[(i-1)%len(key)] ^ byte((int(blob[0])+i-1)&0xff)
		// blob[i] = decoded[i-1] ^ key[(i-1)%len(key)] ^ byte((int(blob[0])+i-1)&0xff)
		c := streamURL[i-1]
		k := token[(i-1)%len(token)]
		mask := byte((int(blob[0]) + i - 1) & 0xff)
		blob[i] = c ^ k ^ mask
	}

	mux.HandleFunc("/token123", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(blob)
	})

	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	// Watch item endpoint on API base
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/watch/match-one", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(watchResponse{
			Item: event{
				Name: "Match One",
				Streams: []stream{
					{Name: "Source 1", URL: srv.URL + "/s1", VIP: false},
				},
			},
		})
	})
	apiSrv := httptest.NewServer(apiMux)
	defer apiSrv.Close()

	// Extract shortBase host to match
	shortBase := srv.URL
	httpClient := srv.Client()
	origTransport := httpClient.Transport
	httpClient.Transport = providertest.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "ticket.sytes.net" {
			cloned := req.Clone(req.Context())
			cloned.URL.Scheme = "https"
			cloned.URL.Host = srv.Listener.Addr().String()
			return origTransport.RoundTrip(cloned)
		}
		return origTransport.RoundTrip(req)
	})

	client, err := NewClientWithURLs(apiSrv.URL, shortBase, httpClient)
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}

	sources, err := client.ResolveSource(context.Background(), "timstreams:event:match-one", provider.Episode{})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("got %d sources, want 1", len(sources))
	}
	if sources[0].URL != streamURL {
		t.Errorf("sources[0].URL = %q, want %q", sources[0].URL, streamURL)
	}
	if sources[0].Type != provider.SourceTypeHLS {
		t.Errorf("sources[0].Type = %q, want %q", sources[0].Type, provider.SourceTypeHLS)
	}
}
