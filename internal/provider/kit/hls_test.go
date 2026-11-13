package kit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
