package subtitles

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

func TestProcessSubtitleData_PlainSRT(t *testing.T) {
	raw := []byte("1\n00:00:01,000 --> 00:00:04,000\nHello World\n\n")
	processed, format := ProcessSubtitleData(raw)
	if format != "srt" {
		t.Fatalf("expected format 'srt', got %q", format)
	}
	if string(processed) != string(raw) {
		t.Fatalf("content mismatch: got %q, want %q", string(processed), string(raw))
	}
}

func TestProcessSubtitleData_WebVTTConversion(t *testing.T) {
	raw := []byte("WEBVTT\n\n00:00:01.000 --> 00:00:04.000\n<v Speaker>Hello World</v>\n\n")
	processed, format := ProcessSubtitleData(raw)
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

	processed, format := ProcessSubtitleData(buf.Bytes())
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

	processed, format := ProcessSubtitleData(buf.Bytes())
	if format != "srt" {
		t.Fatalf("expected 'srt', got %q", format)
	}
	if !strings.Contains(string(processed), "Zip extracted text") {
		t.Fatalf("expected extracted text from zip, got %q", string(processed))
	}
}
