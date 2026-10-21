package animeskip

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestParseIntervals(t *testing.T) {
	tests := []struct {
		name       string
		timestamps []timestamp
		wantOp     [2]float64
		wantEd     [2]float64
		wantRecap  [2]float64
		wantPrev   [2]float64
	}{
		{
			name: "full timeline with intro, canon, credits, preview",
			timestamps: []timestamp{
				{At: 0, Type: timestampType{Name: "Canon"}},
				{At: 93.0, Type: timestampType{Name: "Intro"}},
				{At: 183.0, Type: timestampType{Name: "Canon"}},
				{At: 1340.0, Type: timestampType{Name: "Credits"}},
				{At: 1430.0, Type: timestampType{Name: "Preview"}},
			},
			wantOp:    [2]float64{93.0, 183.0},
			wantEd:    [2]float64{1340.0, 1430.0},
			wantRecap: [2]float64{-1, -1},
			wantPrev:  [2]float64{-1, -1},
		},
		{
			name: "timeline with recap and new intro",
			timestamps: []timestamp{
				{At: 0, Type: timestampType{Name: "Recap"}},
				{At: 45.0, Type: timestampType{Name: "Canon"}},
				{At: 100.0, Type: timestampType{Name: "New Intro"}},
				{At: 190.0, Type: timestampType{Name: "Canon"}},
				{At: 1300.0, Type: timestampType{Name: "New Credits"}},
			},
			wantOp:    [2]float64{100.0, 190.0},
			wantEd:    [2]float64{-1, -1}, // final marker has no confirmed end boundary
			wantRecap: [2]float64{0, 45.0},
			wantPrev:  [2]float64{-1, -1},
		},
		{
			name: "canon only returns nil",
			timestamps: []timestamp{
				{At: 0, Type: timestampType{Name: "Canon"}},
				{At: 1400.0, Type: timestampType{Name: "Canon"}},
			},
			wantOp:    [2]float64{-1, -1},
			wantEd:    [2]float64{-1, -1},
			wantRecap: [2]float64{-1, -1},
			wantPrev:  [2]float64{-1, -1},
		},
		{
			name: "unclosed opening is ignored",
			timestamps: []timestamp{
				{At: 90, Type: timestampType{Name: "Intro"}},
			},
			wantOp:    [2]float64{-1, -1},
			wantEd:    [2]float64{-1, -1},
			wantRecap: [2]float64{-1, -1},
			wantPrev:  [2]float64{-1, -1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseIntervals(tc.timestamps)
			if tc.wantOp[0] < 0 && tc.wantEd[0] < 0 && tc.wantRecap[0] < 0 && tc.wantPrev[0] < 0 {
				if got != nil {
					t.Fatalf("expected nil SkipTimes, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected non-nil SkipTimes, got nil")
			}
			if got.OpStart != tc.wantOp[0] || got.OpEnd != tc.wantOp[1] {
				t.Errorf("Op mismatch: got [%v, %v], want [%v, %v]", got.OpStart, got.OpEnd, tc.wantOp[0], tc.wantOp[1])
			}
			if got.EdStart != tc.wantEd[0] || got.EdEnd != tc.wantEd[1] {
				t.Errorf("Ed mismatch: got [%v, %v], want [%v, %v]", got.EdStart, got.EdEnd, tc.wantEd[0], tc.wantEd[1])
			}
			if got.RecapStart != tc.wantRecap[0] || got.RecapEnd != tc.wantRecap[1] {
				t.Errorf("Recap mismatch: got [%v, %v], want [%v, %v]", got.RecapStart, got.RecapEnd, tc.wantRecap[0], tc.wantRecap[1])
			}
			if got.PreviewStart != tc.wantPrev[0] || got.PreviewEnd != tc.wantPrev[1] {
				t.Errorf("Preview mismatch: got [%v, %v], want [%v, %v]", got.PreviewStart, got.PreviewEnd, tc.wantPrev[0], tc.wantPrev[1])
			}
		})
	}
}

func TestBestEpisode(t *testing.T) {
	episodes := []episodeMeta{
		{
			ID:     "ep-5-phantoms",
			Number: "5",
			Name:   "Phantoms of the Dead",
		},
	}

	// Should match by title "Phantoms of the Dead" even if requested as episode 6 (scraper shift)
	epShifted := bestEpisode(episodes, 0, 6, "Phantoms of the Dead")
	if epShifted == nil || epShifted.ID != "ep-5-phantoms" {
		t.Fatalf("expected ep-5-phantoms by title match, got %+v", epShifted)
	}

	// Absolute numbering cannot substitute for the requested local number.
	if ep25 := bestEpisode([]episodeMeta{
		{ID: "absolute-25", Number: "1", Name: "Hidden Inventory"},
	}, 0, 25, ""); ep25 != nil {
		t.Fatalf("expected unmatched absolute number to be ignored, got %+v", ep25)
	}

	// Repeated episode numbers are ambiguous until a title identifies one.
	// A local episode number can only select a single entry.
	ep5 := bestEpisode([]episodeMeta{
		{ID: "season-episode-5-a", Number: "5"},
		{ID: "season-episode-5-b", Number: "5"},
	}, 0, 5, "")
	if ep5 != nil {
		t.Fatalf("expected duplicate local episode number to be ignored, got %+v", ep5)
	}

	// A repeated local number across seasons is ambiguous without a title.
	if ep5 := bestEpisode([]episodeMeta{
		{ID: "season-1-episode-5", Number: "5"},
		{ID: "season-2-episode-5", Number: "5"},
	}, 0, 5, ""); ep5 != nil {
		t.Fatalf("expected ambiguous episode number to be ignored, got %+v", ep5)
	}

	// Partial titles must not win merely because they appear before the
	// numbered episode.
	ep5 = bestEpisode([]episodeMeta{
		{ID: "wrong-title", Number: "2", Name: "The Battle Begins Again"},
		{ID: "numbered-episode", Number: "5", Name: "Other Title"},
	}, 0, 5, "The Battle Begins")
	if ep5 == nil || ep5.ID != "numbered-episode" {
		t.Fatalf("expected exact episode number over partial title, got %+v", ep5)
	}

	// Missing episode returns nil
	if ep99 := bestEpisode(episodes, 0, 99, ""); ep99 != nil {
		t.Fatalf("expected nil for episode 99, got %+v", ep99)
	}

	// Anime-Skip groups multiple seasons under one AniList show ID.
	ep5Season1 := bestEpisode([]episodeMeta{
		{ID: "fire-force-s1-e5", Season: "1", Number: "5", Name: "The Battle Begins"},
		{ID: "fire-force-s2-e5", Season: "2", Number: "5", Name: "Corna"},
	}, 1, 5, "")
	if ep5Season1 == nil || ep5Season1.ID != "fire-force-s1-e5" {
		t.Fatalf("expected Fire Force season 1 episode 5, got %+v", ep5Season1)
	}

	if ep5 := bestEpisode([]episodeMeta{
		{ID: "fire-force-s1-e5", Season: "1", Number: "5", Name: "The Battle Begins"},
		{ID: "fire-force-s2-e5", Season: "2", Number: "5", Name: "Corna"},
	}, 3, 5, ""); ep5 != nil {
		t.Fatalf("expected missing season 3 episode to be ignored, got %+v", ep5)
	}
}

func TestClientGetTimestamps(t *testing.T) {
	client, err := NewClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("X-Client-ID"); got != "test-client-id" {
			t.Errorf("X-Client-ID = %q, want test-client-id", got)
		}
		body, readErr := io.ReadAll(req.Body)
		if readErr != nil {
			t.Fatalf("read request body: %v", readErr)
		}

		response := `{"data":{"findShowsByExternalId":[{"id":"show-123","name":"Frieren"}]}}`
		switch {
		case bytes.Contains(body, []byte("findEpisodesByShowId")):
			if !bytes.Contains(body, []byte("season")) {
				t.Fatalf("episode lookup must request season: %s", body)
			}
			response = `{"data":{"findEpisodesByShowId":[{"id":"episode-1","season":"1","number":"1","name":"The Journey's End"},{"id":"episode-2","season":"2","number":"1","name":"The Journey's End Again"}]}}`
		case bytes.Contains(body, []byte("findTimestampsByEpisodeId")):
			if !bytes.Contains(body, []byte(`"epId":"episode-1"`)) {
				t.Fatalf("season 1 must select its own episode ID: %s", body)
			}
			response = `{"data":{"findTimestampsByEpisodeId":[{"at":0,"type":{"name":"Canon"}},{"at":90,"type":{"name":"Intro"}},{"at":180,"type":{"name":"Canon"}}]}}`
		}

		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(response)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})}, "test-client-id")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	times, err := client.GetTimestamps(t.Context(), "123", 1, 1, "Frieren", "The Journey's End")
	if err != nil {
		t.Fatalf("GetTimestamps() error = %v", err)
	}
	if times == nil || times.OpStart != 90 || times.OpEnd != 180 {
		t.Fatalf("GetTimestamps() = %+v, want opening [90, 180]", times)
	}
}
