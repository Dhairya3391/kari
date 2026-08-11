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
	// that fan out several concurrent requests to their own host (e.g.
	// wco's query-variant search and index-page fallback both run multiple
	// requests to the same host in parallel) — with only 2 idle connections
	// kept warm, a burst beyond that forces extra TCP+TLS handshakes instead
	// of reusing connections.
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 16
	transport.IdleConnTimeout = 90 * time.Second

	// Termux/Android cross-compiled binaries often fail DNS resolution
	// because they lack a working /etc/resolv.conf (the stub resolver on
	// [::1]:53 refuses connections). Dial public resolvers directly instead.
	// TCP is tried first because a TCP handshake proves the server is
	// reachable — UDP connects always "succeed" even when the server is
	// unreachable, so a UDP-only fallback chain never triggers on timeouts.
	if runtime.GOOS == "android" {
		resolver := &net.Resolver{
			PreferGo:     true,
			StrictErrors: false,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{Timeout: 5 * time.Second}
				var lastErr error
				for _, proto := range []string{"tcp", "udp"} {
					for _, server := range []string{"1.1.1.1:53", "8.8.8.8:53", "8.8.4.4:53", "9.9.9.9:53"} {
						conn, err := d.DialContext(ctx, proto, server)
						if err == nil {
							return conn, nil
						}
						lastErr = err
					}
				}
				return nil, fmt.Errorf("no reachable public DNS server: %w", lastErr)
			},
		}

		dialer := &net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			Resolver:  resolver,
		}

		transport.DialContext = dialer.DialContext
	}

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

type leveledLogger struct{}

func (l *leveledLogger) Error(msg string, keysAndValues ...interface{}) {
	logging.Warnf("[http] %s", msg)
}
func (l *leveledLogger) Warn(msg string, keysAndValues ...interface{}) {
	logging.Warnf("[http] %s", msg)
}
func (l *leveledLogger) Info(msg string, keysAndValues ...interface{})  {} // suppress retryablehttp chatter
func (l *leveledLogger) Debug(msg string, keysAndValues ...interface{}) {} // suppress retryablehttp chatter
