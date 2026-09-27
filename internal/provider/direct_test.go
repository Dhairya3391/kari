package provider

import "testing"

func TestIsDirectURL(t *testing.T) {
	direct := []string{
		"https://cdn.example.com/master.m3u8?token=abc",
		"https://fetch.nexabloom.top/anime/a/b/master.m3u8",
		"http://cdn.example.com/movie.mp4",
	}
	for _, u := range direct {
		if !IsDirectURL(u) {
			t.Errorf("IsDirectURL(%q) = false, want true", u)
		}
	}
	proxied := []string{
		"",
		"not-a-url",
		"http://localhost:8080/proxy/m3u8?url=x",
		"http://127.0.0.1:3000/master.m3u8",
		"https://example.com/proxy/m3u8?url=x",
		"https://player.local/stream.m3u8",
		"ftp://cdn.example.com/movie.mkv",
	}
	for _, u := range proxied {
		if IsDirectURL(u) {
			t.Errorf("IsDirectURL(%q) = true, want false", u)
		}
	}
}

func TestFilterDirectSources(t *testing.T) {
	in := []MediaSource{
		{URL: "https://cdn.example.com/a.m3u8"},
		{URL: "http://localhost:8080/proxy/m3u8"},
		{URL: "https://cdn.example.com/b.mp4"},
	}
	got := FilterDirectSources(in)
	if len(got) != 2 || got[0].URL != in[0].URL || got[1].URL != in[2].URL {
		t.Errorf("FilterDirectSources = %+v, want first and third kept", got)
	}
}
