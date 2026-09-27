package aniskip

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestGetSkipTimes_Found(t *testing.T) {
	jsonBody := `{
		"found": true,
		"results": [
			{
				"interval": {"start_time": 90.5, "end_time": 180.5},
				"skip_type": "op"
			},
			{
				"interval": {"start_time": 1350.0, "end_time": 1440.0},
				"skip_type": "ed"
			}
		]
	}`

	httpClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/v1/skip-times/21/1" {
				t.Fatalf("unexpected path: %s", req.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(jsonBody)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	client := NewClient(httpClient)
	times, err := client.GetSkipTimes(context.Background(), 21, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if times == nil {
		t.Fatal("expected non-nil SkipTimes")
	}
	if times.OpStart != 90.5 || times.OpEnd != 180.5 {
		t.Errorf("Op times = [%v, %v], want [90.5, 180.5]", times.OpStart, times.OpEnd)
	}
	if times.EdStart != 1350.0 || times.EdEnd != 1440.0 {
		t.Errorf("Ed times = [%v, %v], want [1350, 1440]", times.EdStart, times.EdEnd)
	}
}

func TestGetSkipTimes_NotFound(t *testing.T) {
	httpClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(bytes.NewBufferString(`{"found":false}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	client := NewClient(httpClient)
	times, err := client.GetSkipTimes(context.Background(), 999999, 1)
	if err != nil {
		t.Fatalf("unexpected error on 404: %v", err)
	}
	if times != nil {
		t.Fatalf("expected nil SkipTimes on 404, got %+v", times)
	}
}

func TestGetIDs(t *testing.T) {
	jsonBody := `{
		"data": {
			"Media": {
				"id": 154587,
				"idMal": 52991
			}
		}
	}`

	httpClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(jsonBody)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	client := NewClient(httpClient)
	anilistID, malID, err := client.GetIDs(context.Background(), "Frieren")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if anilistID != 154587 {
		t.Errorf("anilistID = %d, want 154587", anilistID)
	}
	if malID != 52991 {
		t.Errorf("malID = %d, want 52991", malID)
	}
}

func TestGetMALID(t *testing.T) {
	jsonBody := `{
		"data": {
			"Media": {
				"id": 154587,
				"idMal": 52991
			}
		}
	}`

	httpClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(jsonBody)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	client := NewClient(httpClient)
	malID, err := client.GetMALID(context.Background(), "Frieren")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if malID != 52991 {
		t.Errorf("malID = %d, want 52991", malID)
	}
}
