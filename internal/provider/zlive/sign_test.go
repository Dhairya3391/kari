package zlive

import (
	"testing"
	"time"
)

// goldenVectors were produced by executing zlive.st's own browser signing
// code with a fixed date, IV, and session ticket.
var goldenVectors = []struct {
	name    string
	payload string
	a       string
	c       string
}{
	{
		name:    "primary",
		payload: `{"s":"skysportsf1-uk2","u":1794935691}`,
		a:       "FHi3ABLwHx2yMJ0ADYZCO6lD1o2iDZ3f4u3hyVqfgyLeDMvbNIU=",
		c:       "zSi6tUNpl7kj+7ISZUTSMg==",
	},
	{
		name:    "backup",
		payload: `{"s":"TSN4","u":1794935691}`,
		a:       "FHi3ABLwOCWFd89DXYcTZ6lZmtKpHITLrv6m",
		c:       "zlmIxTdWrrXeG3u0Ni2vrg==",
	},
}

func TestSignRequestWithIV(t *testing.T) {
	iv := make([]byte, zliveIVBytes)
	for i := range iv {
		iv[i] = byte(i)
	}
	for _, tc := range goldenVectors {
		t.Run(tc.name, func(t *testing.T) {
			got := signRequestWithIV("AAAAAAAAAAAAAAAAAAAAAA", []byte(tc.payload), "2026-11-17", iv)
			if got.A != tc.a {
				t.Errorf("ciphertext = %q, want %q", got.A, tc.a)
			}
			if got.B != "AAECAwQFBgcICQoL" {
				t.Errorf("IV = %q", got.B)
			}
			if got.C != tc.c {
				t.Errorf("tag = %q, want %q", got.C, tc.c)
			}
			if got.D != "2026-11-17" || got.E != "AAAAAAAAAAAAAAAAAAAAAA" {
				t.Errorf("metadata = %q / %q", got.D, got.E)
			}
		})
	}
}

func TestSigningDate(t *testing.T) {
	got := signingDate(time.Date(2026, time.November, 17, 23, 59, 0, 0, time.FixedZone("UTC+5", 5*3600)))
	if got != "2026-11-17" {
		t.Fatalf("signingDate = %q, want 2026-11-17", got)
	}
}

func TestIncrementCounter(t *testing.T) {
	counter := []byte{0, 0, 0, 1}
	incrementCounter(counter)
	if string(counter) != string([]byte{0, 0, 0, 2}) {
		t.Fatalf("counter = %v", counter)
	}
	counter = []byte{0, 255, 255, 255}
	incrementCounter(counter)
	if string(counter) != string([]byte{1, 0, 0, 0}) {
		t.Fatalf("carry counter = %v", counter)
	}
}
