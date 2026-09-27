package movysx

import (
	"encoding/base64"
	"strings"
	"testing"

	"kari/internal/provider/kit"
)

// TestDecryptPayloadJSVector proves the Go cipher port matches the JS
// implementation: the enc payload below was produced by temp/movy/server.mjs
// Df-inverse with seed "test-seed-abc123" and tmdbId 27205.
func TestDecryptPayloadJSVector(t *testing.T) {
	enc := "6LSGkSflK95z7jNMfGuX9N1gT4xzQgNdvJNEIihZg8roqZd_Soyh-VAszyawH2ZZl4hC816_btEOYQPkkcaaL3BF1x8HHBNG_Nxh9bFIX0dM9A-0mAjs5gXSchbdUViElXLti9ICGQp84njl5-AytajOukV-HepUeRuDAS8qiY5arkadPG_TNpbIvLozf3zZ6esxCi7N28U52xwrJSw"
	got, err := decryptPayload(enc, "test-seed-abc123", 27205)
	if err != nil {
		t.Fatalf("decryptPayload: %v", err)
	}
	want := `{"sources":[{"url":"https://cdn.example.com/master.m3u8","quality":"1080p"}],"subtitles":[{"url":"https://cdn.example.com/en.vtt","lang":"en","name":""}]}`
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

// encryptForTest mirrors the JS encrypt helper (magic prefix, XOR,
// base64url) using the ported keystream, for fixture generation.
func encryptForTest(t *testing.T, plain, seed string, tmdbID uint32) string {
	t.Helper()
	raw := append(append([]byte{}, cipherMagic...), []byte(plain)...)
	ks := keystream(seed, tmdbID, len(raw))
	for i := range raw {
		raw[i] ^= ks[i]
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func TestDecryptPayloadRoundTrip(t *testing.T) {
	seed := "another-seed-9"
	plain := `{"sources":[],"subtitles":[]}`
	enc := encryptForTest(t, plain, seed, 1396)
	got, err := decryptPayload(enc, seed, 1396)
	if err != nil {
		t.Fatalf("decryptPayload: %v", err)
	}
	if got != plain {
		t.Fatalf("got %q, want %q", got, plain)
	}
}

func TestDecryptPayloadRejects(t *testing.T) {
	if _, err := decryptPayload("", "s", 1); err == nil {
		t.Error("empty payload must error")
	}
	if _, err := decryptPayload("AAAA", "", 1); err == nil {
		t.Error("empty seed must error")
	}
	// Correct shape, wrong seed: magic check must fail.
	enc := encryptForTest(t, `{"a":1}`, "right-seed", 7)
	if _, err := decryptPayload(enc, "wrong-seed", 7); err == nil {
		t.Error("wrong seed must fail the magic check")
	}
}

func TestNormalizeQuality(t *testing.T) {
	cases := map[string]string{
		"1080p": "1080p", "FHD": "1080p", "4K": "2160p", "HD": "720p",
		"SD": "480p", "360p": "360p", "Auto": "auto", "weird": "auto",
	}
	for in, want := range cases {
		if got := normalizeQuality(in); got != want {
			t.Errorf("normalizeQuality(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDubLangOf(t *testing.T) {
	if got := kit.DubLangOf("Hindi"); got != "hi" {
		t.Errorf("kit.DubLangOf(Hindi) = %q, want hi", got)
	}
	if kit.DubLangOf("1080p") != "" || kit.DubLangOf("auto") != "" {
		t.Error("quality labels must not detect as dub langs")
	}
}

func TestIsSubtitleURL(t *testing.T) {
	if !kit.IsSubtitleURL("https://cdn.example.com/en.vtt") {
		t.Error("vtt must pass")
	}
	if kit.IsSubtitleURL("https://cdn.example.com/video.m3u8") {
		t.Error("video m3u8 must not pass as subtitle")
	}
	if kit.IsSubtitleURL("https://cdn.example.com/master.m3u8") {
		t.Error("master m3u8 must not pass as subtitle")
	}
}

func TestMergeProviderResults(t *testing.T) {
	payloads := []*upstreamPayload{
		{
			Sources: []upstreamSource{
				{URL: "https://cdn.example.com/lo.m3u8", Quality: "480p"},
				{URL: "https://cdn.example.com/hi.mp4", Quality: "1080p"},
				{URL: "https://cdn.example.com/dub-hi.m3u8", Quality: "Hindi"},
				{URL: "https://cdn.example.com/hi.mp4", Quality: "1080p"},
			},
			Subtitles: []upstreamSubtitle{
				{URL: "https://cdn.example.com/en.vtt", Lang: "en"},
				{URL: "https://cdn.example.com/video.m3u8", Lang: "en"},
			},
		},
	}
	m := mergeProviderResults(payloads)
	if len(m.sources) != 2 {
		t.Fatalf("want 2 deduped sources, got %+v", m.sources)
	}
	if m.sources[0].quality != "1080p" || !strings.HasSuffix(m.sources[0].url, "hi.mp4") {
		t.Errorf("best-first broken: %+v", m.sources[0])
	}
	if len(m.audio) != 1 || m.audio[0].lang != "hi" {
		t.Errorf("dub row must become hi audio: %+v", m.audio)
	}
	if len(m.subtitles) != 1 {
		t.Errorf("video m3u8 must be filtered from subtitles: %+v", m.subtitles)
	}
}
