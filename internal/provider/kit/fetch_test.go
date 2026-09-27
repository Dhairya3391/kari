package kit

// FetchFirst fallback-loop coverage: first hit with streams wins,
// misses and failures fall through, typed errors surface.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"kari/internal/provider"
)

type fetchPayload struct {
	Streams []string `json:"streams"`
}

// TestFetchFirstFallbackOrder proves the first URL with streams wins and
// empty payloads fall through to later servers.
func TestFetchFirstFallbackOrder(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/empty", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"streams":[]}`))
	})
	mux.HandleFunc("/full", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"streams":["https://cdn.example.com/x.m3u8"]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got, err := FetchFirst(context.Background(), srv.Client(),
		[]string{srv.URL + "/empty", srv.URL + "/full"}, "test", func(p fetchPayload) bool {
			return len(p.Streams) > 0
		})
	if err != nil {
		t.Fatalf("FetchFirst: %v", err)
	}
	if len(got.Streams) != 1 {
		t.Errorf("streams = %v", got.Streams)
	}
}

// TestFetchFirstErrorMapping proves 404 maps to ErrNotFound, 500 to a
// typed HTTPError, and total misses to ErrNoSources — never silent
// empties.
func TestFetchFirstErrorMapping(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/gone", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/broke", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/bad", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("{not json"))
	})
	mux.HandleFunc("/dry", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"streams":[]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	has := func(p fetchPayload) bool { return len(p.Streams) > 0 }

	if _, err := FetchFirst(context.Background(), srv.Client(), []string{srv.URL + "/gone"}, "t", has); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("404 err = %v, want ErrNotFound", err)
	}
	if _, err := FetchFirst(context.Background(), srv.Client(), []string{srv.URL + "/broke"}, "t", has); !isHTTPError(err, http.StatusInternalServerError) {
		t.Errorf("500 err = %v, want typed 500", err)
	}
	if _, err := FetchFirst(context.Background(), srv.Client(), []string{srv.URL + "/bad"}, "t", has); err == nil {
		t.Error("malformed JSON must error, not read as empty")
	}
	if _, err := FetchFirst(context.Background(), srv.Client(), []string{srv.URL + "/dry"}, "t", has); !errors.Is(err, provider.ErrNoSources) {
		t.Errorf("dry err = %v, want ErrNoSources", err)
	}
}

// TestHandleValid proves the route carries enough to build watch URLs.
func TestHandleValid(t *testing.T) {
	if (Handle{AniListID: "21", Number: 1}).Valid() != true {
		t.Error("complete handle must be valid")
	}
	if (Handle{Number: 1}).Valid() != false {
		t.Error("missing id must be invalid")
	}
	if (Handle{AniListID: "21"}).Valid() != false {
		t.Error("missing number must be invalid")
	}
}

func isHTTPError(err error, code int) bool {
	var httpErr *provider.HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	return httpErr.Code == code
}
