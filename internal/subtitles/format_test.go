package subtitles

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProcessSubtitleData_PlainSRT(t *testing.T) {
	raw := []byte("1\n00:00:01,000 --> 00:00:04,000\nHello World\n\n")
	processed, format, err := ProcessSubtitleData(raw)
	if err != nil {
		t.Fatalf("ProcessSubtitleData: %v", err)
	}
	if format != "srt" {
		t.Fatalf("expected format 'srt', got %q", format)
	}
	if string(processed) != string(raw) {
		t.Fatalf("content mismatch: got %q, want %q", string(processed), string(raw))
	}
}

func TestProcessSubtitleData_WebVTTConversion(t *testing.T) {
	raw := []byte("WEBVTT\n\n00:00:01.000 --> 00:00:04.000\n<v Speaker>Hello World</v>\n\n")
	processed, format, err := ProcessSubtitleData(raw)
	if err != nil {
		t.Fatalf("ProcessSubtitleData: %v", err)
	}
	if format != "srt-from-vtt" {
		t.Fatalf("expected format 'srt-from-vtt', got %q", format)
	}
	result := string(processed)
	if !strings.Contains(result, "00:00:01,000 --> 00:00:04,000") {
		t.Fatalf("expected converted comma timestamp, got %q", result)
	}
	if !strings.Contains(result, "Hello World") {
		t.Fatalf("expected cleaned subtitle text, got %q", result)
	}
	if strings.Contains(result, "<v") {
		t.Fatalf("expected voice tags to be stripped, got %q", result)
	}
}

func TestProcessSubtitleData_GZIP(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write([]byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nGzip test\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	zw.Close()

	processed, format, err := ProcessSubtitleData(buf.Bytes())
	if err != nil {
		t.Fatalf("ProcessSubtitleData: %v", err)
	}
	if format != "srt-from-vtt" {
		t.Fatalf("expected 'srt-from-vtt', got %q", format)
	}
	if !strings.Contains(string(processed), "Gzip test") {
		t.Fatalf("expected uncompressed text, got %q", string(processed))
	}
}

func TestProcessSubtitleData_ZIP(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create("movie_subs.srt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("1\n00:00:05,000 --> 00:00:08,000\nZip extracted text\n\n"))
	zw.Close()

	processed, format, err := ProcessSubtitleData(buf.Bytes())
	if err != nil {
		t.Fatalf("ProcessSubtitleData: %v", err)
	}
	if format != "srt" {
		t.Fatalf("expected 'srt', got %q", format)
	}
	if !strings.Contains(string(processed), "Zip extracted text") {
		t.Fatalf("expected extracted text from zip, got %q", string(processed))
	}
}

func TestProcessSubtitleDataRejectsNonSubtitleResponses(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{name: "html", data: []byte("<!doctype html><html><body>challenge</body></html>")},
		{name: "json", data: []byte(`{"error":"not found"}`)},
		{name: "image", data: append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)},
		{name: "unknown text", data: []byte("service unavailable")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := ProcessSubtitleData(test.data); err == nil {
				t.Fatal("expected invalid subtitle data to fail")
			}
		})
	}
}

func TestOpenSubtitlesFileDownloadDoesNotLeakAPICredentials(t *testing.T) {
	headers := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		_, _ = w.Write([]byte("subtitle"))
	}))
	defer server.Close()

	client := NewClient("api-key", "user", "password")
	client.token = "bearer-token"
	if _, err := client.downloadFile(t.Context(), server.URL+"/subtitle"); err != nil {
		t.Fatalf("downloadFile: %v", err)
	}
	got := <-headers
	if got.Get("Api-Key") != "" || got.Get("Authorization") != "" {
		t.Fatalf("file request leaked API credentials: %v", got)
	}
}
