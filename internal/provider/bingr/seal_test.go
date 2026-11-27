package bingr

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"

	"kari/internal/config"
	"kari/internal/provider"
)

// sealJSONForTest produces the same v1 payload Bingr embeds in a watch page.
// The stream cipher is symmetric, so sealing reuses unscrambleSeal.
func sealJSONForTest(t *testing.T, payload any) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("nonce: %v", err)
	}
	body := make([]byte, len(nonce)+len(raw))
	copy(body, nonce)
	copy(body[len(nonce):], raw)
	sealed := unscrambleSeal(body[len(nonce):], nonce)
	copy(body[len(nonce):], sealed)
	return sealPrefix + base64.RawURLEncoding.EncodeToString(body)
}

func TestDecodeSealedSources(t *testing.T) {
	want := sealedSources{
		Provider: sealedProvider{ID: "prism", Label: "Prism", Tag: "MULTI", HEVC: true},
		Streams: []sealedStream{{
			URL:      "https://cdn.example.com/movie/550/index.m3u8",
			Type:     "hls",
			Quality:  "1080p",
			Language: "Original",
			Subtitles: []sealedSubtitle{{
				URL:   "https://cdn.example.com/subs/en.vtt",
				Lang:  "en",
				Label: "English",
			}},
		}},
		Servers: []sealedServer{{ID: "prism", Label: "Prism", Tag: "MULTI"}},
	}

	got, err := decodeSealedSources(sealJSONForTest(t, want))
	if err != nil {
		t.Fatalf("decodeSealedSources: %v", err)
	}
	if got.Provider.Label != want.Provider.Label {
		t.Errorf("provider label = %q, want %q", got.Provider.Label, want.Provider.Label)
	}
	if len(got.Streams) != 1 || got.Streams[0].URL != want.Streams[0].URL {
		t.Fatalf("streams = %+v, want %+v", got.Streams, want.Streams)
	}
	if len(got.Streams[0].Subtitles) != 1 || got.Streams[0].Subtitles[0].Lang != "en" {
		t.Errorf("subtitles = %+v", got.Streams[0].Subtitles)
	}
}

func TestDecodeSealedSourcesRejectsGarbage(t *testing.T) {
	for _, raw := range []string{"", "not-a-seal", "v1.%%%", "v1.AQID"} {
		if _, err := decodeSealedSources(raw); err == nil {
			t.Errorf("decodeSealedSources(%q) succeeded, want error", raw)
		}
	}
}

func TestMapSealedSources(t *testing.T) {
	payload := &sealedSources{
		Provider: sealedProvider{ID: "prism", Label: "Prism"},
		Streams: []sealedStream{
			{
				URL:      "https://cdn.example.com/movie/550/1080.m3u8",
				Type:     "hls",
				Quality:  "1080p",
				Language: "Arabic sub",
				Subtitles: []sealedSubtitle{
					{URL: "https://cdn.example.com/subs/en.vtt", Lang: "en", Label: "English"},
					{URL: "https://cdn.example.com/subs/en.vtt", Lang: "en", Label: "English"},
					{URL: "", Lang: "fr", Label: "French"},
				},
			},
			{URL: "", Quality: "720p"},
		},
	}

	sources := mapSealedSources(payload, config.BingrSiteBase+"/")
	if len(sources) != 1 {
		t.Fatalf("mapSealedSources returned %d sources, want 1", len(sources))
	}
	s := sources[0]
	if s.Backend != "Prism" || s.Quality != "1080p" || s.Height != 1080 {
		t.Errorf("source metadata = %q/%q/%d", s.Backend, s.Quality, s.Height)
	}
	if s.Type != provider.SourceTypeHLS {
		t.Errorf("type = %q, want HLS", s.Type)
	}
	if len(s.Subtitles) != 1 || s.Subtitles[0].Language != "en" {
		t.Errorf("subtitles = %+v, want one deduped English track", s.Subtitles)
	}
	if s.Language != "" || len(s.Audio.Languages) != 0 {
		t.Errorf("stream language must stay unknown, got %q / %v", s.Language, s.Audio.Languages)
	}
	// The CDN hotlink-protects audio/subtitle sub-playlists, so playback must
	// carry the site referer and the master URL for rendition discovery.
	if s.Referer != config.BingrSiteBase+"/" {
		t.Errorf("referer = %q, want %q", s.Referer, config.BingrSiteBase+"/")
	}
	if s.ManifestURL != s.URL {
		t.Errorf("manifest URL = %q, want the playable master %q", s.ManifestURL, s.URL)
	}
}
