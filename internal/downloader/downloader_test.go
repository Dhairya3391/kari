package downloader

// Pins header derivation for downloads: fields come from
// media.Source.Headers via toMediaSource, emitted in deterministic order.

import (
	"testing"

	"kari/internal/provider"
)

func TestSourceHeaders(t *testing.T) {
	got := sourceHeaders(provider.MediaSource{
		URL: "https://cdn.example.com/x.m3u8", UserAgent: "UA",
		Referer: "https://megaplay.buzz/watch/1", CookieHeader: "a=b",
	})
	want := []string{
		"User-Agent: UA",
		"Referer: https://megaplay.buzz/watch/1",
		"Origin: https://megaplay.buzz",
		"Cookie: a=b",
	}
	if len(got) != len(want) {
		t.Fatalf("sourceHeaders = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sourceHeaders[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSourceHeadersSuppressOrigin(t *testing.T) {
	got := sourceHeaders(provider.MediaSource{Referer: "https://megaplay.buzz/watch/1", SuppressOrigin: true})
	for _, h := range got {
		if len(h) >= 6 && h[:6] == "Origin" {
			t.Errorf("SuppressOrigin must drop Origin, got %v", got)
		}
	}
}

func TestSourceHeadersEmpty(t *testing.T) {
	if got := sourceHeaders(provider.MediaSource{URL: "https://cdn.example.com/x.m3u8"}); len(got) != 0 {
		t.Errorf("empty source must yield no headers, got %v", got)
	}
}
