package logging

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no URL",
			in:   "search done",
			want: "search done",
		},
		{
			name: "public URL unchanged",
			in:   "https://example.com/path?x=1",
			want: "https://example.com/path?x=1",
		},
		{
			name: "signed URL",
			in:   "https://cdn.example.com/m.m3u8?token=abc&expires=123&q=4",
			want: "https://cdn.example.com/m.m3u8?expires=REDACTED&q=4&token=REDACTED",
		},
		{
			name: "API key",
			in:   "https://api.themoviedb.org/3/search?api_key=secret&query=x",
			want: "https://api.themoviedb.org/3/search?api_key=REDACTED&query=x",
		},
		{
			name: "case insensitive key",
			in:   "https://x.test/a?API_KEY=secret",
			want: "https://x.test/a?API_KEY=REDACTED",
		},
		{
			name: "multiple URLs in prose",
			in:   "GET https://x.test/a?token=s1 failed, master https://y.test/b?key=s2 used",
			want: "GET https://x.test/a?token=REDACTED failed, master https://y.test/b?key=REDACTED used",
		},
		{
			name: "unparsable URL preserved",
			in:   "https://%zz/broken?token=s",
			want: "https://%zz/broken?token=s",
		},
		{
			name: "fragment and trailing punctuation",
			in:   "url https://x.test/a?token=s.",
			want: "url https://x.test/a?token=REDACTED.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Redact(tt.in); got != tt.want {
				t.Errorf("Redact(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRedactNoScheme(t *testing.T) {
	t.Parallel()
	if got := Redact("request to cdn.example.com/a?token=secret failed"); !strings.Contains(got, "token=secret") {
		t.Fatalf("scheme-less strings must be passed through, got %q", got)
	}
}
