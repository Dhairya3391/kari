package introdb

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestGetSegments_TVSuccess(t *testing.T) {
	jsonPayload := `{
		"imdb_id": "tt0903747",
		"media_type": "tv",
		"is_movie": false,
		"season": 1,
		"episode": 1,
		"intro": {
			"start_sec": 1850.5,
			"end_sec": 2025.0,
			"start_ms": 1850500,
			"end_ms": 2025000,
			"confidence": 0.95,
			"submission_count": 12
		},
		"recap": {
			"start_sec": 0,
			"end_sec": 45.0,
			"confidence": 0.9
		},
		"outro": {
			"start_sec": 3431.0,
			"end_sec": 3500.0,
			"confidence": 1.0
		},
		"post_credits": {
			"start_sec": 3500.0,
			"end_sec": 3540.0
		}
	}`

	httpClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if !strings.Contains(req.URL.Path, "/segments") {
				t.Fatalf("unexpected path: %s", req.URL.Path)
			}
			if req.URL.Query().Get("imdb_id") != "tt0903747" {
				t.Fatalf("unexpected imdb_id: %s", req.URL.Query().Get("imdb_id"))
			}
			if req.URL.Query().Get("season") != "1" || req.URL.Query().Get("episode") != "1" {
				t.Fatalf("unexpected season/episode params: %s", req.URL.RawQuery)
			}
			if req.Header.Get("X-API-Key") != "idb_test_key" {
				t.Fatalf("missing or wrong X-API-Key header: %s", req.Header.Get("X-API-Key"))
			}

			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(jsonPayload)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	client := NewClient(httpClient, "idb_test_key")
	times, err := client.GetSegments(context.Background(), "tt0903747", 1, 1, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if times == nil {
		t.Fatal("expected skip times, got nil")
	}

	if times.OpStart != 1850.5 || times.OpEnd != 2025.0 {
		t.Errorf("Op times = (%f, %f), want (1850.5, 2025.0)", times.OpStart, times.OpEnd)
	}
	if times.RecapStart != 0 || times.RecapEnd != 45.0 {
		t.Errorf("Recap times = (%f, %f), want (0, 45.0)", times.RecapStart, times.RecapEnd)
	}
	if times.EdStart != 3431.0 || times.EdEnd != 3500.0 {
		t.Errorf("Ed times = (%f, %f), want (3431.0, 3500.0)", times.EdStart, times.EdEnd)
	}
	if times.PreviewStart != 3500.0 || times.PreviewEnd != 3540.0 {
		t.Errorf("Preview times = (%f, %f), want (3500.0, 3540.0)", times.PreviewStart, times.PreviewEnd)
	}
}

func TestGetSegments_MovieSuccess(t *testing.T) {
	jsonPayload := `{
		"imdb_id": "tt1375666",
		"media_type": "movie",
		"is_movie": true,
		"season": 0,
		"episode": 0,
		"intro": null,
		"recap": null,
		"outro": {
			"start_sec": 8400.0,
			"end_sec": 8800.0
		},
		"post_credits": {
			"start_sec": 8810.0,
			"end_sec": 8850.0
		}
	}`

	httpClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Query().Get("is_movie") != "true" {
				t.Fatalf("movie request must include is_movie=true, got: %s", req.URL.RawQuery)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(jsonPayload)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	client := NewClient(httpClient, "")
	times, err := client.GetSegments(context.Background(), "tt1375666", 0, 0, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if times == nil {
		t.Fatal("expected skip times for movie, got nil")
	}

	if times.OpStart != -1 || times.RecapStart != -1 {
		t.Errorf("expected absent intro/recap to be -1: %+v", times)
	}
	if times.EdStart != 8400.0 || times.EdEnd != 8800.0 {
		t.Errorf("Ed times = (%f, %f), want (8400.0, 8800.0)", times.EdStart, times.EdEnd)
	}
	if times.PreviewStart != 8810.0 || times.PreviewEnd != 8850.0 {
		t.Errorf("Preview times = (%f, %f), want (8810.0, 8850.0)", times.PreviewStart, times.PreviewEnd)
	}
}

func TestGetSegments_NotFound(t *testing.T) {
	httpClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(bytes.NewBufferString(`{"error": "not found"}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	client := NewClient(httpClient, "")
	times, err := client.GetSegments(context.Background(), "tt9999999", 1, 1, false)
	if err != nil {
		t.Fatalf("expected nil error on 404, got %v", err)
	}
	if times != nil {
		t.Fatalf("expected nil times on 404, got %+v", times)
	}
}
