package httpclient

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-retryablehttp"

	"kari/internal/config"
	"kari/internal/logging"
	"kari/internal/timeouts"
)

const (
	// defaultTimeout bounds one HTTP attempt. Most callers also carry a
	// service-level context deadline; keeping this short avoids a single
	// stalled host holding a fan-out slot for a full minute.
	defaultTimeout = timeouts.HTTPDefault
	// One retry is enough to ride out a transient 5xx or connection reset.
	// More retries multiply the tail latency of every provider call.
	defaultRetries = 1
	// Tight cap for our own computed backoff so a transient failure cannot
	// stall playback resolution.
	maxRetryWait = 600 * time.Millisecond
	// A server's explicit Retry-After is a direct instruction; honor it, but
	// still bound pathological values (some hosts send minutes).
	maxRetryAfterWait = 5 * time.Second
	// maxPerHost caps concurrent requests to one host so provider fan-out
	// stays polite: 16 in flight per host, the rest wait on context.
	maxPerHost = 16
	// MaxBodyBytes caps JSON/HTML response bodies (8 MB). Media downloads
	// never flow through the capped helpers.
	MaxBodyBytes = 8 << 20
)

var (
	transportOnce   sync.Once
	sharedTransport *http.Transport
	sharedSemaphore *hostSemaphore
)

// sharedBase builds the process-wide transport and per-host semaphore once.
// They are immutable after construction; only their internal pools and
// semaphore channels mutate, which is what lets unrelated clients reuse the
// same idle connections instead of opening fresh TCP+TLS handshakes.
func sharedBase() (*http.Transport, *hostSemaphore) {
	transportOnce.Do(func() {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.ForceAttemptHTTP2 = true
		transport.MaxIdleConns = 200
		transport.MaxIdleConnsPerHost = 32
		transport.IdleConnTimeout = 90 * time.Second
		transport.TLSHandshakeTimeout = timeouts.HTTPTLSHandshake
		transport.ExpectContinueTimeout = time.Second
		transport.DialContext = newResilientDialer().DialContext
		sharedTransport = transport
		sharedSemaphore = &hostSemaphore{
			sems: make(map[string]chan struct{}),
			next: transport,
		}
	})
	return sharedTransport, sharedSemaphore
}

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

// NewWithUserAgentAndTimeout returns a shared HTTP client that injects a User-Agent header and custom timeout.
func NewWithUserAgentAndTimeout(userAgent string, timeout time.Duration) *http.Client {
	client := newClient(timeout)
	client.Transport = &uaRoundTripper{
		next: client.Transport,
		ua:   userAgent,
	}
	return client
}

// NewNoRedirect returns a shared client that leaves 3xx responses for the
// caller to inspect. The default client follows redirects inside
// retryablehttp's transport, so a standard client's CheckRedirect hook never
// sees them; this variant omits that layer and handles the response itself.
func NewNoRedirect() *http.Client {
	return &http.Client{
		Transport: sharedRoundTripper(config.DesktopUserAgent),
		Timeout:   defaultTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// sharedRoundTripper builds the shared, wrapped transport used by every
// client: per-host semaphore, app header, then User-Agent injection.
func sharedRoundTripper(userAgent string) http.RoundTripper {
	_, semaphore := sharedBase()
	var rt http.RoundTripper = semaphore
	rt = &kariClientRoundTripper{next: rt}
	rt = &uaRoundTripper{next: rt, ua: userAgent}
	return rt
}

func newClient(timeout time.Duration) *http.Client {
	retryClient := retryablehttp.NewClient()
	retryClient.RetryMax = defaultRetries
	retryClient.RetryWaitMin = 100 * time.Millisecond
	retryClient.RetryWaitMax = maxRetryWait
	retryClient.Backoff = func(min, max time.Duration, attemptNum int, resp *http.Response) time.Duration {
		wait := retryablehttp.DefaultBackoff(min, max, attemptNum, resp)
		if resp != nil && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) {
			if wait > maxRetryAfterWait {
				return maxRetryAfterWait
			}
			return wait
		}
		if wait > maxRetryWait {
			return maxRetryWait
		}
		return wait
	}
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

	retryClient.HTTPClient.Transport = sharedRoundTripper(config.DesktopUserAgent)
	retryClient.Logger = &leveledLogger{}
	return retryClient.StandardClient()
}

// hostSemaphore caps concurrent requests per host. It is safe for
// concurrent use; waiting acquires respect the request context.
type hostSemaphore struct {
	mu   sync.RWMutex
	sems map[string]chan struct{}
	next http.RoundTripper
}

// slot returns the semaphore for host, creating it on first use. The read
// path dominates under fan-out, so lookups take a read lock and only the
// first request for a host takes the write lock.
func (h *hostSemaphore) slot(host string) chan struct{} {
	h.mu.RLock()
	s, ok := h.sems[host]
	h.mu.RUnlock()
	if ok {
		return s
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sems == nil {
		h.sems = make(map[string]chan struct{})
	}
	if s, ok := h.sems[host]; ok {
		return s
	}
	s = make(chan struct{}, maxPerHost)
	h.sems[host] = s
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

// kariClientRoundTripper tags outgoing requests destined for our own
// Cloudflare-fronted APIs (broggl.farm) as coming from this app to bypass bot
// challenges that otherwise intermittently misclassify the app's non-browser
// TLS fingerprint and return an HTML challenge page instead of JSON.
type kariClientRoundTripper struct {
	next http.RoundTripper
}

func (t *kariClientRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Host, "broggl.farm") {
		req.Header.Set(config.KariClientHeader, config.KariClientToken)
	}
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

// retryablehttp's give-up message embeds the full request URL, which may carry
// API keys or signed CDN tokens.
func (l *leveledLogger) Error(msg string, keysAndValues ...interface{}) {
	logging.Warn("http retry gave up", "detail", logging.Redact(msg))
}
func (l *leveledLogger) Warn(msg string, keysAndValues ...interface{}) {
	logging.Warn("http retry gave up", "detail", logging.Redact(msg))
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

			total := len(publicDNSServers)
			ch := make(chan dialResult, total)
			for _, server := range publicDNSServers {
				go func() {
					d := net.Dialer{Timeout: timeouts.HTTPDial}
					conn, err := d.DialContext(raceCtx, network, server)
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
		PreferGo:     false,
		StrictErrors: false,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if runtime.GOOS == "android" {
				return fallbackResolver.Dial(ctx, network, address)
			}
			d := net.Dialer{Timeout: timeouts.HTTPDialTLS}
			conn, err := d.DialContext(ctx, network, address)
			if err == nil {
				return conn, nil
			}
			return fallbackResolver.Dial(ctx, network, address)
		},
	}

	return &net.Dialer{
		Timeout:   timeouts.HTTPDefault,
		KeepAlive: 30 * time.Second,
		Resolver:  dualResolver,
	}
}
