package miruro

import (
	"bytes"
	"encoding/json"
	"fmt"

	"kari/internal/provider"
)

// This file holds the minimal hydrator for SvelteKit __data.json payloads,
// which serialize with devalue: values live in a flat array and composite
// values reference siblings by numeric index. Only the shapes Miruro emits
// are supported: dicts (string keys), lists, strings, numbers, booleans,
// and null. Numbers that fit the array are references; out-of-range
// numbers, booleans, strings, and null are literals.

// svelteDoc is one __data.json line: either the initial data document or
// a streamed chunk (watch responses arrive as data + chunks).
type svelteDoc struct {
	Type string `json:"type"`
	// Nodes holds the initial document's per-route payloads.
	Nodes []svelteNode `json:"nodes"`
	// ID and Data hold a streamed chunk's payload array.
	ID   int   `json:"id"`
	Data []any `json:"data"`
	// Location carries the canonical URL of a redirect document.
	Location string `json:"location"`
}

// svelteNode is one route payload of the initial document.
type svelteNode struct {
	Type string `json:"type"`
	Data []any  `json:"data"`
}

// parseDoc decodes lines of a __data.json body into documents. Search
// responses are a single JSON object; watch responses stream the initial
// document plus chunks as newline-delimited JSON.
func parseDoc(body []byte) ([]svelteDoc, error) {
	var docs []svelteDoc
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var doc svelteDoc
		if err := json.Unmarshal(line, &doc); err != nil {
			return nil, fmt.Errorf("miruro: decode data document: %w", err)
		}
		docs = append(docs, doc)
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("miruro: empty data document: %w", provider.ErrUpstreamChanged)
	}
	return docs, nil
}

// hydrator resolves devalue index pointers inside one flat value array.
type hydrator struct {
	arr []any
}

// hydrate resolves index i to its final value: composites are expanded
// one level (their children resolve recursively), primitives return
// as-is. Out-of-range indices arrive back untouched so callers can tell
// "reference to nothing" apart from a decoded value.
func (h hydrator) hydrate(i int) any {
	if i < 0 || i >= len(h.arr) {
		return i
	}
	return h.value(h.arr[i], 0)
}

// ref resolves a composite member: in-range numbers are pointers, all
// other values are literals.
func (h hydrator) ref(v any, depth int) any {
	if n, ok := toInt(v); ok && n >= 0 && n < len(h.arr) {
		return h.value(h.arr[n], depth)
	}
	return v
}

// value expands one array slot with a recursion cap: devalue graphs are
// trees (provider configs reference parents by name, not by pointer),
// so depth overflow means a corrupt payload rather than a cycle.
func (h hydrator) value(v any, depth int) any {
	if depth > 32 {
		return nil
	}
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, child := range t {
			out[k] = h.ref(child, depth+1)
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, child := range t {
			out = append(out, h.ref(child, depth+1))
		}
		return out
	default:
		return v
	}
}

// toInt converts JSON numbers (float64) to int without accepting
// fractions or non-numeric values.
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
	case int:
		return n, true
	}
	return 0, false
}

// asMap returns v when it decoded to an object.
func asMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

// asList returns v when it decoded to an array.
func asList(v any) ([]any, bool) {
	l, ok := v.([]any)
	return l, ok
}

// asString returns v trimmed when it decoded to a string.
func asString(v any) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	return s, true
}
