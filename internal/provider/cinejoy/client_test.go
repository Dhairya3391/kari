package cinejoy

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

func TestCinejoyCapabilities(t *testing.T) {
	c, err := NewClient(tmdb.NewKeyPool([]string{"dummy-key"}))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if c.Alias() != "Cinejoy" {
		t.Errorf("Alias() = %q, want Cinejoy", c.Alias())
	}
	if c.Name() != "cinejoy" {
		t.Errorf("Name() = %q, want cinejoy", c.Name())
	}

	for _, expectedMode := range []provider.ContentType{provider.ModeMovies, provider.ModeTV, provider.ModeCartoon} {
		found := false
		for _, m := range c.Modes() {
			if m.Name == expectedMode && m.Priority == 2 {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("mode %q missing at priority 2: %+v", expectedMode, c.Modes())
		}
	}
}

// TestSealRequestRoundTrip opens a sealed request the way the gateway does and
// seals a reply the way the gateway does, proving the envelope is symmetric.
func TestSealRequestRoundTrip(t *testing.T) {
	serverKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}

	payload := []byte(`{"path":"/Light/movie","payload":{"tmdb":"550"}}`)
	body, session, err := sealRequest(serverKey.PublicKey(), payload)
	if err != nil {
		t.Fatalf("sealRequest: %v", err)
	}
	if len(body) != gateHeaderSize+len(payload)+16 {
		t.Fatalf("body length = %d, want sealed payload with header", len(body))
	}
	if body[0] != gateKeyID || body[1] != gateEnvelopeVersion {
		t.Fatalf("body header = %v, want key id %d version %d", body[:2], gateKeyID, gateEnvelopeVersion)
	}

	request, replySession := openTestRequest(t, serverKey, body)
	if request.Path != "/Light/movie" {
		t.Fatalf("decrypted path = %q", request.Path)
	}
	if request.Payload.TMDB != "550" {
		t.Fatalf("decrypted tmdb = %q", request.Payload.TMDB)
	}

	response := []byte(`{"status":200,"data":{"stream":[]}}`)
	sealedReply := sealTestResponse(t, replySession, response)
	got, err := session.openResponse(sealedReply)
	if err != nil {
		t.Fatalf("openResponse: %v", err)
	}
	if string(got) != string(response) {
		t.Fatalf("opened response = %q, want %q", got, response)
	}
}

func TestGateRejectsTamperedResponse(t *testing.T) {
	serverKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	body, session, err := sealRequest(serverKey.PublicKey(), []byte(`{"path":"/Light/movie","payload":{"tmdb":"550"}}`))
	if err != nil {
		t.Fatalf("sealRequest: %v", err)
	}
	_, replySession := openTestRequest(t, serverKey, body)
	sealed := sealTestResponse(t, replySession, []byte(`{"status":200,"data":{}}`))
	sealed[len(sealed)-1] ^= 0xff
	if _, err := session.openResponse(sealed); err == nil {
		t.Fatal("tampered response must fail authentication")
	}
}

func TestCinejoyResolveMovie(t *testing.T) {
	serverKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}

	siteBase := "https://cinejoy.pk"
	var apiRequests atomic.Int64
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != siteBase {
			http.Error(w, "missing origin", http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(serversResponse{Servers: []serverEntry{
			{Name: "Nebula", Status: "ok"},
			{Name: "Light", Status: "ok", FourK: true},
			{Name: "Dead", Status: "down", FourK: true},
		}})
	})

	mux.HandleFunc("/g", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != siteBase {
			http.Error(w, "missing origin", http.StatusForbidden)
			return
		}
		request, replySession := openTestRequest(t, serverKey, readBody(t, r))
		if !strings.HasSuffix(request.Path, "/movie") || request.Payload.TMDB != "550" {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		apiRequests.Add(1)

		// Masters live on a CDN-looking host so they survive the direct-URL
		// filter; the test client dials them to the local mux.
		var playlist string
		switch {
		case strings.HasPrefix(request.Path, "/Light/"):
			playlist = "http://cdn.cinejoy.test/light/master.m3u8"
		case strings.HasPrefix(request.Path, "/Nebula/"):
			playlist = "http://cdn.cinejoy.test/nebula/master.m3u8"
		default:
			http.Error(w, "unknown lane", http.StatusNotFound)
			return
		}
		response := fmt.Sprintf(`{"status":200,"data":{"stream":[{"type":"hls","playlist":%q,"captions":[{"type":"vtt","url":%q,"language":"en"}]}]}}`,
			playlist, "https://cdn.example.com/captions/en.vtt")
		_, _ = w.Write(sealTestResponse(t, replySession, []byte(response)))
	})

	masters := map[string][2]int{"light": {2160, 1080}, "nebula": {1080, 720}}
	for lane, heights := range masters {
		mux.HandleFunc("/"+lane+"/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Origin") != siteBase {
				http.Error(w, "missing origin", http.StatusForbidden)
				return
			}
			fmt.Fprintf(w, `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=20000000,RESOLUTION=3840x%d
https://cdn.example.com/%s/%dp/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x%d
https://cdn.example.com/%s/%dp/index.m3u8
`, heights[0], lane, heights[0], heights[1], lane, heights[1])
		})
	}

	client := newTestClient(t, serverKey.PublicKey(), srv.URL, siteBase)
	client.httpClient = cdnClient(srv)
	sources, err := client.ResolveSource(t.Context(), "550", provider.Episode{TMDBID: 550})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if got := apiRequests.Load(); got != 2 {
		t.Fatalf("gateway requests = %d, want 2 (down lane skipped)", got)
	}
	if len(sources) != 6 {
		t.Fatalf("sources = %d, want 2 tiers + Auto on each lane: %+v", len(sources), sources)
	}

	want := map[string]bool{
		"Light/4K (2160p)": true,
		"Light/1080p":      true,
		"Light/Auto":       true,
		"Nebula/1080p":     true,
		"Nebula/720p":      true,
		"Nebula/Auto":      true,
	}
	seen := make(map[string]bool, len(sources))
	for _, source := range sources {
		key := source.Backend + "/" + source.Quality
		if !want[key] {
			t.Errorf("unexpected source row %q", key)
		}
		seen[key] = true

		if source.Referer != siteBase+"/" {
			t.Errorf("%s Referer = %q", key, source.Referer)
		}
		if source.SuppressOrigin {
			t.Errorf("%s SuppressOrigin = true, want derived Origin", key)
		}
		if source.Type != provider.SourceTypeHLS {
			t.Errorf("%s Type = %q, want hls", key, source.Type)
		}
		wantHeight := map[string]int{"4K (2160p)": 2160, "1080p": 1080, "720p": 720}
		if h, ok := wantHeight[source.Quality]; ok && source.Height != h {
			t.Errorf("%s Height = %d, want %d", key, source.Height, h)
		}
		if source.Quality == "Auto" && source.Height <= 0 {
			t.Errorf("%s master Height = %d, want the best declared rung", key, source.Height)
		}
		if source.Bitrate <= 0 {
			t.Errorf("%s Bitrate = %d, want declared bandwidth", key, source.Bitrate)
		}
		if source.SubType != provider.SubTypeSoft {
			t.Errorf("%s SubType = %q, want soft from caption type", key, source.SubType)
		}
		if len(source.Subtitles) != 1 || source.Subtitles[0].Language != "en" {
			t.Errorf("%s Subtitles = %+v, want one en track", key, source.Subtitles)
		}
		if len(source.Subtitles) == 1 && source.Subtitles[0].Referer != siteBase+"/" {
			t.Errorf("%s caption Referer = %q, want the site referer (the CDN rejects bare requests)",
				key, source.Subtitles[0].Referer)
		}
		if !strings.HasPrefix(source.URL, "http://cdn.cinejoy.test/") &&
			!strings.HasPrefix(source.URL, "https://cdn.example.com/") {
			t.Errorf("%s URL = %q, want a direct CDN address", key, source.URL)
		}
	}
	if len(seen) != len(want) {
		t.Errorf("resolved %d of %d expected rows: %+v", len(seen), len(want), sources)
	}
}

func TestCinejoyResolveEpisode(t *testing.T) {
	serverKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(serversResponse{Servers: []serverEntry{{Name: "Sirius", Status: "ok", FourK: true}}})
	})
	mux.HandleFunc("/g", func(w http.ResponseWriter, r *http.Request) {
		request, session := openTestRequest(t, serverKey, readBody(t, r))
		if request.Path != "/Sirius/series" || request.Payload.Season != "2" || request.Payload.Episode != "5" {
			http.Error(w, "bad series payload", http.StatusBadRequest)
			return
		}
		_, _ = w.Write(sealTestResponse(t, session, []byte(`{"status":200,"data":{"stream":[]}}`)))
	})

	client := newTestClient(t, serverKey.PublicKey(), srv.URL, "https://cinejoy.pk")
	_, err = client.ResolveSource(t.Context(), "1399", provider.Episode{TMDBID: 1399, Season: 2, Episode: 5})
	if !errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("err = %v, want ErrNoSources", err)
	}
}

// TestCinejoyResolveStreamIncremental proves the streaming contract: every
// lane that finds the title ships its own batch, and a title two lanes share is
// delivered once. Master-less playlists keep the case hermetic.
func TestCinejoyResolveStreamIncremental(t *testing.T) {
	serverKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(serversResponse{Servers: []serverEntry{
			{Name: "Light", Status: "ok"},
			{Name: "Nebula", Status: "ok"},
			{Name: "Sirius", Status: "ok"},
		}})
	})
	mux.HandleFunc("/g", func(w http.ResponseWriter, r *http.Request) {
		request, session := openTestRequest(t, serverKey, readBody(t, r))
		// Sirius mirrors Light, so its row is a duplicate of the first batch.
		laneName := strings.ToLower(strings.Split(strings.TrimPrefix(request.Path, "/"), "/")[0])
		if laneName == "sirius" {
			laneName = "light"
		}
		response := fmt.Sprintf(`{"status":200,"data":{"stream":[{"type":"hls","playlist":%q}]}}`,
			"http://cdn.cinejoy.test/"+laneName+"/media.m3u8")
		_, _ = w.Write(sealTestResponse(t, session, []byte(response)))
	})

	client := newTestClient(t, serverKey.PublicKey(), srv.URL, "https://cinejoy.pk")
	client.httpClient = cdnClient(srv)

	updates := make(chan []provider.MediaSource, 8)
	if err := client.ResolveStream(t.Context(), "550", provider.Episode{TMDBID: 550}, updates); err != nil {
		t.Fatalf("ResolveStream: %v", err)
	}
	if got := len(updates); got != 2 {
		t.Fatalf("batches = %d, want 2 (Light, Nebula; Sirius is a duplicate): %+v", got, updates)
	}

	backends := make(map[string]int)
	urls := make(map[string]int)
	for drained := len(updates); drained > 0; drained-- {
		for _, source := range <-updates {
			backends[source.Backend]++
			urls[source.URL]++
		}
	}
	if len(backends) != 2 || len(urls) != 2 {
		t.Fatalf("backends = %v urls = %v, want two unique rows", backends, urls)
	}
	for url, count := range urls {
		if count != 1 {
			t.Errorf("url %s delivered %d times, want once", url, count)
		}
	}
	if backends["Nebula"] != 1 {
		t.Errorf("Nebula row missing: %v", backends)
	}
	if mirrored := backends["Light"] + backends["Sirius"]; mirrored != 1 {
		t.Errorf("mirrored Light/Sirius row shipped %d times, want once: %v", mirrored, backends)
	}
}

func TestSourceTypeFor(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"hls", provider.SourceTypeHLS},
		{" HLS ", provider.SourceTypeHLS},
		{"m3u8", provider.SourceTypeHLS},
		{"", provider.SourceTypeHLS},
		{"mp4", provider.SourceTypeMP4},
		{"dash", ""},
		{"mkv", ""},
	}
	for _, tt := range tests {
		if got := sourceTypeFor(tt.raw); got != tt.want {
			t.Errorf("sourceTypeFor(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

// TestCinejoySkipsVideoOnlyTiers proves a master that keeps its audio in a
// separate EXT-X-MEDIA group publishes the muxed tier plus a master row named
// for the highest tier. The video-only tier URL is hidden because it would
// play the picture silently; the master carries both picture and sound.
func TestCinejoySkipsVideoOnlyTiers(t *testing.T) {
	const master = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="ENGLISH 5.1",DEFAULT=YES,URI="audio-en/playlist.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=23328000,RESOLUTION=3840x2160,AUDIO="audio"
2160p/playlist.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080
1080p/playlist.m3u8
`

	siteBase := "https://cinejoy.pk"
	mux := http.NewServeMux()
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != siteBase {
			http.Error(w, "missing origin", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(master))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := newTestClient(t, nil, srv.URL, siteBase)
	client.httpClient = cdnClient(srv)

	sources := client.streamSources(t.Context(), "Light", []gateStream{{
		Type:     "hls",
		Playlist: "http://cdn.cinejoy.test/master.m3u8",
		Captions: []gateCaption{{URL: "https://cdn.example.com/en.vtt", Language: "en"}},
	}})
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want the muxed 1080p tier and the master: %+v", len(sources), sources)
	}
	if sources[0].Quality != "1080p" {
		t.Errorf("sources[0].Quality = %q, want 1080p", sources[0].Quality)
	}
	if sources[1].Quality != "4K (2160p)" || sources[1].URL != "http://cdn.cinejoy.test/master.m3u8" {
		t.Errorf("sources[1] = %+v, want the 4K-labelled master row", sources[1])
	}
	for _, source := range sources {
		if strings.Contains(source.URL, "2160p") {
			t.Errorf("video-only rendition published as %q", source.URL)
		}
		if len(source.Subtitles) != 1 || source.Subtitles[0].Referer != siteBase+"/" {
			t.Errorf("%s subtitles = %+v, want one caption with the site referer", source.Quality, source.Subtitles)
		}
	}
}

func TestCinejoyFallbackLanes(t *testing.T) {
	serverKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/g", func(w http.ResponseWriter, r *http.Request) {
		request, session := openTestRequest(t, serverKey, readBody(t, r))
		response := fmt.Sprintf(`{"status":200,"data":{"stream":[{"type":"hls","playlist":%q}]}}`,
			srv.URL+request.Path+"/master.m3u8")
		_, _ = w.Write(sealTestResponse(t, session, []byte(response)))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080
https://cdn.example.com%s/1080p/index.m3u8
`, r.URL.Path)
	})

	client := newTestClient(t, serverKey.PublicKey(), srv.URL, "https://cinejoy.pk")
	sources, err := client.ResolveSource(t.Context(), "550", provider.Episode{TMDBID: 550})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != len(defaultLanes()) {
		t.Fatalf("sources = %d, want one per fallback lane (%d)", len(sources), len(defaultLanes()))
	}
}

// TestCredibleHeight proves an H.264 resolution claim that exceeds the codec
// level's real capability is clamped, while HEVC and unknown codecs keep the
// manifest's declared resolution.
func TestCredibleHeight(t *testing.T) {
	tests := []struct {
		name   string
		height int
		codecs string
		want   int
	}{
		{"hevc 4k kept", 2160, "hvc1.2.4.L153.B0,mp4a.40.2", 2160},
		{"hevc variant 4k kept", 2160, "hev1.2.4.L153.90,mp4a.40.2", 2160},
		{"avc level 3.2 clamped to 720", 2160, "avc1.4d4020,mp4a.40.2", 720},
		{"avc level 4.0 clamped to 1080", 2160, "avc1.4d4028,mp4a.40.2", 1080},
		{"avc level 5.1 kept", 2160, "avc1.640033,mp4a.40.2", 2160},
		{"avc3 level 3.1 clamped", 2160, "avc3.42c01f", 720},
		{"unknown codec kept", 2160, "", 2160},
		{"non-4k untouched", 1080, "avc1.4d4020", 1080},
	}
	for _, tt := range tests {
		if got := credibleHeight(tt.height, tt.codecs); got != tt.want {
			t.Errorf("credibleHeight(%d, %q) = %d, want %d", tt.height, tt.codecs, got, tt.want)
		}
	}
}

// TestCinejoyMasterNamedForRealResolution proves the playable master row of a
// separate-audio lane is labelled with its highest tier, so the real 4K source
// is visible instead of a vague "Auto" row.
func TestCinejoyMasterNamedForRealResolution(t *testing.T) {
	const master = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="ENGLISH 7.1",DEFAULT=YES,URI="audio/playlist.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=23328000,RESOLUTION=3840x2160,CODECS="hvc1.2.4.L153.B0,mp4a.40.2",AUDIO="audio"
2160p/playlist.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=8128000,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2",AUDIO="audio"
1080p/playlist.m3u8
`
	mux := http.NewServeMux()
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(master))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := newTestClient(t, nil, srv.URL, "https://cinejoy.pk")
	client.httpClient = cdnClient(srv)

	sources := client.streamSources(t.Context(), "Light", []gateStream{{
		Type:     "hls",
		Playlist: "http://cdn.cinejoy.test/master.m3u8",
	}})
	if len(sources) != 1 {
		t.Fatalf("sources = %d, want only the master row: %+v", len(sources), sources)
	}
	if sources[0].Quality != "4K (2160p)" {
		t.Errorf("master Quality = %q, want 4K (2160p)", sources[0].Quality)
	}
	if sources[0].URL != "http://cdn.cinejoy.test/master.m3u8" {
		t.Errorf("master URL = %q, want the master playlist", sources[0].URL)
	}
}

// TestCinejoyFalse4KClamped proves a lane claiming 4K with an impossible H.264
// level does not publish a 4K row; the real HEVC 4K lanes stay the only 4K.
func TestCinejoyFalse4KClamped(t *testing.T) {
	const master = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=16000000,RESOLUTION=3840x2160,CODECS="avc1.4d4020,mp4a.40.2"
/955557/2160p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080,CODECS="avc1.4d4020,mp4a.40.2"
/955557/1080p.m3u8
`
	mux := http.NewServeMux()
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(master))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := newTestClient(t, nil, srv.URL, "https://cinejoy.pk")
	client.httpClient = cdnClient(srv)

	sources := client.streamSources(t.Context(), "Sirius", []gateStream{{
		Type:     "hls",
		Playlist: "http://cdn.cinejoy.test/master.m3u8",
	}})
	for _, source := range sources {
		if source.Quality == "4K (2160p)" {
			t.Errorf("false 4K published as %q: %+v", source.URL, source)
		}
	}
}

func TestCaptionSubType(t *testing.T) {
	if got := captionSubType([]gateCaption{{URL: "https://cdn.example.com/en.vtt", Type: "vtt"}}); got != provider.SubTypeSoft {
		t.Errorf("typed caption = %q, want soft", got)
	}
	if got := captionSubType([]gateCaption{{URL: "https://cdn.example.com/en.vtt"}}); got != "" {
		t.Errorf("untyped caption = %q, want unknown", got)
	}
}

func TestResolutionLabel(t *testing.T) {
	tests := []struct {
		height int
		want   string
	}{
		{2160, "4K (2160p)"},
		{1440, "1440p"},
		{1080, "1080p"},
		{720, "720p"},
		{480, "480p"},
		{360, "360p"},
		{240, "240p"},
	}
	for _, tt := range tests {
		if got := resolutionLabel(tt.height); got != tt.want {
			t.Errorf("resolutionLabel(%d) = %q, want %q", tt.height, got, tt.want)
		}
	}
}

// cdnClient routes every host to the test server, so fixtures can use
// CDN-looking master URLs that survive the direct-URL filter while the probes
// still land on the local mux.
func cdnClient(srv *httptest.Server) *http.Client {
	addr := srv.Listener.Addr().String()
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
}

func newTestClient(t *testing.T, gate *ecdh.PublicKey, apiBase, siteBase string) *Client {
	t.Helper()
	client, err := newClientWithGate(tmdb.NewKeyPool([]string{"key"}), apiBase, siteBase, gate)
	if err != nil {
		t.Fatalf("newClientWithGate: %v", err)
	}
	return client
}

// openTestRequest performs the gateway side of the handshake: it recovers the
// plaintext payload and derives the response session.
func openTestRequest(t *testing.T, serverKey *ecdh.PrivateKey, body []byte) (gateRequest, gateSession) {
	t.Helper()
	if len(body) < gateHeaderSize+16 {
		t.Fatalf("sealed body too short: %d", len(body))
	}
	publicKey := body[2 : 2+65]
	nonce := body[2+65 : gateHeaderSize]
	sealed := body[gateHeaderSize:]

	ephemeral, err := ecdh.P256().NewPublicKey(publicKey)
	if err != nil {
		t.Fatalf("parse ephemeral key: %v", err)
	}
	shared, err := serverKey.ECDH(ephemeral)
	if err != nil {
		t.Fatalf("server ECDH: %v", err)
	}

	requestKey := hkdfSHA256(shared, publicKey, []byte(gateRequestInfo))
	responseKey := hkdfSHA256(shared, publicKey, []byte(gateResponseInfo))

	plaintext, err := openAESGCM(requestKey, nonce, sealed, gateAAD(publicKey, gateAADRequestType))
	if err != nil {
		t.Fatalf("open request: %v", err)
	}
	var request gateRequest
	if err := json.Unmarshal(plaintext, &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return request, gateSession{key: responseKey, aad: gateAAD(publicKey, gateAADResponseType)}
}

func sealTestResponse(t *testing.T, session gateSession, plaintext []byte) []byte {
	t.Helper()
	nonce := make([]byte, gateNonceSize)
	for i := range nonce {
		nonce[i] = byte(i + 1)
	}
	sealed, err := sealAESGCM(session.key, nonce, plaintext, session.aad)
	if err != nil {
		t.Fatalf("seal response: %v", err)
	}
	return append(nonce, sealed...)
}

func openAESGCM(key [32]byte, nonce, ciphertext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ciphertext, aad)
}

func readBody(t *testing.T, r *http.Request) []byte {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return body
}
