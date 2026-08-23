package provider

import (
	"errors"
	"fmt"
)

// Sentinel errors used by providers for common failure cases.
var (
	ErrNoResults  = errors.New("no results found")
	ErrNoEpisodes = errors.New("no episodes found")
	ErrNoSources  = errors.New("no sources found")
	ErrNotFound   = errors.New("not found")
)

// HTTPError represents a non-2xx HTTP response from an upstream API.
type HTTPError struct {
	Code int
	URL  string
}

// Error renders the status code and URL.
func (e *HTTPError) Error() string {
	if e == nil {
		return "http error"
	}
	if e.URL == "" {
		return fmt.Sprintf("http status %d", e.Code)
	}
	return fmt.Sprintf("http status %d for %s", e.Code, e.URL)
}

// StatusCode implements StatusCodedError.
func (e *HTTPError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.Code
}

var _ StatusCodedError = (*HTTPError)(nil)
