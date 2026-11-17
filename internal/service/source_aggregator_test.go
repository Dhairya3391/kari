package service

import (
	"reflect"
	"testing"

	"kari/internal/provider"
)

// TestSourceAggregatorOwnsBatches guards both caller order and nested storage
// against sorting, duplicate merging, and later provider mutations.
func TestSourceAggregatorOwnsBatches(t *testing.T) {
	agg := newSourceAggregator(nil, func(name string) string { return name })
	batch := []provider.MediaSource{
		{URL: "https://cdn.example/soft.mp4", SubType: provider.SubTypeSoft},
		{
			URL: "https://cdn.example/hard.mp4", SubType: provider.SubTypeHard,
			ExtraArgs: []string{"--original"},
			Subtitles: []provider.SubtitleOption{{URL: "https://cdn.example/en.vtt", Language: "en"}},
			SkipTimes: &provider.SkipTimes{OpEnd: 90},
		},
	}
	before := cloneMediaSources(batch)
	if !agg.add("stream", batch) {
		t.Fatal("initial batch did not change aggregate")
	}
	if !reflect.DeepEqual(batch, before) {
		t.Fatal("aggregation sorted or mutated caller-owned batch")
	}
	if agg.sources[0].URL != batch[0].URL {
		t.Fatal("arrival order was not preserved")
	}
	batch[1].ExtraArgs[0] = "changed"
	batch[1].Subtitles[0].URL = "changed"
	batch[1].SkipTimes.OpEnd = 1
	if agg.sources[1].ExtraArgs[0] != "--original" ||
		agg.sources[1].Subtitles[0].URL != "https://cdn.example/en.vtt" ||
		agg.sources[1].SkipTimes.OpEnd != 90 {
		t.Fatal("aggregate aliases nested provider data")
	}
}

// TestSourceAggregatorReplacesProviderBatch proves late enrichment can drop a
// shadowed row that an earlier progressive snapshot already added, and that an
// empty batch is a no-op rather than a removal of previously published rows.
func TestSourceAggregatorReplacesProviderBatch(t *testing.T) {
	agg := newSourceAggregator(nil, func(name string) string { return name })
	full := []provider.MediaSource{
		{URL: "https://cdn.example/master.m3u8", Quality: "1080p"},
		{URL: "https://cdn.example/tier-1080.m3u8", Quality: "1080p"},
	}
	if !agg.add("stream", full) {
		t.Fatal("initial batch did not change aggregate")
	}
	if len(agg.sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(agg.sources))
	}

	kept := full[:1]
	if !agg.replaceProviderSources("stream", kept) {
		t.Fatal("replacement did not change aggregate")
	}
	if len(agg.sources) != 1 || agg.sources[0].URL != kept[0].URL {
		t.Fatalf("sources after replacement = %+v, want only master", agg.sources)
	}

	if agg.replaceProviderSources("stream", []provider.MediaSource{{URL: ""}}) {
		t.Fatal("empty batch should be a no-op")
	}
	if len(agg.sources) != 1 {
		t.Fatalf("sources after no-op = %d, want 1", len(agg.sources))
	}
}

// TestSourceAggregatorReportsChanges distinguishes no-op batches from metadata
// improvements, preferred-provider replacement, and newly discovered subtitles.
func TestSourceAggregatorReportsChanges(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		batch    []provider.MediaSource
		changed  bool
	}{
		{name: "empty"},
		{name: "invalid", provider: "fallback", batch: []provider.MediaSource{{URL: ""}}},
		{name: "initial", provider: "fallback", changed: true, batch: []provider.MediaSource{{URL: "https://cdn.example/video.mp4"}}},
		{name: "duplicate", provider: "fallback", batch: []provider.MediaSource{{URL: "https://cdn.example/video.mp4"}}},
		{name: "quality improvement", provider: "fallback", changed: true, batch: []provider.MediaSource{{URL: "https://cdn.example/video.mp4", Quality: "1080p"}}},
		{name: "inferior quality", provider: "fallback", batch: []provider.MediaSource{{URL: "https://cdn.example/video.mp4", Quality: "720p"}}},
		{name: "preferred provider", provider: "primary", changed: true, batch: []provider.MediaSource{{URL: "https://cdn.example/video.mp4"}}},
		{name: "fallback duplicate", provider: "fallback", batch: []provider.MediaSource{{URL: "https://cdn.example/video.mp4"}}},
		{
			name: "new subtitle", provider: "fallback", changed: true,
			batch: []provider.MediaSource{{URL: "https://cdn.example/video.mp4", Subtitles: []provider.SubtitleOption{{URL: "https://cdn.example/en.vtt", Language: "en"}}}},
		},
		{
			name: "duplicate subtitle", provider: "fallback",
			batch: []provider.MediaSource{{URL: "https://cdn.example/video.mp4", Subtitles: []provider.SubtitleOption{{URL: "https://cdn.example/en.vtt", Language: "en"}}}},
		},
	}
	agg := newSourceAggregator([]provider.Provider{
		&stubProvider{name: "primary"}, &stubProvider{name: "fallback"},
	}, func(name string) string { return name })
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if changed := agg.add(tc.provider, tc.batch); changed != tc.changed {
				t.Fatalf("changed = %v, want %v", changed, tc.changed)
			}
			agg.sort()
		})
	}
	if len(agg.sources) != 1 || agg.sources[0].Resolver != "primary" || agg.sources[0].Quality != "1080p" {
		t.Fatalf("priority or metadata lost: %+v", agg.sources)
	}
	if len(agg.subs) != 1 || agg.subs[0].Resolver != "primary" {
		t.Fatalf("subtitle ownership lost: %+v", agg.subs)
	}
}
