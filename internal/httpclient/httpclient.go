package httpclient

import (
	"context"
	"net"
	"net/http"
	"runtime"
	"time"

	"github.com/hashicorp/go-retryablehttp"

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

	// Termux/Android cross-compiled binaries often fail DNS resolution
	// because they lack /etc/resolv.conf. Use a fallback public DNS.
	if runtime.GOOS == "android" {
		resolver := &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{
					Timeout: 5 * time.Second,
				}
				// Force UDP to Cloudflare DNS
				conn, err := d.DialContext(ctx, "udp", "1.1.1.1:53")
				if err != nil {
					// Fallback to Google DNS
					return d.DialContext(ctx, "udp", "8.8.8.8:53")
				}
				return conn, err
			},
		}

		dialer := &net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			Resolver:  resolver,
		}

		transport.DialContext = dialer.DialContext
	}

	retryClient.HTTPClient.Transport = transport
	retryClient.Logger = &leveledLogger{}
	return retryClient.StandardClient()
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
