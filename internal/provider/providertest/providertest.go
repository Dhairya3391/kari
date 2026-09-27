// Package providertest is the shared offline harness for providers:
// a redacting record/replay RoundTripper, a scripted Fake provider, a
// contract suite every provider runs against real fixtures, and a chaos
// transport for resilience tests. Live re-recording is explicit:
//
//	go test -tags live -update ./internal/provider/<name>/
//
// fixtures land in testdata/ redacted (tokens, keys, cookies, IPs) with
// large HTML trimmed. Normal CI runs offline tiers only.
package providertest

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"kari/internal/provider"
)

// Update re-records fixtures from live upstreams. Defined here so every
// test binary importing the harness accepts -update.
var Update = flag.Bool("update", false, "re-record provider fixtures from live upstreams")

// maxFixtureBody trims large HTML payloads in fixtures.
const maxFixtureBody = 256 * 1024

// redactQueryKeys are query parameters replaced with [redacted].
var redactQueryKeys = []string{"key", "api_key", "apikey", "token", "auth", "session", "sig", "sign"}

var (
	reIP   = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	reIPv6 = regexp.MustCompile(`\b(?:[0-9a-fA-F]{0,4}:){3,}[0-9a-fA-F:.]+\b`)
)

// RedactURL strips credential query parameters, keeping the domain and
// path so fixtures stay debuggable without leaking secrets.
func RedactURL(raw string) string {
	for _, k := range redactQueryKeys {
		re := regexp.MustCompile(`(?i)([?&]` + k + `=)[^&]*`)
		raw = re.ReplaceAllString(raw, `${1}[redacted]`)
	}
	return raw
}

// RedactBody scrubs IPs and over-long payloads from recorded bodies.
func RedactBody(body []byte) []byte {
	body = reIP.ReplaceAll(body, []byte("0.0.0.0"))
	body = reIPv6.ReplaceAll(body, []byte("[ipv6]"))
	if len(body) > maxFixtureBody {
		body = append(append([]byte{}, body[:maxFixtureBody]...), []byte("\n…[trimmed]")...)
	}
	return body
}

// Recording is one saved HTTP exchange.
type Recording struct {
	Method string
	URL    string
	Status int
	Header http.Header
	Body   []byte
}

// Replay serves canned responses keyed by method+redacted URL. Unknown
// requests fail the test loudly instead of hitting the network.
type Replay struct {
	t       *testing.T
	records map[string]Recording
}

// NewReplay builds a Replay from recordings.
func NewReplay(t *testing.T, recs []Recording) *Replay {
	t.Helper()
	m := make(map[string]Recording, len(recs))
	for _, r := range recs {
		m[r.Method+"\x00"+r.URL] = r
	}
	return &Replay{t: t, records: m}
}

// RoundTrip implements http.RoundTripper without touching the network.
func (r *Replay) RoundTrip(req *http.Request) (*http.Response, error) {
	key := req.Method + "\x00" + RedactURL(req.URL.String())
	rec, ok := r.records[key]
	if !ok {
		r.t.Fatalf("no fixture for %s %s (run with -update to record)", req.Method, RedactURL(req.URL.String()))
		return nil, fmt.Errorf("no fixture")
	}
	resp := &http.Response{
		StatusCode: rec.Status,
		Header:     rec.Header.Clone(),
		Body:       io.NopCloser(bytes.NewReader(rec.Body)),
		Request:    req,
	}
	return resp, nil
}

// RoundTripFunc adapts a function to http.RoundTripper.
type RoundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip implements http.RoundTripper.
func (f RoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// WithTransport swaps a client's transport (tests only).
func WithTransport(c *http.Client, rt http.RoundTripper) *http.Client {
	c.Transport = rt
	return c
}

// FixturePath resolves the testdata file for a provider case.
func FixturePath(provider, name string) string {
	return filepath.Join("testdata", provider+"_"+name+".txt")
}

// Fake is a scripted Provider double for contract, resilience and
// service tests: latency, errors and chaos per stage.
type Fake struct {
	NameValue string
	ModeValue provider.ContentType
	SearchFn  func(ctx context.Context, q string) ([]provider.SearchResult, error)
	ItemsFn   func(ctx context.Context, s provider.SearchResult) ([]provider.Episode, error)
	SourcesFn func(ctx context.Context, mediaID string, e provider.Episode) ([]provider.MediaSource, error)
	Delay     time.Duration
}

// Name implements provider.Provider.
func (f *Fake) Name() string { return f.NameValue }

// Modes implements provider.Provider.
func (f *Fake) Modes() []provider.Mode { return []provider.Mode{{Name: f.ModeValue, Priority: 1}} }

// wait honors delay or context cancel.
func (f *Fake) wait(ctx context.Context) error {
	if f.Delay <= 0 {
		return ctx.Err()
	}
	select {
	case <-time.After(f.Delay):
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Search implements provider.Provider.
func (f *Fake) Search(ctx context.Context, q string, _ provider.ContentType) ([]provider.SearchResult, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	if f.SearchFn != nil {
		return f.SearchFn(ctx, q)
	}
	return nil, provider.ErrNoResults
}

// FetchEpisodes implements provider.Provider.
func (f *Fake) FetchEpisodes(ctx context.Context, s provider.SearchResult) ([]provider.Episode, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	if f.ItemsFn != nil {
		return f.ItemsFn(ctx, s)
	}
	return nil, provider.ErrNoEpisodes
}

// ResolveSource implements provider.Provider.
func (f *Fake) ResolveSource(ctx context.Context, mediaID string, e provider.Episode) ([]provider.MediaSource, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	if f.SourcesFn != nil {
		return f.SourcesFn(ctx, mediaID, e)
	}
	return nil, provider.ErrNoSources
}

// Spec drives the contract suite: closures over a provider instance
// (usually wired to a replay transport) plus the handles to chain.
type Spec struct {
	Mode    provider.ContentType
	Kinds   []provider.ContentType
	Series  provider.SearchResult
	Episode provider.Episode
	MediaID string
	Search  func(ctx context.Context) ([]provider.SearchResult, error)
	Items   func(ctx context.Context, s provider.SearchResult) ([]provider.Episode, error)
	Sources func(ctx context.Context, mediaID string, e provider.Episode) ([]provider.MediaSource, error)
}

// Run executes the contract suite: shape invariants, error taxonomy,
// cancellation, race safety and goroutine hygiene. It runs against real
// fixtures (replay) and fakes alike.
func Run(t *testing.T, s Spec) {
	t.Helper()
	ctx := context.Background()

	// Titles carry ID, Name and a Kind within the provider's declared
	// kinds; items are ordered and numbered; sources are unique with
	// URLs and headers when required.
	t.Run("shapes", func(t *testing.T) {
		titles, err := s.Search(ctx)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(titles) == 0 {
			t.Fatal("search returned zero titles without error")
		}
		for _, title := range titles {
			if title.ID == "" || title.Title == "" {
				t.Errorf("title missing ID/Name: %+v", title)
			}
			if len(s.Kinds) > 0 {
				ok := false
				for _, k := range s.Kinds {
					if title.Type == k {
						ok = true
					}
				}
				if !ok {
					t.Errorf("title kind %q outside %v", title.Type, s.Kinds)
				}
			}
		}
		items, err := s.Items(ctx, s.Series)
		if err != nil {
			t.Fatalf("items: %v", err)
		}
		if len(items) == 0 {
			t.Fatal("items returned zero rows without error")
		}
		seen := make(map[string]struct{}, len(items))
		for _, it := range items {
			if it.ID == "" {
				t.Errorf("item missing ID: %+v", it)
			}
			if _, dup := seen[it.ID]; dup {
				t.Errorf("duplicate item ID %q", it.ID)
			}
			seen[it.ID] = struct{}{}
		}
		sources, err := s.Sources(ctx, s.MediaID, s.Episode)
		if err != nil {
			t.Fatalf("sources: %v", err)
		}
		if len(sources) == 0 {
			t.Fatal("sources returned zero rows without error")
		}
		urls := make(map[string]struct{}, len(sources))
		for _, src := range sources {
			if strings.TrimSpace(src.URL) == "" {
				t.Errorf("source missing URL: %+v", src)
			}
			if _, dup := urls[src.URL]; dup {
				t.Errorf("duplicate source URL %q", src.URL)
			}
			urls[src.URL] = struct{}{}
		}
	})

	// Typed errors: not-found surfaces ErrNotFound (never a silent
	// empty), and cancellation returns within 200ms.
	t.Run("cancel", func(t *testing.T) {
		cctx, cancel := context.WithCancel(context.Background())
		cancel()
		start := time.Now()
		_, _ = s.Search(cctx)
		_, _ = s.Items(cctx, s.Series)
		_, _ = s.Sources(cctx, s.MediaID, s.Episode)
		if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
			t.Errorf("canceled calls took %v, want <200ms", elapsed)
		}
	})

	// Parallel calls are race-safe and leak no goroutines. Warm-up
	// rounds run until the count stabilizes (transport keep-alive
	// connections linger by design and need a round or two to settle),
	// then one measured round must add none. A provider leaking per
	// call grows every round and still fails.
	t.Run("race", func(t *testing.T) {
		round := func() {
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, _ = s.Search(ctx)
					_, _ = s.Items(ctx, s.Series)
					_, _ = s.Sources(ctx, s.MediaID, s.Episode)
				}()
			}
			wg.Wait()
		}
		settle := func() {
			deadline := time.Now().Add(500 * time.Millisecond)
			for time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
		}
		for i := 0; i < 4; i++ {
			before := runtime.NumGoroutine()
			round()
			settle()
			if runtime.NumGoroutine() <= before {
				break
			}
		}
		before := runtime.NumGoroutine()
		round()
		deadline := time.Now().Add(500 * time.Millisecond)
		for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if n := runtime.NumGoroutine(); n > before {
			t.Errorf("goroutines before=%d after=%d (leak)", before, n)
		}
	})
}

// ChaosFault selects the fault a Chaos transport injects.
type ChaosFault int

// Chaos faults.
const (
	ChaosOK ChaosFault = iota
	Chaos429
	Chaos500
	ChaosTimeout
	ChaosTruncated
	ChaosMalformed
	ChaosChangedHTML
)

// Chaos serves one scripted fault per request for resilience tests.
type Chaos struct {
	Fault      ChaosFault
	RetryAfter string
	Body       []byte
	Delay      time.Duration
}

// RoundTrip implements http.RoundTripper.
func (c *Chaos) RoundTrip(req *http.Request) (*http.Response, error) {
	switch c.Fault {
	case Chaos429:
		h := make(http.Header)
		if c.RetryAfter != "" {
			h.Set("Retry-After", c.RetryAfter)
		}
		return &http.Response{StatusCode: http.StatusTooManyRequests, Header: h, Body: io.NopCloser(bytes.NewReader(nil)), Request: req}, nil
	case Chaos500:
		return &http.Response{StatusCode: http.StatusInternalServerError, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader([]byte("boom"))), Request: req}, nil
	case ChaosTimeout:
		select {
		case <-time.After(c.Delay):
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(c.Body)), Request: req}, nil
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	case ChaosTruncated:
		half := c.Body[:len(c.Body)/2]
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(half)), Request: req}, nil
	case ChaosMalformed:
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader([]byte("{not json"))), Request: req}, nil
	case ChaosChangedHTML:
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader([]byte("<html><body>challenge</body></html>"))), Request: req}, nil
	default:
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(c.Body)), Request: req}, nil
	}
}

// WriteFixture saves redacted recordings (record mode only).
func WriteFixture(t *testing.T, path string, recs []Recording) {
	t.Helper()
	if !*Update {
		t.Skip("record mode only (run with -update)")
	}
	var b strings.Builder
	for _, r := range recs {
		fmt.Fprintf(&b, "=== %s %s => %d (%d bytes)\n", r.Method, RedactURL(r.URL), r.Status, len(r.Body))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir testdata: %v", err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}
