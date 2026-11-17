package kit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// BANDWIDTH must be read from its own attribute, not from the suffix of
// AVERAGE-BANDWIDTH, otherwise bitrate-based ranking picks the wrong rendition.
func TestHLSBandwidthSkipsAverageBandwidth(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080", "6000000"},
		{"#EXT-X-STREAM-INF:AVERAGE-BANDWIDTH=4500000,BANDWIDTH=6000000,RESOLUTION=1920x1080", "6000000"},
		{"#EXT-X-STREAM-INF:AVERAGE-BANDWIDTH=4500000,RESOLUTION=1920x1080", ""},
	}
	for _, tc := range cases {
		m := reHLSBandwidth.FindStringSubmatch(tc.line)
		got := ""
		if len(m) == 2 {
			got = m[1]
		}
		if got != tc.want {
			t.Errorf("bandwidth for %q = %q, want %q", tc.line, got, tc.want)
		}
	}
}

func TestProbeHLSAsset(t *testing.T) {
	const media = `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:1
#EXTINF:6.006,
seg00001.ts
#EXTINF:6.006,
seg00002.ts
#EXTINF:5.839,
seg00003.ts
#EXT-X-ENDLIST
`
	const otherMedia = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:10
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:10.010,
seg00001.ts
#EXTINF:10.010,
seg00002.ts
#EXT-X-ENDLIST
`
	const master = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080
1080/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=16000000,RESOLUTION=3840x2160
2160/index.m3u8
`
	const otherMaster = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=8000000,RESOLUTION=1920x1080
1080/index.m3u8
`

	mux := http.NewServeMux()
	serve := func(path, body string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(body))
		})
	}
	serve("/a.m3u8", media)
	serve("/b.m3u8", media)
	serve("/other.m3u8", otherMedia)
	serve("/master-a.m3u8", master)
	serve("/master-b.m3u8", master)
	serve("/master-other.m3u8", otherMaster)
	mux.HandleFunc("/html", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not a playlist</html>"))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ctx := context.Background()
	fingerprint := func(path string) string {
		return ProbeHLSAsset(ctx, srv.Client(), srv.URL+path, nil).Fingerprint
	}

	a := fingerprint("/a.m3u8")
	b := fingerprint("/b.m3u8")
	other := fingerprint("/other.m3u8")
	masterA := fingerprint("/master-a.m3u8")
	masterB := fingerprint("/master-b.m3u8")
	masterOther := fingerprint("/master-other.m3u8")

	if a == "" || b == "" || other == "" || masterA == "" || masterB == "" || masterOther == "" {
		t.Fatalf("fingerprints must not be empty: a=%q b=%q other=%q masterA=%q masterB=%q masterOther=%q",
			a, b, other, masterA, masterB, masterOther)
	}
	if a != b {
		t.Errorf("mirrored media playlists differ: %q vs %q", a, b)
	}
	if a == other {
		t.Errorf("different media playlists share fingerprint %q", a)
	}
	if masterA != masterB {
		t.Errorf("mirrored masters differ: %q vs %q", masterA, masterB)
	}
	if masterA == masterOther {
		t.Errorf("different rendition ladders share fingerprint %q", masterA)
	}
	if got := fingerprint("/html"); got != "" {
		t.Errorf("non-playlist body fingerprint = %q, want empty", got)
	}
	if got := fingerprint("/missing"); got != "" {
		t.Errorf("404 fingerprint = %q, want empty", got)
	}

	masterInfo := ProbeHLSAsset(ctx, srv.Client(), srv.URL+"/master-a.m3u8", nil)
	if masterInfo.Dimension != 3840 {
		t.Errorf("master dimension = %d, want 3840", masterInfo.Dimension)
	}
	otherMasterInfo := ProbeHLSAsset(ctx, srv.Client(), srv.URL+"/master-other.m3u8", nil)
	if otherMasterInfo.Dimension != 1920 {
		t.Errorf("other master dimension = %d, want 1920", otherMasterInfo.Dimension)
	}
	mediaInfo := ProbeHLSAsset(ctx, srv.Client(), srv.URL+"/a.m3u8", nil)
	if mediaInfo.Dimension != 0 {
		t.Errorf("media playlist dimension = %d, want 0", mediaInfo.Dimension)
	}
	if mediaInfo.Fingerprint == "" {
		t.Error("media playlist fingerprint must not be empty")
	}
	if htmlInfo := ProbeHLSAsset(ctx, srv.Client(), srv.URL+"/html", nil); htmlInfo.Dimension != 0 {
		t.Errorf("non-playlist dimension = %d, want 0", htmlInfo.Dimension)
	}
}

func TestSourceMetaFromVariants(t *testing.T) {
	cases := []struct {
		name        string
		variants    []HLSVariant
		wantHeight  int
		wantBitrate int
	}{
		{
			name: "empty ladder",
		},
		{
			name: "best credible height and peak bandwidth",
			variants: []HLSVariant{
				{Height: 2160, Bandwidth: 16_000_000, Codecs: "hvc1.2.4.L153.B0"},
				{Height: 1080, Bandwidth: 23_000_000},
				{Height: 720, Bandwidth: 2_000_000},
			},
			wantHeight:  2160,
			wantBitrate: 23_000_000,
		},
		{
			name: "low AVC level 4K claim is clamped",
			variants: []HLSVariant{
				{Height: 2160, Bandwidth: 30_000_000, Codecs: "avc1.4d4020,mp4a.40.2"},
				{Height: 1080, Bandwidth: 8_000_000, Codecs: "avc1.640028"},
			},
			wantHeight:  1080,
			wantBitrate: 30_000_000,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, b := SourceMetaFromVariants(tc.variants)
			if h != tc.wantHeight || b != tc.wantBitrate {
				t.Errorf("SourceMetaFromVariants() = (%d,%d), want (%d,%d)", h, b, tc.wantHeight, tc.wantBitrate)
			}
		})
	}
}

// TestDetectHLSQualityDelegatesToNormalizeQuality proves the detected label
// is exactly what the canonical parser produces for the ladder's best tier.
func TestDetectHLSQualityDelegatesToNormalizeQuality(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "4k master",
			body: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=16000000,RESOLUTION=3840x2160\n2160/index.m3u8\n",
			want: "2160p",
		},
		{
			name: "1080p master",
			body: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080\n1080/index.m3u8\n",
			want: "1080p",
		},
		{
			name: "360p master",
			body: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360\n360/index.m3u8\n",
			want: "360p",
		},
		{
			name: "media playlist has no rendition",
			body: "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.0,\nseg.ts\n",
			want: "auto",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)
			if got := DetectHLSQuality(context.Background(), srv.Client(), srv.URL+"/master.m3u8", nil); got != tc.want {
				t.Errorf("DetectHLSQuality() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseHLSVariants(t *testing.T) {
	const mixed = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=16000000,RESOLUTION=3840x2160
2160/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080
https://cdn.example.com/absolute/1080.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1200000
`
	const media = `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXTINF:6.006,
seg00001.ts
`

	mux := http.NewServeMux()
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(mixed))
	})
	mux.HandleFunc("/media.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(media))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ctx := context.Background()

	variants, err := ParseHLSVariants(ctx, srv.Client(), srv.URL+"/master.m3u8", map[string]string{"Referer": "https://site.example/"})
	if err != nil {
		t.Fatalf("ParseHLSVariants: %v", err)
	}
	if len(variants) != 2 {
		t.Fatalf("variants = %d, want 2 (a declared rendition without a URI is not one): %+v", len(variants), variants)
	}
	if want := srv.URL + "/2160/index.m3u8"; variants[0].URL != want {
		t.Errorf("variants[0].URL = %q, want %q", variants[0].URL, want)
	}
	if variants[0].Height != 2160 || variants[0].Bandwidth != 16000000 {
		t.Errorf("variants[0] = %+v, want 2160/16000000", variants[0])
	}
	if want := "https://cdn.example.com/absolute/1080.m3u8"; variants[1].URL != want {
		t.Errorf("variants[1].URL = %q, want absolute URL kept as-is", variants[1].URL)
	}

	empty, err := ParseHLSVariants(ctx, srv.Client(), srv.URL+"/media.m3u8", nil)
	if err != nil {
		t.Fatalf("ParseHLSVariants(media): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("media playlist variants = %+v, want none", empty)
	}

	if _, err := ParseHLSVariants(ctx, srv.Client(), srv.URL+"/missing.m3u8", nil); err == nil {
		t.Error("missing master must report an error so callers can fall back")
	}
}

// TestParseHLSVariantsAudioGroup pins the signal callers need to avoid
// publishing video-only renditions: a variant that names an EXT-X-MEDIA audio
// group has no audio of its own.
func TestParseHLSVariantsAudioGroup(t *testing.T) {
	const grouped = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="ENG",DEFAULT=YES,URI="audio/playlist.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=8000000,RESOLUTION=1920x1080,AUDIO="audio"
1080p/playlist.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=640x360
360p/playlist.m3u8
`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(grouped))
	}))
	t.Cleanup(srv.Close)

	variants, err := ParseHLSVariants(context.Background(), srv.Client(), srv.URL+"/grouped.m3u8", nil)
	if err != nil {
		t.Fatalf("ParseHLSVariants: %v", err)
	}
	if len(variants) != 2 {
		t.Fatalf("variants = %d, want 2", len(variants))
	}
	if variants[0].AudioGroup != "audio" {
		t.Errorf("variants[0].AudioGroup = %q, want audio", variants[0].AudioGroup)
	}
	if variants[1].AudioGroup != "" {
		t.Errorf("variants[1].AudioGroup = %q, want empty (muxed variant)", variants[1].AudioGroup)
	}
}
