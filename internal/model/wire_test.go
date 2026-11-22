package model

import (
	"testing"
)

func TestKindSpecTable(t *testing.T) {
	// Tab order follows the visible mode strip, with Jellyfin last when shown.
	wantTabs := map[Kind]int{
		KindAnime: 0, KindCartoon: 1, KindStremio: 2, KindLive: 3, KindManga: 4,
		KindMovie: 5, KindTV: 6, KindJellyfin: 7,
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
	for _, k := range []Kind{KindAnime, KindCartoon, KindStremio, KindJellyfin, KindTV, KindManga} {
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
	for _, k := range []Kind{KindAnime, KindCartoon, KindStremio, KindJellyfin, KindLive, KindManga, KindMovie, KindTV} {
		if got := FromKey(k.Key()); got != k {
			t.Errorf("FromKey(%q) = %d, want %d", k.Key(), got, k)
		}
	}
	if got := FromKey("nope"); got != KindUnknown {
		t.Errorf("FromKey(unknown) = %d, want Unknown", got)
	}
}
