package zlive

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"kari/internal/provider"
)

const testPlaylist = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080
1080.m3u8
`

type resolveHarness struct {
	client   *Client
	server   *httptest.Server
	ticketOK atomic.Bool
	location atomic.Value
	requests atomic.Int32
}

func newResolveHarness(t *testing.T) *resolveHarness {
	t.Helper()
	h := &resolveHarness{}
	h.ticketOK.Store(true)
	h.location.Store("")
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels.json":
			_, _ = w.Write([]byte(testCatalog))
		case "/session/ticket":
			if !h.ticketOK.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(`{"w":"TESTTICKET"}`))
		case "/resolve":
			h.requests.Add(1)
			var envelope signedRequest
			if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
				t.Errorf("decode envelope: %v", err)
			}
			if envelope.E != "TESTTICKET" || envelope.D == "" || envelope.A == "" || envelope.B == "" || envelope.C == "" {
				t.Errorf("incomplete envelope: %+v", envelope)
			}
			_, _ = w.Write([]byte(`{"location":"` + h.location.Load().(string) + `"}`))
		case "/play.m3u8":
			_, _ = w.Write([]byte(testPlaylist))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(h.server.Close)

	client, err := NewClientWithURLs(h.server.URL, "https://zlive.st")
	if err != nil {
		t.Fatalf("NewClientWithURLs: %v", err)
	}
	client.httpClient = &http.Client{Transport: rewriteTransport{base: h.server.URL, next: client.httpClient.Transport}}
	h.client = client
	h.location.Store("https://streams.example.test/play.m3u8")
	return h
}

func TestResolveSource(t *testing.T) {
	h := newResolveHarness(t)
	sources, err := h.client.ResolveSource(context.Background(), "zlive:f1-uk", provider.Episode{})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("got %d sources: %+v", len(sources), sources)
	}
	source := sources[0]
	if !strings.HasSuffix(source.URL, "/play.m3u8") {
		t.Errorf("URL = %q", source.URL)
	}
	if source.Type != provider.SourceTypeHLS || source.Backend != "Primary" {
		t.Errorf("type/backend = %q / %q", source.Type, source.Backend)
	}
	if source.Quality != "1080p" {
		t.Errorf("quality = %q, want 1080p from catalog label", source.Quality)
	}
	// The URL is single-use, so the provider never probes it before playback.
	if source.Height != 0 || source.Bitrate != 0 {
		t.Errorf("height/bitrate = %d / %d, want no pre-probe", source.Height, source.Bitrate)
	}
	if source.Referer != "https://zlive.st/" || source.UserAgent == "" {
		t.Errorf("transport headers = %q / %q", source.Referer, source.UserAgent)
	}
	if h.requests.Load() != 1 {
		t.Errorf("resolve requests = %d, want 1", h.requests.Load())
	}
}

func TestResolveSourceFallsBackToBareID(t *testing.T) {
	h := newResolveHarness(t)
	sources, err := h.client.ResolveSource(context.Background(), "zlive:unknown-channel", provider.Episode{})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 || sources[0].Backend != "zlive" {
		t.Fatalf("sources = %+v", sources)
	}
}

func TestResolveSourceRejectsInvalidHandle(t *testing.T) {
	h := newResolveHarness(t)
	_, err := h.client.ResolveSource(context.Background(), "", provider.Episode{})
	if !errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("error = %v, want ErrNoSources", err)
	}
}

func TestResolveSourceRejectsDecoyLocation(t *testing.T) {
	h := newResolveHarness(t)
	h.location.Store("https://route.transcode.cfd/m3u8-proxy.m3u8?data=abc")
	_, err := h.client.ResolveSource(context.Background(), "zlive:f1-uk", provider.Episode{})
	if !errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("error = %v, want ErrNoSources", err)
	}
}

func TestResolveSourceRetriesTicketFailure(t *testing.T) {
	h := newResolveHarness(t)
	h.ticketOK.Store(false)
	_, err := h.client.ResolveSource(context.Background(), "zlive:f1-uk", provider.Episode{})
	if !errors.Is(err, provider.ErrNoSources) {
		t.Fatalf("error = %v, want ErrNoSources", err)
	}
	if h.requests.Load() != 0 {
		t.Errorf("resolve should not be called without a ticket; got %d", h.requests.Load())
	}
}

// rewriteTransport sends non-local test-host requests to the local fixture
// server so provider.FilterDirectSources keeps the resolved row.
type rewriteTransport struct {
	base string
	next http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, err := url.Parse(t.base + req.URL.RequestURI())
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.URL = target
	clone.Host = target.Host
	next := t.next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(clone)
}
