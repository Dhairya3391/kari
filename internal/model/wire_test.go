package model

import (
	"testing"
)

func TestKindSpecTable(t *testing.T) {
	// Tab order follows the spec strip: anime cartoon live manga movies tv,
	// jellyfin last when shown.
	wantTabs := map[Kind]int{
		KindAnime: 0, KindCartoon: 1, KindLive: 2, KindManga: 3,
		KindMovie: 4, KindTV: 5, KindJellyfin: 6,
	}
	for k, want := range wantTabs {
		s := k.Spec()
		if s == nil {
			t.Fatalf("Spec(%d) = nil", k)
		}
		if s.Tab != want {
			t.Errorf("Spec(%s).Tab = %d, want %d", s.Key, s.Tab, want)
		}
	}
	if KindUnknown.Spec() != nil {
		t.Errorf("Spec(Unknown) should be nil")
	}
}

func TestKindSpecFlows(t *testing.T) {
	if !KindManga.Spec().Reader {
		t.Errorf("manga must be a Reader")
	}
	if !KindLive.Spec().Live {
		t.Errorf("live must set Live")
	}
	for _, k := range []Kind{KindAnime, KindCartoon, KindJellyfin, KindTV, KindManga} {
		if !k.Spec().HasItems {
			t.Errorf("%s must have items", k.Spec().Key)
		}
	}
	for _, k := range []Kind{KindMovie, KindLive} {
		if k.Spec().HasItems {
			t.Errorf("%s must go directly to Preview", k.Spec().Key)
		}
	}
	if KindManga.Spec().ItemNoun != "chapter" {
		t.Errorf("manga noun must be chapter")
	}
}

func TestFromKeyRoundTrip(t *testing.T) {
	for _, k := range []Kind{KindAnime, KindCartoon, KindJellyfin, KindLive, KindManga, KindMovie, KindTV} {
		if got := FromKey(k.Key()); got != k {
			t.Errorf("FromKey(%q) = %d, want %d", k.Key(), got, k)
		}
	}
	if got := FromKey("nope"); got != KindUnknown {
		t.Errorf("FromKey(unknown) = %d, want Unknown", got)
	}
}

func TestSourceHeaders(t *testing.T) {
	s := Source{URL: "https://cdn.example.com/x.m3u8", Referer: "https://megaplay.buzz/watch/1", UserAgent: "UA", Cookie: "a=b"}
	h := s.Headers()
	if h.Get("User-Agent") != "UA" {
		t.Errorf("missing User-Agent: %v", h)
	}
	if h.Get("Referer") != "https://megaplay.buzz/watch/1" {
		t.Errorf("missing Referer: %v", h)
	}
	if h.Get("Origin") != "https://megaplay.buzz" {
		t.Errorf("Origin must be scheme://host, got %q", h.Get("Origin"))
	}
	if h.Get("Cookie") != "a=b" {
		t.Errorf("missing Cookie: %v", h)
	}

	suppressed := Source{Referer: "https://megaplay.buzz/watch/1", SuppressOrigin: true}
	if got := suppressed.Headers().Get("Origin"); got != "" {
		t.Errorf("SuppressOrigin must drop Origin, got %q", got)
	}

	empty := Source{URL: "https://cdn.example.com/x.m3u8"}
	if len(empty.Headers()) != 0 {
		t.Errorf("empty source must yield no headers, got %v", empty.Headers())
	}
}
