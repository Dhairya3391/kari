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
			wantPrev:  [2]float64{1430.0, 1520.0}, // unclosed preview: +90s
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
			wantEd:    [2]float64{1300.0, 1390.0},
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
			ID:     "ep-1-sparse",
			Number: "1",
			Name:   "Sparse Episode",
		},
		{
			ID:     "ep-1-rich",
			Number: "1",
			Name:   "Rich Episode",
		},
		{
			ID:             "ep-25-absolute",
			Number:         "1",
			AbsoluteNumber: "25",
			Name:           "Hidden Inventory",
		},
		{
			ID:     "ep-5-phantoms",
			Number: "5",
			Name:   "Phantoms of the Dead",
		},
	}

	// Should match by title "Phantoms of the Dead" even if requested as episode 6 (scraper shift)
	epShifted := bestEpisode(episodes, 6, "Phantoms of the Dead")
	if epShifted == nil || epShifted.ID != "ep-5-phantoms" {
		t.Fatalf("expected ep-5-phantoms by title match, got %+v", epShifted)
	}

	// Should match by absoluteNumber 25
	ep25 := bestEpisode(episodes, 25, "")
	if ep25 == nil || ep25.ID != "ep-25-absolute" {
		t.Fatalf("expected ep-25-absolute, got %+v", ep25)
	}

	// Should match by number 1
	ep1 := bestEpisode(episodes, 1, "")
	if ep1 == nil {
		t.Fatalf("expected non-nil for episode 1")
	}

	// Missing episode returns nil
	if ep99 := bestEpisode(episodes, 99, ""); ep99 != nil {
		t.Fatalf("expected nil for episode 99, got %+v", ep99)
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
			response = `{"data":{"findEpisodesByShowId":[{"id":"episode-1","number":"1","name":"The Journey's End"}]}}`
		case bytes.Contains(body, []byte("findTimestampsByEpisodeId")):
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

	times, err := client.GetTimestamps(t.Context(), "123", 1, "Frieren", "The Journey's End")
	if err != nil {
		t.Fatalf("GetTimestamps() error = %v", err)
	}
	if times == nil || times.OpStart != 90 || times.OpEnd != 180 {
		t.Fatalf("GetTimestamps() = %+v, want opening [90, 180]", times)
	}
}
