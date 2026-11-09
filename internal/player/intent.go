package player

import (
	"net/url"
	"path/filepath"
)

// intentDataURI converts a local filesystem path into a file:// URI for an
// activity-manager -d argument. Android intent filters match on the URI
// scheme and a bare path has none, so players such as mpv-android resolve no
// activity for it ("unable to resolve Intent ... dat="). Values that already
// carry a scheme (http, https, content, ...) are returned unchanged.
func intentDataURI(raw string) string {
	if !filepath.IsAbs(raw) {
		return raw
	}
	return (&url.URL{Scheme: "file", Path: raw}).String()
}
