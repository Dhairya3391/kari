// Package manga caches manga/comic artwork (covers and chapter pages)
// addressed by URL: raw bytes on disk under ~/.config/kari/manga so
// repeat reads skip the network, plus a small decoded-image LRU so page
// turns never re-decode. All downloads go through the shared HTTP client
// with per-page headers supplied by the provider.
package manga

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/util"
)

// mangaLog scopes every line from this package with its identity.
var mangaLog = logging.With("component", "manga")

// maxCachedPages caps decoded page images in memory. Pages are large
// (a 1000px page is several MB decoded), so this stays small — the disk
// cache below is the real store, this only smooths page turns.
const maxCachedPages = 12

// fetchTimeout bounds a single page-image download.
const fetchTimeout = 20 * time.Second

// maxDiskCacheBytes caps the on-disk page cache: about a hundred
// full-quality pages. Beyond that the oldest are evicted at startup.
const maxDiskCacheBytes = 256 << 20

// PruneDiskCache evicts the oldest cached pages until the on-disk page
// cache fits its cap. Best-effort: call once at startup.
func PruneDiskCache() error {
	dir, err := cacheDir()
	if err != nil {
		return err
	}
	removed, err := util.PruneDirToSize(dir, maxDiskCacheBytes)
	if err != nil {
		return err
	}
	if removed > 0 {
		mangaLog.Debug("pruned page cache", "removed", removed)
	}
	return nil
}

// Client fetches and caches manga/comic images by URL.
type Client struct {
	http  *http.Client
	pages *util.BoundedCache[image.Image]
}

// NewClient builds a manga image Client.
func NewClient() *Client {
	return &Client{
		http:  httpclient.New(),
		pages: util.NewBoundedCache[image.Image](maxCachedPages),
	}
}

// FetchImage returns the decoded image for a manga page (or cover URL),
// from memory, then disk, then network. page carries the per-host
// request headers the URL needs. When the primary URL fails with
// 404/5xx and a FallbackURL is set, the fallback is tried before
// giving up — each URL caches under its own key so a recovered primary
// is picked up again later.
func (c *Client) FetchImage(ctx context.Context, page provider.MangaPage) (image.Image, error) {
	urls := []string{page.URL}
	if page.FallbackURL != "" && page.FallbackURL != page.URL {
		urls = append(urls, page.FallbackURL)
	}
	var lastErr error
	for _, u := range urls {
		if img, ok := c.pages.Get(cacheKey(u)); ok {
			return img, nil
		}
		if data, err := readDiskCache(cacheKey(u)); err == nil {
			if img, derr := decode(data); derr == nil {
				c.pages.Set(cacheKey(u), img)
				return img, nil
			}
		}
		data, err := c.download(ctx, page, u)
		if err != nil {
			lastErr = err
			if retryableStatus(err) {
				continue
			}
			return nil, err
		}
		img, err := decode(data)
		if err != nil {
			return nil, err
		}
		if werr := writeDiskCache(cacheKey(u), data); werr != nil {
			mangaLog.Debug("page disk cache write failed", "url", u, "err", werr)
		}
		c.pages.Set(cacheKey(u), img)
		return img, nil
	}
	return nil, lastErr
}

// decode decodes raw image bytes.
func decode(data []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("manga: decode failed: %w", err)
	}
	return img, nil
}

// retryableStatus reports whether a download error is worth retrying
// against the fallback URL: missing or erroring uploads, not transport
// or client errors.
func retryableStatus(err error) bool {
	var httpErr *provider.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Code == http.StatusNotFound || httpErr.Code >= 500
	}
	return false
}

// download fetches raw image bytes for one URL with the page's headers,
// defaulting to a desktop UA when the provider set none.
func (c *Client) download(ctx context.Context, page provider.MangaPage, rawURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	ua := page.UserAgent
	if ua == "" {
		ua = config.DesktopUserAgent
	}
	req.Header.Set("User-Agent", ua)
	if page.Referer != "" {
		req.Header.Set("Referer", page.Referer)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("manga: download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: rawURL}
	}
	return io.ReadAll(resp.Body)
}

// cacheKey hashes the URL so hostile file names (query strings,
// path traversal) can never escape the cache directory.
func cacheKey(rawURL string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(rawURL)))
	return hex.EncodeToString(sum[:])
}

// cacheDir resolves (creating) the on-disk page cache.
func cacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	baseDir := filepath.Join(home, ".config", "kari")
	newDir := filepath.Join(baseDir, "cache", "manga", "pages")
	legacyDir := filepath.Join(baseDir, "manga")

	if _, err := os.Stat(newDir); err == nil {
		return newDir, nil
	}
	if _, err := os.Stat(legacyDir); err == nil {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(baseDir, "cache", "manga")), 0o755)
		if err := os.Rename(legacyDir, filepath.Join(baseDir, "cache", "manga")); err == nil {
			if err := os.MkdirAll(newDir, 0o755); err == nil {
				return newDir, nil
			}
		}
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		return "", err
	}
	return newDir, nil
}

func readDiskCache(key string) ([]byte, error) {
	dir, err := cacheDir()
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(dir, key+".img"))
}

func writeDiskCache(key string, data []byte) error {
	dir, err := cacheDir()
	if err != nil {
		return err
	}
	return util.AtomicWriteFile(filepath.Join(dir, key+".img"), data, 0o644)
}
