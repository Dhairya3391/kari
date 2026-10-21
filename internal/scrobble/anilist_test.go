package scrobble

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kari/internal/model"
)

func TestAniListClient_GetMediaListEntry(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string                 `json:"query"`
			Variables map[string]interface{} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if req.Variables["id"] == float64(154587) || req.Variables["search"] == "Frieren" {
			_, _ = w.Write([]byte(`{
				"data": {
					"Media": {
						"id": 154587,
						"episodes": 28,
						"mediaListEntry": {
							"id": 999,
							"status": "COMPLETED",
							"progress": 28,
							"repeat": 1
						}
					}
				}
			}`))
			return
		}
		if req.Variables["id"] == float64(99999) {
			_, _ = w.Write([]byte(`{
				"data": {
					"Media": {
						"id": 99999,
						"episodes": 12,
						"mediaListEntry": null
					}
				}
			}`))
			return
		}

		_, _ = w.Write([]byte(`{"data":{"Media":null}}`))
	}))
	defer ts.Close()

	client := testAnilistClient(ts)

	// 1. By ID with existing entry
	entry, total, err := client.GetMediaListEntry(context.Background(), 154587, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 28 {
		t.Errorf("totalEpisodes = %d, want 28", total)
	}
	if entry == nil {
		t.Fatalf("expected entry, got nil")
	}
	if entry.ID != 999 || entry.Status != StatusCompleted || entry.Progress != 28 || entry.Repeat != 1 {
		t.Errorf("unexpected entry values: %+v", entry)
	}

	// 2. By search title
	entry, total, err = client.GetMediaListEntry(context.Background(), 0, "Frieren")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 28 {
		t.Errorf("totalEpisodes by search = %d, want 28", total)
	}
	if entry == nil || entry.ID != 999 {
		t.Errorf("unexpected entry by search: %+v", entry)
	}

	// 3. Media without mediaListEntry (not in user's list)
	entry, total, err = client.GetMediaListEntry(context.Background(), 99999, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 12 {
		t.Errorf("totalEpisodes = %d, want 12", total)
	}
	if entry != nil {
		t.Errorf("expected nil entry, got %+v", entry)
	}

	// 4. Unauthenticated
	unauth := &AniListClient{httpClient: ts.Client(), apiBase: ts.URL}
	if _, _, err := unauth.GetMediaListEntry(context.Background(), 154587, ""); err == nil {
		t.Error("expected error for unauthenticated client")
	}
}

func TestAniListClient_UpdateMediaListStatus(t *testing.T) {
	var lastVars map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string                 `json:"query"`
			Variables map[string]interface{} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		lastVars = req.Variables

		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Query, "Media (search:") {
			_, _ = w.Write([]byte(`{"data":{"Media":{"id":154587}}}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"data": {
				"SaveMediaListEntry": {
					"id": 999,
					"status": "REPEATING",
					"progress": 0,
					"repeat": 1
				}
			}
		}`))
	}))
	defer ts.Close()

	client := testAnilistClient(ts)

	progress := 0
	repeat := 1
	err := client.UpdateMediaListStatus(context.Background(), 154587, "", StatusRepeating, &progress, &repeat)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lastVars["mediaId"] != float64(154587) || lastVars["status"] != string(StatusRepeating) {
		t.Errorf("unexpected variables: %+v", lastVars)
	}
	if lastVars["progress"] != float64(0) || lastVars["repeat"] != float64(1) {
		t.Errorf("unexpected progress/repeat variables: %+v", lastVars)
	}

	// With search title (mediaID = 0)
	err = client.UpdateMediaListStatus(context.Background(), 0, "Frieren", StatusCurrent, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error with title search: %v", err)
	}
	if lastVars["status"] != string(StatusCurrent) {
		t.Errorf("status = %v, want CURRENT", lastVars["status"])
	}
}

func TestAniListClient_DeleteMediaListEntry(t *testing.T) {
	var lastVars map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string                 `json:"query"`
			Variables map[string]interface{} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		lastVars = req.Variables

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"DeleteMediaListEntry":{"deleted":true}}}`))
	}))
	defer ts.Close()

	client := testAnilistClient(ts)
	err := client.DeleteMediaListEntry(context.Background(), 999)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lastVars["id"] != float64(999) {
		t.Errorf("id = %v, want 999", lastVars["id"])
	}

	// Unauthenticated
	unauth := &AniListClient{httpClient: ts.Client(), apiBase: ts.URL}
	if err := unauth.DeleteMediaListEntry(context.Background(), 999); err == nil {
		t.Error("expected error for unauthenticated client")
	}
}

func TestAniListClient_UpdateProgressDoesNotForceCurrent(t *testing.T) {
	var lastQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		lastQuery = req.Query

		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Query, "Media (search:") {
			_, _ = w.Write([]byte(`{"data":{"Media":{"id":154587}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"SaveMediaListEntry":{"id":999,"progress":5}}}`))
	}))
	defer ts.Close()

	client := testAnilistClient(ts)
	client.token.ExpiresAt = time.Now().Add(time.Hour)

	err := client.UpdateProgress(context.Background(), model.ResolvedMedia{
		SeriesTitle:   "Frieren",
		EpisodeNumber: 5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(lastQuery, "status: CURRENT") {
		t.Errorf("SaveMediaListEntry should not hardcode status: CURRENT, query was: %s", lastQuery)
	}
}
