package httpclient

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/hashicorp/go-retryablehttp"

	"kari/internal/config"
	"kari/internal/logging"
)

const (
	defaultTimeout = 15 * time.Second
	defaultRetries = 2
	// maxPerHost caps concurrent requests to one host so provider fan-out
	// stays polite: 16 in flight per host, the rest wait on context.
	maxPerHost = 16
	// MaxBodyBytes caps JSON/HTML response bodies (8 MB). Media downloads
	// never flow through the capped helpers.
	MaxBodyBytes = 8 << 20
)

// New returns a shared HTTP client with retry and timeout settings.
func New() *http.Client {
	return newClient(defaultTimeout)
}

// NewWithTimeout returns a shared HTTP client with a custom timeout.
func NewWithTimeout(timeout time.Duration) *http.Client {
	return newClient(timeout)
}

// NewWithUserAgent returns a shared HTTP client that injects a User-Agent header.
func NewWithUserAgent(userAgent string) *http.Client {
	client := newClient(defaultTimeout)
	client.Transport = &uaRoundTripper{
		next: client.Transport,
		ua:   userAgent,
	}
	return client
}

func newClient(timeout time.Duration) *http.Client {
	retryClient := retryablehttp.NewClient()
	retryClient.RetryMax = defaultRetries
	retryClient.RetryWaitMin = 200 * time.Millisecond
	retryClient.RetryWaitMax = 1500 * time.Millisecond
	// Retry idempotent reads only: connect errors, 5xx and 429 (honoring
	// Retry-After via the base policy). POSTs never retry so a flaky
	// transport cannot double-submit a write; the whole call stays
	// bounded by the client timeout so retries never multiply past the
	// caller's context deadline.
	retryClient.CheckRetry = func(ctx context.Context, resp *http.Response, err error) (bool, error) {
		if resp != nil && resp.Request != nil {
			switch resp.Request.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
			default:
				return false, nil
			}
		}
		return retryablehttp.DefaultRetryPolicy(ctx, resp, err)
	}
	retryClient.HTTPClient.Timeout = timeout

	transport := http.DefaultTransport.(*http.Transport).Clone()

	// Go's default MaxIdleConnsPerHost is 2, which is too low for providers
	// that fan out several concurrent requests to their own host (e.g. the
	// streaming providers' parallel multi-quality resolves) — with only 2
	// idle connections kept warm, a burst beyond that forces extra TCP+TLS
	// handshakes instead of reusing connections.
	transport.ForceAttemptHTTP2 = true
	transport.MaxIdleConns = 200
	transport.MaxIdleConnsPerHost = 32
	transport.IdleConnTimeout = 90 * time.Second
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ExpectContinueTimeout = time.Second

	// Universal resilient DNS dialer: uses system DNS first, falling back to
	// public DNS (Cloudflare 1.1.1.1/1.0.0.1, Google 8.8.8.8/8.8.4.4) over
	// UDP/TCP if system DNS is blocked, hijacked, or fails.
	dialer := newResilientDialer()
	transport.DialContext = dialer.DialContext

	// Compression stays on (HTTP/2 + gzip via the default transport; no
	// code sets Accept-Encoding manually) and connections are reused.
	var rt http.RoundTripper = transport
	rt = &hostSemaphore{next: rt}
	rt = &kariClientRoundTripper{next: rt}
	rt = &uaRoundTripper{next: rt, ua: config.DesktopUserAgent}
	retryClient.HTTPClient.Transport = rt
	retryClient.Logger = &leveledLogger{}
	return retryClient.StandardClient()
}

// hostSemaphore caps concurrent requests per host. It is safe for
// concurrent use; waiting acquires respect the request context.
type hostSemaphore struct {
	mu   sync.Mutex
	sems map[string]chan struct{}
	next http.RoundTripper
}

// slot returns the semaphore for host, creating it on first use.
func (h *hostSemaphore) slot(host string) chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sems == nil {
		h.sems = make(map[string]chan struct{})
	}
	s, ok := h.sems[host]
	if !ok {
		s = make(chan struct{}, maxPerHost)
		h.sems[host] = s
	}
	return s
}

// RoundTrip waits for a per-host slot, then delegates.
func (h *hostSemaphore) RoundTrip(req *http.Request) (*http.Response, error) {
	s := h.slot(req.URL.Host)
	select {
	case s <- struct{}{}:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	defer func() { <-s }()
	return h.next.RoundTrip(req)
}

// ErrBodyTooLarge reports a response exceeding MaxBodyBytes.
var ErrBodyTooLarge = fmt.Errorf("response body exceeds %d bytes", MaxBodyBytes)

// ReadCapped reads a JSON/HTML response body bounded by MaxBodyBytes. An
// oversized body errors instead of being silently truncated, so a huge
// page surfaces as a typed failure rather than a confusing parse error.
func ReadCapped(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(body) > MaxBodyBytes {
		return nil, ErrBodyTooLarge
	}
	return body, nil
}

// kariClientRoundTripper tags every outgoing request as coming from this app.
// Our own Cloudflare-fronted APIs (broggl.farm) use it to bypass bot
// challenges that otherwise intermittently misclassify the app's non-browser
// TLS fingerprint and return an HTML challenge page instead of JSON.
type kariClientRoundTripper struct {
	next http.RoundTripper
}

func (t *kariClientRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set(config.KariClientHeader, config.KariClientToken)
	return t.next.RoundTrip(req)
}

// uaRoundTripper injects a default User-Agent when a request doesn't carry
// one, letting individual requests still override it.
type uaRoundTripper struct {
	next http.RoundTripper
	ua   string
}

func (t *uaRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", t.ua)
	}
	return t.next.RoundTrip(req)
}

// leveledLogger adapts retryablehttp's retry chatter to the app logger:
// only genuine retry-give-up errors surface (as warnings); per-attempt
// Info/Debug noise is suppressed.
type leveledLogger struct{}

func (l *leveledLogger) Error(msg string, keysAndValues ...interface{}) {
	logging.Warn("http retry gave up", "detail", msg)
}
func (l *leveledLogger) Warn(msg string, keysAndValues ...interface{}) {
	logging.Warn("http retry gave up", "detail", msg)
}
func (l *leveledLogger) Info(msg string, keysAndValues ...interface{})  {} // suppress retryablehttp chatter
func (l *leveledLogger) Debug(msg string, keysAndValues ...interface{}) {} // suppress retryablehttp chatter

func newResilientDialer() *net.Dialer {
	publicDNSServers := [...]string{
		"1.1.1.1:53",
		"1.0.0.1:53",
		"8.8.8.8:53",
		"8.8.4.4:53",
		"9.9.9.9:53",
	}
	fallbackResolver := &net.Resolver{
		PreferGo:     true,
		StrictErrors: false,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			type dialResult struct {
				conn net.Conn
				err  error
			}
			raceCtx, raceCancel := context.WithCancel(ctx)
			defer raceCancel()

			total := len(publicDNSServers) * 2
			ch := make(chan dialResult, total)
			for _, proto := range []string{"tcp", "udp"} {
				for _, server := range publicDNSServers {
					proto, server := proto, server
					go func() {
						d := net.Dialer{Timeout: 4 * time.Second}
						conn, err := d.DialContext(raceCtx, proto, server)
						if err == nil {
							select {
							case ch <- dialResult{conn: conn}:
							default:
								conn.Close()
							}
							return
						}
						select {
						case ch <- dialResult{err: err}:
						case <-raceCtx.Done():
						}
					}()
				}
			}

			var lastErr error
			for range total {
				select {
				case res := <-ch:
					if res.conn != nil {
						raceCancel()
						return res.conn, nil
					}
					lastErr = res.err
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return nil, fmt.Errorf("public dns lookup failed: %w", lastErr)
		},
	}

	dualResolver := &net.Resolver{
		PreferGo:     true,
		StrictErrors: false,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if runtime.GOOS == "android" {
				return fallbackResolver.Dial(ctx, network, address)
			}
			d := net.Dialer{Timeout: 3 * time.Second}
			conn, err := d.DialContext(ctx, network, address)
			if err == nil {
				return conn, nil
			}
			return fallbackResolver.Dial(ctx, network, address)
		},
	}

	return &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Resolver:  dualResolver,
	}
}
