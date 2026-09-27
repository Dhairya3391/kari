package subtitles

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

type opensubtitlesRoundTripper func(*http.Request) (*http.Response, error)

func (f opensubtitlesRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestFetchBestSubtitleDownloadsAndMaterializesTrack(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := NewClient("api-key", "user", "password")
	client.http = &http.Client{Transport: opensubtitlesRoundTripper(func(req *http.Request) (*http.Response, error) {
		var statusCode int
		var body string
		switch {
		case strings.HasSuffix(req.URL.Path, "/login"):
			statusCode, body = http.StatusOK, `{"token":"test-token"}`
		case strings.HasSuffix(req.URL.Path, "/subtitles"):
			if req.Header.Get("Authorization") != "Bearer test-token" {
				return nil, fmt.Errorf("missing bearer token")
			}
			statusCode, body = http.StatusOK, `{"data":[{"attributes":{"language":"en","download_count":10,"format":"srt","release":"Example Movie","files":[{"file_id":42,"file_name":"example.srt"}]}}]}`
		case strings.HasSuffix(req.URL.Path, "/download"):
			statusCode, body = http.StatusOK, `{"link":"https://files.example/subtitle.srt"}`
		case req.URL.Host == "files.example":
			statusCode, body = http.StatusOK, "1\n00:00:01,000 --> 00:00:02,000\nhello\n"
		default:
			return nil, fmt.Errorf("unexpected OpenSubtitles request: %s", req.URL)
		}
		return &http.Response{
			StatusCode: statusCode,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}

	track, found, err := client.FetchBestSubtitle(context.Background(), "Example Movie", "en", 123, 0, 0)
	if err != nil {
		t.Fatalf("FetchBestSubtitle: %v", err)
	}
	if !found {
		t.Fatal("FetchBestSubtitle found no subtitle")
	}
	if track.Path == "" || track.Language != "en" {
		t.Fatalf("track = %+v, want materialized English subtitle", track)
	}
	data, err := os.ReadFile(track.Path)
	if err != nil {
		t.Fatalf("read materialized subtitle: %v", err)
	}
	if !strings.Contains(string(data), "-->") {
		t.Fatalf("materialized subtitle has no cue timestamp: %q", data)
	}
}
