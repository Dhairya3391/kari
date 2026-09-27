package kit

import "testing"

func TestParseHandle(t *testing.T) {
	tests := []struct {
		name           string
		mediaID, epID  string
		audio          string
		episode        int
		requireSegment string
		want           Handle
	}{
		{
			name:    "full five-part route honors explicit audio",
			mediaID: "1", epID: "watch/hd1/123/sub/4", audio: "dub", episode: 9,
			want: Handle{AniListID: "123", Category: "dub", Number: 4},
		},
		{
			name:    "five-part with required segment",
			mediaID: "1", epID: "watch/anilight/123/dub/7", requireSegment: "anilight",
			want: Handle{AniListID: "123", Category: "dub", Number: 7},
		},
		{
			name:    "five-part rejected when segment differs",
			mediaID: "1", epID: "watch/other/123/dub/7", requireSegment: "anilight", episode: 5,
			want: Handle{AniListID: "1", Category: "sub", Number: 5},
		},
		{
			name:    "four-part route",
			mediaID: "1", epID: "watch/123/sub/2", episode: 8,
			want: Handle{AniListID: "123", Category: "sub", Number: 2},
		},
		{
			name:    "three-part route honors explicit audio",
			mediaID: "1", epID: "123/sub/3", audio: "dub",
			want: Handle{AniListID: "123", Category: "dub", Number: 3},
		},
		{
			name:    "no route falls back to inputs",
			mediaID: "42", epID: "e42", audio: "DUB", episode: 6,
			want: Handle{AniListID: "42", Category: "dub", Number: 6},
		},
		{
			name:    "empty audio defaults to sub",
			mediaID: "42", epID: "e1",
			want: Handle{AniListID: "42", Category: "sub", Number: 0},
		},
		{
			name:    "non-positive episode number keeps fallback",
			mediaID: "42", epID: "42/sub/0", episode: 2,
			want: Handle{AniListID: "42", Category: "sub", Number: 2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseHandle(tt.mediaID, tt.epID, tt.audio, tt.episode, tt.requireSegment)
			if got != tt.want {
				t.Errorf("ParseHandle = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestHeadersExtraction(t *testing.T) {
	st := Stream{
		Referer:     "",
		Headers:     map[string]string{"User-Agent": "map-ua"},
		HTTPHeaders: map[string]string{"Referer": "map-referer"},
		MPVArgs:     []string{"--referrer=arg-referer", "--user-agent=arg-ua", "--force-seekable=yes"},
	}
	referer, ua, extra := Headers(st, ArgPolicy{})
	if referer != "map-referer" {
		t.Errorf("referer = %q, want map-referer", referer)
	}
	if ua != "map-ua" {
		t.Errorf("user agent = %q, want map-ua", ua)
	}
	if len(extra) != 1 || extra[0] != "--force-seekable=yes" {
		t.Errorf("extra args = %v", extra)
	}
}

func TestHeadersRefererFieldWins(t *testing.T) {
	st := Stream{
		Referer: "field",
		Headers: map[string]string{"Referer": "map"},
		MPVArgs: []string{"--referrer=arg"},
	}
	referer, _, _ := Headers(st, ArgPolicy{})
	if referer != "field" {
		t.Errorf("referer = %q, want field", referer)
	}
}

func TestHeadersReAnimePolicy(t *testing.T) {
	st := Stream{
		MPVArgs: []string{
			`"--http-header-fields=User-Agent: X"`,
			"https://stream.example/master.m3u8",
			"--demuxer-lavf-o=reconnect=1",
		},
	}
	_, _, extra := Headers(st, ArgPolicy{KeepHeaderFieldArgs: true, SkipURLArgs: true, StripQuotes: true})
	want := []string{"--http-header-fields=User-Agent: X", "--demuxer-lavf-o=reconnect=1"}
	if len(extra) != len(want) {
		t.Fatalf("extra args = %v, want %v", extra, want)
	}
	for i := range want {
		if extra[i] != want[i] {
			t.Errorf("extra[%d] = %q, want %q", i, extra[i], want[i])
		}
	}
}

func TestSubtitles(t *testing.T) {
	got := Subtitles([]Subtitle{
		{URL: "https://x/en.vtt", Lang: "eng", Default: true},
		{URL: "https://x/en.vtt", Lang: "en"},           // duplicate URL dropped
		{URL: "https://x/th.vtt", Kind: "thumbnails"},   // preview dropped
		{File: "https://x/legacy.vtt", Label: "French"}, // legacy File field, label fallback
		{URL: "   "}, // empty dropped
	})
	if len(got) != 2 {
		t.Fatalf("got %d subtitles, want 2: %+v", len(got), got)
	}
	if got[0].URL != "https://x/en.vtt" || got[0].Language != "en" || !got[0].Default {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].Language != "fr" {
		t.Errorf("legacy track language = %q, want fr", got[1].Language)
	}
}

func TestQualityLabels(t *testing.T) {
	if got := AutoQuality(""); got != "Auto" {
		t.Errorf("AutoQuality(\"\") = %q", got)
	}
	if got := AutoQuality("AUTO"); got != "Auto" {
		t.Errorf("AutoQuality(AUTO) = %q", got)
	}
	if got := AutoQuality("1080p"); got != "1080p" {
		t.Errorf("AutoQuality(1080p) = %q", got)
	}
	if got := QualityLabel("", "hls"); got != "Auto" {
		t.Errorf("QualityLabel(\"\", hls) = %q", got)
	}
	if got := QualityLabel("", "embed"); got != "Embed" {
		t.Errorf("QualityLabel(\"\", embed) = %q", got)
	}
	if got := QualityLabel("", "mp4"); got != "Direct" {
		t.Errorf("QualityLabel(\"\", mp4) = %q", got)
	}
	if got := QualityLabel("720p", "hls"); got != "720p" {
		t.Errorf("QualityLabel(720p, hls) = %q", got)
	}
}

func TestTagQuality(t *testing.T) {
	if got := TagQuality("Auto", "HD1", ""); got != "Auto (HD1)" {
		t.Errorf("TagQuality = %q", got)
	}
	if got := TagQuality("Auto", "", "hd2"); got != "Auto (hd2)" {
		t.Errorf("TagQuality fallback = %q", got)
	}
	if got := TagQuality("Auto", "", ""); got != "Auto" {
		t.Errorf("TagQuality untagged = %q", got)
	}
}

func TestWatchURLs(t *testing.T) {
	got := WatchURLs("https://api", "12 34", "sub", 2, []string{"hd1", "hd2"})
	want := []string{
		"https://api/watch/hd1/12%2034/sub/2",
		"https://api/watch/hd2/12%2034/sub/2",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d urls, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("url[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
