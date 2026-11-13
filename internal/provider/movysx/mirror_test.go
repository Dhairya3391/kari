package movysx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"kari/internal/provider/kit"
)

const collapseManifestA = `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:1
#EXTINF:6.006,
seg1.ts
#EXTINF:6.006,
seg2.ts
#EXT-X-ENDLIST
`

const collapseManifestB = `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-TARGETDURATION:8
#EXT-X-MEDIA-SEQUENCE:1
#EXTINF:8.008,
seg1.ts
#EXTINF:8.008,
seg2.ts
#EXT-X-ENDLIST
`

func TestCollapseMirrorsProbesAndDedupes(t *testing.T) {
	const dead = "/dead.m3u8"

	mux := http.NewServeMux()
	for path, body := range map[string]string{
		"/a.m3u8": collapseManifestA,
		"/b.m3u8": collapseManifestA,
		"/c.m3u8": collapseManifestB,
	} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Referer") != movyReferer {
				http.Error(w, "missing referer", http.StatusForbidden)
				return
			}
			if r.Header.Get("Origin") != "https://www.movy.sx" {
				http.Error(w, "missing origin", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(body))
		})
	}
	mux.HandleFunc(dead, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusInternalServerError)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := &Client{httpClient: srv.Client()}

	merged := &mergedResults{
		sources: []mergedSource{
			{url: srv.URL + "/a.m3u8", quality: "1080p", typ: "hls", backend: "Miami"},
			{url: srv.URL + "/b.m3u8", quality: "1080p", typ: "hls", backend: "Boise"},
			{url: srv.URL + "/c.m3u8", quality: "1080p", typ: "hls", backend: "Atlanta"},
			{url: srv.URL + dead, quality: "1080p", typ: "hls", backend: "Phoenix"},
		},
		audio: []mergedAudio{
			{url: srv.URL + "/a.m3u8", lang: "hi", typ: "hls", backend: "Miami"},
			{url: srv.URL + "/b.m3u8", lang: "hi", typ: "hls", backend: "Boise"},
			{url: srv.URL + dead, lang: "hi", typ: "hls", backend: "Phoenix"},
			{url: srv.URL + "/a.m3u8", lang: "en", typ: "hls", backend: "Miami"},
			{url: srv.URL + "/c.m3u8", lang: "en", typ: "hls", backend: "Atlanta"},
		},
	}

	got := c.collapseMirrors(context.Background(), merged)

	wantSourceBackends := []string{"Miami", "Atlanta", "Phoenix"}
	if len(got.sources) != len(wantSourceBackends) {
		t.Fatalf("sources = %+v, want %v", got.sources, wantSourceBackends)
	}
	for i, backend := range wantSourceBackends {
		if got.sources[i].backend != backend {
			t.Errorf("sources[%d].backend = %q, want %q", i, got.sources[i].backend, backend)
		}
	}

	wantAudio := []string{"hi:Miami", "hi:Phoenix", "en:Miami", "en:Atlanta"}
	if len(got.audio) != len(wantAudio) {
		t.Fatalf("audio = %+v, want %v", got.audio, wantAudio)
	}
	for i, want := range wantAudio {
		gotKey := got.audio[i].lang + ":" + got.audio[i].backend
		if gotKey != want {
			t.Errorf("audio[%d] = %q, want %q", i, gotKey, want)
		}
	}
}

func TestCollapseSourceCopiesKeepsUnverifiedRows(t *testing.T) {
	sources := []mergedSource{
		{url: "https://a/1080.m3u8", quality: "1080p", typ: "hls", backend: "Miami"},
		{url: "https://b/1080.m3u8", quality: "1080p", typ: "hls", backend: "Boise"},
		{url: "https://a/720.m3u8", quality: "720p", typ: "hls", backend: "Miami"},
		{url: "https://b/720.m3u8", quality: "720p", typ: "hls", backend: "Boise"},
		{url: "https://c/file.mp4", quality: "1080p", typ: "mp4", backend: "Atlanta"},
	}
	signatures := []string{"same", "same", "", "", ""}

	got := collapseSourceCopies(sources, signatures)
	want := []string{"Miami", "Miami", "Boise", "Atlanta"}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %v", got, want)
	}
	for i, backend := range want {
		if got[i].backend != backend {
			t.Errorf("got[%d].backend = %q, want %q", i, got[i].backend, backend)
		}
	}
}

func TestCollapseSourceCopiesSeparatesQualities(t *testing.T) {
	sources := []mergedSource{
		{url: "https://a/1080.m3u8", quality: "1080p", typ: "hls", backend: "Miami"},
		{url: "https://b/720.m3u8", quality: "720p", typ: "hls", backend: "Boise"},
	}
	got := collapseSourceCopies(sources, []string{"same", "same"})
	if len(got) != 2 {
		t.Fatalf("same manifest across different quality labels must stay separate: %+v", got)
	}
}

func TestCollapseMirrorsSkipsRowsThatCannotBeDuplicates(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(collapseManifestA))
	}))
	t.Cleanup(srv.Close)

	c := &Client{httpClient: srv.Client()}
	merged := &mergedResults{
		sources: []mergedSource{
			{url: srv.URL + "/1080.m3u8", quality: "1080p", typ: "hls", backend: "Miami"},
			{url: srv.URL + "/720.m3u8", quality: "720p", typ: "hls", backend: "Boise"},
			{url: srv.URL + "/auto.m3u8", quality: "auto", typ: "hls", backend: "Austin"},
		},
	}

	c.collapseMirrors(context.Background(), merged)

	// The two unique real tiers cannot collapse, so only the adaptive row is
	// worth a probe.
	if got := hits.Load(); got != 1 {
		t.Errorf("probed %d manifests, want only the adaptive row", got)
	}
	if len(merged.sources) != 3 {
		t.Errorf("kept %d sources, want 3", len(merged.sources))
	}
}

func TestQualityFromDimension(t *testing.T) {
	// dimension is the master's largest side (max of width/height), so the
	// inputs are the widths of the standard ladders, not their heights.
	cases := map[int]string{
		3840: "2160p", // 3840x2160
		2560: "1440p", // 2560x1440
		1920: "1080p", // 1920x1080 and letterboxed 1920x800
		1280: "720p",  // 1280x720
		854:  "480p",  // 854x480
		640:  "360p",  // 640x360
		0:    "",
	}
	for dimension, want := range cases {
		if got := qualityFromDimension(dimension); got != want {
			t.Errorf("qualityFromDimension(%d) = %q, want %q", dimension, got, want)
		}
	}
}

func TestSortMergedSourcesOrdersEveryTier(t *testing.T) {
	sources := []mergedSource{
		{quality: "auto", backend: "Miami", url: "https://cdn.example.com/auto.m3u8"},
		{quality: "360p", backend: "Miami", url: "https://cdn.example.com/360.m3u8"},
		{quality: "576p", backend: "Miami", url: "https://cdn.example.com/576.m3u8"},
		{quality: "480p", backend: "Miami", url: "https://cdn.example.com/480.m3u8"},
		{quality: "720p", backend: "Miami", url: "https://cdn.example.com/720.m3u8"},
		{quality: "1080p", backend: "Miami", url: "https://cdn.example.com/1080.m3u8"},
		{quality: "1440p", backend: "Miami", url: "https://cdn.example.com/1440.m3u8"},
		{quality: "2160p", backend: "Miami", url: "https://cdn.example.com/2160.m3u8"},
	}

	sortMergedSources(sources)

	want := []string{"2160p", "1440p", "1080p", "720p", "576p", "480p", "360p", "auto"}
	for i, quality := range want {
		if sources[i].quality != quality {
			t.Errorf("sources[%d].quality = %q, want %q", i, sources[i].quality, quality)
		}
	}
}

func TestApplyProbedQualityOnlyOverridesKnownDimensions(t *testing.T) {
	sources := []mergedSource{
		{quality: "auto"},
		{quality: "720p"},
	}
	audio := []mergedAudio{
		{quality: "auto"},
		{quality: "auto"},
	}
	probes := []kit.HLSAssetInfo{
		{Dimension: 3840}, // 3840x2160 master
		{Dimension: 0},    // media playlist, no resolution metadata
		{Dimension: 1920}, // 1920x1080 master
		{Dimension: 0},
	}

	applyProbedQuality(sources, audio, probes)

	if sources[0].quality != "2160p" {
		t.Errorf("source with 4K master = %q, want 2160p", sources[0].quality)
	}
	if sources[1].quality != "720p" {
		t.Errorf("media playlist label = %q, want 720p unchanged", sources[1].quality)
	}
	if audio[0].quality != "1080p" {
		t.Errorf("audio with FHD master = %q, want 1080p", audio[0].quality)
	}
	if audio[1].quality != "auto" {
		t.Errorf("audio without a dimension = %q, want auto unchanged", audio[1].quality)
	}
}
