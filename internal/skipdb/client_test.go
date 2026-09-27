package skipdb

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
		"season": 1,
		"episode": 1,
		"segments": {
			"intro": {
				"start_ms": 229500,
				"end_ms": 246500,
				"match": "agnostic",
				"confidence": 0.75
			},
			"recap": {
				"start_ms": 0,
				"end_ms": 30000,
				"match": "exact"
			},
			"outro": {
				"start_ms": 3434000,
				"end_ms": 3500000,
				"match": "agnostic",
				"confidence": 0.75
			},
			"preview": {
				"start_ms": 3500000,
				"end_ms": 3530000,
				"match": "exact"
			}
		}
	}`

	httpClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if !strings.Contains(req.URL.Path, "/api/segments") {
				t.Fatalf("unexpected path: %s", req.URL.Path)
			}
			if req.URL.Query().Get("imdb_id") != "tt0903747" {
				t.Fatalf("unexpected imdb_id: %s", req.URL.Query().Get("imdb_id"))
			}
			if req.URL.Query().Get("season") != "1" || req.URL.Query().Get("episode") != "1" {
				t.Fatalf("unexpected season/episode params: %s", req.URL.RawQuery)
			}
			if req.Header.Get("X-API-Key") != "skdb_test_key" {
				t.Fatalf("missing or wrong X-API-Key header: %s", req.Header.Get("X-API-Key"))
			}

			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(jsonPayload)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	client := NewClient(httpClient, "skdb_test_key")
	times, err := client.GetSegments(context.Background(), "tt0903747", 1, 1, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if times == nil {
		t.Fatal("expected skip times, got nil")
	}

	if times.OpStart != 229.5 || times.OpEnd != 246.5 {
		t.Errorf("Op times = (%f, %f), want (229.5, 246.5)", times.OpStart, times.OpEnd)
	}
	if times.RecapStart != 0 || times.RecapEnd != 30.0 {
		t.Errorf("Recap times = (%f, %f), want (0, 30.0)", times.RecapStart, times.RecapEnd)
	}
	if times.EdStart != 3434.0 || times.EdEnd != 3500.0 {
		t.Errorf("Ed times = (%f, %f), want (3434.0, 3500.0)", times.EdStart, times.EdEnd)
	}
	if times.PreviewStart != 3500.0 || times.PreviewEnd != 3530.0 {
		t.Errorf("Preview times = (%f, %f), want (3500.0, 3530.0)", times.PreviewStart, times.PreviewEnd)
	}
}

func TestGetSegments_MovieSuccess(t *testing.T) {
	jsonPayload := `{
		"imdb_id": "tt1375666",
		"season": null,
		"episode": null,
		"segments": {
			"intro": null,
			"recap": null,
			"outro": {
				"start_ms": 8455400,
				"end_ms": 8888300
			},
			"preview": null
		}
	}`

	httpClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Query().Get("season") != "" || req.URL.Query().Get("episode") != "" {
				t.Fatalf("movie request should not include season/episode params: %s", req.URL.RawQuery)
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

	if times.OpStart != -1 || times.RecapStart != -1 || times.PreviewStart != -1 {
		t.Errorf("expected absent segments to be -1: %+v", times)
	}
	if times.EdStart != 8455.4 || times.EdEnd != 8888.3 {
		t.Errorf("Ed times = (%f, %f), want (8455.4, 8888.3)", times.EdStart, times.EdEnd)
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

func TestGetSegments_SentinelZeroHandling(t *testing.T) {
	jsonPayload := `{
		"imdb_id": "tt1234567",
		"segments": {
			"intro": { "start_ms": 0, "end_ms": 0 },
			"recap": { "start_ms": 100, "end_ms": 50 },
			"outro": null,
			"preview": null
		}
	}`

	httpClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(jsonPayload)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	client := NewClient(httpClient, "")
	times, err := client.GetSegments(context.Background(), "tt1234567", 1, 1, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if times != nil {
		t.Fatalf("expected all absent segments to return nil times, got %+v", times)
	}
}
