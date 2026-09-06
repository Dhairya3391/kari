package httpclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"time"

	"github.com/hashicorp/go-retryablehttp"

	"kari/internal/config"
	"kari/internal/logging"
)

const (
	defaultTimeout = 30 * time.Second
	defaultRetries = 3
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
	retryClient.RetryWaitMin = 500 * time.Millisecond
	retryClient.RetryWaitMax = 3 * time.Second
	retryClient.HTTPClient.Timeout = timeout

	transport := http.DefaultTransport.(*http.Transport).Clone()

	// Go's default MaxIdleConnsPerHost is 2, which is too low for providers
	// that fan out several concurrent requests to their own host (e.g. the
	// streaming providers' parallel multi-quality resolves) — with only 2
	// idle connections kept warm, a burst beyond that forces extra TCP+TLS
	// handshakes instead of reusing connections.
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 16
	transport.IdleConnTimeout = 90 * time.Second

	// Universal resilient DNS dialer: uses system DNS first, falling back to
	// public DNS (Cloudflare 1.1.1.1/1.0.0.1, Google 8.8.8.8/8.8.4.4) over
	// UDP/TCP if system DNS is blocked, hijacked, or fails.
	dialer := newResilientDialer()
	transport.DialContext = dialer.DialContext

	retryClient.HTTPClient.Transport = &kariClientRoundTripper{next: transport}
	retryClient.Logger = &leveledLogger{}
	return retryClient.StandardClient()
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
			d := net.Dialer{Timeout: 4 * time.Second}
			var lastErr error
			// A UDP dial only creates a local socket; it does not prove that
			// a resolver is reachable. Prefer TCP so a successful connection
			// reflects a reachable DNS server on networks that drop UDP/53.
			for _, proto := range []string{"tcp", "udp"} {
				for _, server := range publicDNSServers {
					conn, err := d.DialContext(ctx, proto, server)
					if err == nil {
						return conn, nil
					}
					lastErr = err
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
