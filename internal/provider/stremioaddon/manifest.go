package stremioaddon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"kari/internal/httpclient"
	"kari/internal/provider"
)

const manifestSuffix = "manifest.json"

// normalizeManifestURL turns a pasted addon address into the URL of its
// manifest. Addon links are commonly shared as a bare base URL, so the
// manifest suffix is appended when missing while the configuration query
// string is preserved.
func normalizeManifestURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid addon manifest URL")
	}
	// url.Parse preserves scheme case; addon links pasted from a browser may
	// carry an uppercase scheme, which is still a valid HTTP(S) address.
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid addon manifest URL")
	}
	u.Fragment = ""
	base := strings.TrimSuffix(u.Path, "/")
	if !strings.HasSuffix(strings.ToLower(base), "/"+manifestSuffix) {
		base += "/" + manifestSuffix
	}
	u.Path = base
	u.RawPath = ""
	return u.String(), nil
}

// fetchManifest downloads and validates an addon manifest. The response body
// is bounded so a misconfigured endpoint cannot exhaust memory, and transport
// failures are classified into redacted provider errors rather than losing
// their cause behind a generic string.
func fetchManifest(ctx context.Context, hc *http.Client, manifestURL string) (manifest, error) {
	u, err := url.Parse(manifestURL)
	if err != nil {
		return manifest{}, fmt.Errorf("parse addon manifest URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return manifest{}, fmt.Errorf("create manifest request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return manifest{}, fetchError(ctx, u.Host, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return manifest{}, fmt.Errorf("fetch addon manifest: %w",
			&provider.HTTPError{Code: resp.StatusCode, URL: u.Host})
	}
	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return manifest{}, fmt.Errorf("read addon manifest: %w", err)
	}
	var info manifest
	if err := json.Unmarshal(body, &info); err != nil {
		if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "html") {
			return manifest{}, fmt.Errorf("decode addon manifest: endpoint returned HTML, not JSON")
		}
		return manifest{}, fmt.Errorf("decode addon manifest: %w", err)
	}
	return info, nil
}

// fetchError classifies a transport failure without exposing credentials
// that may be embedded in the addon URL's query string.
func fetchError(ctx context.Context, host string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("fetch addon manifest: %w", ctxErr)
	}
	kind := provider.KindUpstream
	hint := "host " + host + " unreachable"
	// The shared HTTP client exhausts retries for 5xx responses and returns a
	// synthetic error, so keep that case distinguishable from DNS failures.
	if strings.Contains(strings.ToLower(err.Error()), "giving up after") {
		hint = "host " + host + " failed after retries"
		return provider.WrapHint("stremioaddon", "fetch manifest", kind, hint, err)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			kind = provider.KindTimeout
			hint = "host " + host + " did not respond in time"
		} else if message := strings.ToLower(urlErr.Err.Error()); strings.Contains(message, "tls") ||
			strings.Contains(message, "certificate") {
			hint = "host " + host + " TLS handshake failed"
		}
	}
	return provider.WrapHint("stremioaddon", "fetch manifest", kind, hint, err)
}

// addonInfo reports the capabilities Kari derived from a manifest so the
// settings screen can describe a configured addon.
func addonInfo(info manifest, modes []provider.Mode) provider.AddonInfo {
	return provider.AddonInfo{
		Catalogs:       supportsResource(info, "catalog") && len(info.Catalogs) > 0,
		Streams:        supportsResource(info, "stream"),
		Subtitles:      supportsResource(info, "subtitles"),
		ConfigRequired: info.Behavior.ConfigurationRequired,
		Modes:          modeNames(modes),
	}
}

func modeNames(modes []provider.Mode) []provider.ContentType {
	names := make([]provider.ContentType, 0, len(modes))
	for _, mode := range modes {
		names = append(names, mode.Name)
	}
	return names
}
