package httpclient

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestPerHostConcurrencyCap proves at most 4 requests run concurrently
// against one host while the rest wait on context.
func TestPerHostConcurrencyCap(t *testing.T) {
	var current, maxSeen int64
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&current, 1)
		for {
			m := atomic.LoadInt64(&maxSeen)
			if n <= m || atomic.CompareAndSwapInt64(&maxSeen, m, n) {
				break
			}
		}
		<-release
		atomic.AddInt64(&current, -1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := New()
	const total = 8
	done := make(chan struct{}, total)
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("GET", srv.URL, nil)
			resp, err := client.Do(req)
			if err != nil {
				t.Errorf("Do: %v", err)
				return
			}
			resp.Body.Close()
			done <- struct{}{}
		}()
	}
	// Let the burst arrive, then assert the cap before releasing.
	time.Sleep(300 * time.Millisecond)
	if m := atomic.LoadInt64(&maxSeen); m > maxPerHost {
		t.Errorf("max concurrent = %d, want <= %d", m, maxPerHost)
	}
	close(release)
	wg.Wait()
	if len(done) != total {
		t.Errorf("completed = %d, want %d", len(done), total)
	}
}

// TestSemaphoreRespectsContext proves a queued acquire aborts on cancel.
func TestSemaphoreRespectsContext(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer close(block)

	client := New()
	// Fill all 4 slots with blocked requests.
	for i := 0; i < maxPerHost; i++ {
		go func() {
			req, _ := http.NewRequest("GET", srv.URL, nil)
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	time.Sleep(200 * time.Millisecond)
	req, _ := http.NewRequest("GET", srv.URL, nil)
	cancelled, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req = req.WithContext(cancelled)
	if _, err := client.Do(req); err == nil {
		t.Error("queued acquire must fail on context cancel")
	}
}

// TestGetOnlyRetryPolicy proves POSTs never retry while GETs do.
func TestGetOnlyRetryPolicy(t *testing.T) {
	var gets, posts int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			atomic.AddInt64(&posts, 1)
		} else {
			atomic.AddInt64(&gets, 1)
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := NewWithTimeout(10 * time.Second)
	req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
	if n := atomic.LoadInt64(&posts); n != 1 {
		t.Errorf("POST attempts = %d, want 1 (no retry)", n)
	}
	greq, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	if resp, err := client.Do(greq); err == nil {
		resp.Body.Close()
	}
	if n := atomic.LoadInt64(&gets); n <= 1 {
		t.Errorf("GET attempts = %d, want retries", n)
	}
}

// TestDefaultUserAgent proves every request carries a stable browser UA
// unless the caller overrides it.
func TestDefaultUserAgent(t *testing.T) {
	var got, custom string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/custom" {
			custom = r.Header.Get("User-Agent")
		} else {
			got = r.Header.Get("User-Agent")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := New()
	for _, path := range []string{"/a", "/custom"} {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		if path == "/custom" {
			req.Header.Set("User-Agent", "Custom/1.0")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		resp.Body.Close()
	}
	if got == "" || got == "Go-http-client/2.0" {
		t.Errorf("default UA = %q, want a stable browser UA", got)
	}
	if custom != "Custom/1.0" {
		t.Errorf("override UA = %q, want Custom/1.0", custom)
	}
}

// TestConnectionReuse proves sequential requests to one host reuse a
// single connection (keep-alive).
func TestConnectionReuse(t *testing.T) {
	var mu sync.Mutex
	conns := make(map[string]struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.Config.ConnState = func(c net.Conn, s http.ConnState) {
		if s == http.StateNew {
			mu.Lock()
			conns[c.RemoteAddr().String()] = struct{}{}
			mu.Unlock()
		}
	}
	srv.Start()
	defer srv.Close()

	client := New()
	for i := 0; i < 5; i++ {
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		resp.Body.Close()
	}
	mu.Lock()
	n := len(conns)
	mu.Unlock()
	if n != 1 {
		t.Errorf("sequential requests used %d connections, want 1", n)
	}
}

// TestReadCapped proves bodies over 8 MB error instead of truncating.
func TestReadCapped(t *testing.T) {
	big := make([]byte, MaxBodyBytes+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/big" {
			w.Write(big)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	resp, err := New().Get(srv.URL + "/small")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	body, err := ReadCapped(resp)
	if err != nil || string(body) != `{"ok":true}` {
		t.Errorf("small body = %q, %v", body, err)
	}
	resp2, err := New().Get(srv.URL + "/big")
	if err != nil {
		t.Fatalf("Get big: %v", err)
	}
	if _, err := ReadCapped(resp2); err == nil {
		t.Error("oversized body must error")
	}
}
