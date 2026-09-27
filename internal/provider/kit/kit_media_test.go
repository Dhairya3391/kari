package kit

import "testing"

func TestDubLangOf(t *testing.T) {
	if got := DubLangOf("Hindi"); got != "hi" {
		t.Errorf("DubLangOf(Hindi) = %q, want hi", got)
	}
	if DubLangOf("1080p") != "" || DubLangOf("auto") != "" {
		t.Error("quality labels must not detect as dub langs")
	}
}

func TestIsSubtitleURL(t *testing.T) {
	if !IsSubtitleURL("https://cdn.example.com/en.vtt") {
		t.Error("vtt must pass")
	}
	if IsSubtitleURL("https://cdn.example.com/video.m3u8") {
		t.Error("video m3u8 must not pass as subtitle")
	}
	if IsSubtitleURL("https://cdn.example.com/master.m3u8") {
		t.Error("master m3u8 must not pass as subtitle")
	}
}

func TestIsMp4URL(t *testing.T) {
	if !IsMp4URL("https://cdn.example.com/movie.mp4?token=x") {
		t.Error("mp4 with query must pass")
	}
	if IsMp4URL("https://cdn.example.com/master.m3u8") {
		t.Error("m3u8 must not pass as mp4")
	}
}
