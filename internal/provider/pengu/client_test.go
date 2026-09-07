package pengu

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	if parsed["res_1080"] != "on" {
		t.Errorf("res_1080 = %v, want on", parsed["res_1080"])
	}
	// VidKing and 2Peckle must NOT be requested from Pengu
	if parsed["source_vidking"] == "on" {
		t.Errorf("source_vidking should not be enabled in Pengu config")
	}
	if parsed["source_2peckle"] == "on" {
		t.Errorf("source_2peckle should not be enabled in Pengu config")
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

func TestIsExcludedStream(t *testing.T) {
	vkStream := penguStreamItem{
		Name:        "🐧 PenguPlay 1080p • VidKing",
		Description: "Source: VidKing",
		URL:         "https://pengu.uk/hls/vidking/stream.m3u8",
	}
	if !isExcludedStream(vkStream) {
		t.Errorf("isExcludedStream want true for VidKing stream")
	}

	twoPeckleStream := penguStreamItem{
		Name:        "🐧 PenguPlay 1080p • 2Peckle",
		Description: "Source: 2Peckle",
		URL:         "https://pengu.uk/direct/external/stream.mp4",
	}
	if !isExcludedStream(twoPeckleStream) {
		t.Errorf("isExcludedStream want true for 2Peckle stream")
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

		_, err := c.fetchPenguStreams(context.Background(), provider.MediaTypeMovie, "tmdb:550")
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

		_, err := c.fetchPenguStreams(context.Background(), provider.MediaTypeMovie, "tmdb:550")
		if !errors.Is(err, provider.ErrRateLimited) {
			t.Fatalf("fetchPenguStreams want ErrRateLimited, got %v", err)
		}
	})
}
