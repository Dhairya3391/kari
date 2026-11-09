package anidream

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// AniDream is a TanStack Start application: every catalog, playback, and
// reader call goes through a hashed /_serverFn endpoint whose input and
// output are encoded with Seroval, not plain JSON. The encoder below covers
// the subset Kari sends (objects, arrays, strings, numbers, booleans, null);
// the decoder covers the subset the server returns, including the error
// envelope TanStack wraps around every handler result.
//
// Reference: Seroval node tags are stable wire values, not an implementation
// detail of one bundle.

// serovalEncoder assigns the reference ids Seroval needs for cyclic graphs.
// Kari's payloads are trees, so ids are simply allocated in traversal order.
type serovalEncoder struct {
	next int
}

// encode converts a Go value into a Seroval node.
func (e *serovalEncoder) encode(value any) any {
	switch v := value.(type) {
	case nil:
		return map[string]any{"t": 2, "s": 0}
	case bool:
		special := 3
		if v {
			special = 2
		}
		return map[string]any{"t": 2, "s": special}
	case int:
		return map[string]any{"t": 0, "s": v}
	case int64:
		return map[string]any{"t": 0, "s": v}
	case float64:
		return map[string]any{"t": 0, "s": v}
	case float32:
		return map[string]any{"t": 0, "s": v}
	case string:
		return map[string]any{"t": 1, "s": v}
	case []any:
		id := e.next
		e.next++
		items := make([]any, len(v))
		for i, item := range v {
			items[i] = e.encode(item)
		}
		return map[string]any{"t": 9, "i": id, "a": items, "o": 0}
	case map[string]any:
		id := e.next
		e.next++
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		values := make([]any, len(keys))
		for i, key := range keys {
			values[i] = e.encode(v[key])
		}
		return map[string]any{
			"t": 10,
			"i": id,
			"p": map[string]any{"k": keys, "v": values},
			"o": 0,
		}
	default:
		panic(fmt.Sprintf("anidream: unsupported Seroval value %T", value))
	}
}

// marshalServerFnPayload wraps an argument object in the request envelope
// TanStack expects. Sending the bare node makes the server reject the call
// with "Seroval Error (step: 3)".
func marshalServerFnPayload(argument map[string]any) ([]byte, error) {
	encoder := &serovalEncoder{}
	return json.Marshal(map[string]any{
		"t": encoder.encode(argument),
		"f": 0,
		"m": []any{},
	})
}

// serovalDecoder resolves Seroval reference ids back into ordinary Go
// values. Only tags AniDream emits are handled; unknown tags are returned
// as their raw node so an upstream addition cannot crash a search.
type serovalDecoder struct {
	refs map[int]any
}

// decode converts one Seroval node into a Go value.
func (d *serovalDecoder) decode(node any) (any, error) {
	m, ok := node.(map[string]any)
	if !ok {
		return node, nil
	}
	tag, _ := m["t"].(float64)
	switch int(tag) {
	case 0, 1:
		return m["s"], nil
	case 2:
		return decodeSpecial(m["s"]), nil
	case 3:
		// Dates are transported as their ISO string, which is already the
		// representation every caller wants.
		return m["s"], nil
	case 9:
		return d.decodeArray(m)
	case 10, 11:
		return d.decodeObject(m)
	case 25:
		// Error nodes carry the error's fields directly in "s"; each field
		// value is itself a Seroval node.
		inner, ok := m["s"].(map[string]any)
		if !ok {
			return nil, nil
		}
		out := make(map[string]any, len(inner))
		for key, value := range inner {
			decoded, err := d.decode(value)
			if err != nil {
				return nil, err
			}
			out[key] = decoded
		}
		return out, nil
	default:
		return node, nil
	}
}

// decodeArray resolves an array node.
func (d *serovalDecoder) decodeArray(m map[string]any) ([]any, error) {
	ref, _ := m["i"].(float64)
	raw, _ := m["a"].([]any)
	items := make([]any, 0, len(raw))
	d.refs[int(ref)] = items
	for _, node := range raw {
		value, err := d.decode(node)
		if err != nil {
			return nil, err
		}
		items = append(items, value)
	}
	d.refs[int(ref)] = items
	return items, nil
}

// decodeObject resolves an object node. Null-prototype objects (tag 11)
// share the same key/value payload as ordinary objects.
func (d *serovalDecoder) decodeObject(m map[string]any) (map[string]any, error) {
	ref, _ := m["i"].(float64)
	out := make(map[string]any)
	d.refs[int(ref)] = out
	payload, _ := m["p"].(map[string]any)
	keys, _ := payload["k"].([]any)
	values, _ := payload["v"].([]any)
	for i, key := range keys {
		name, ok := key.(string)
		if !ok || i >= len(values) {
			continue
		}
		value, err := d.decode(values[i])
		if err != nil {
			return nil, err
		}
		out[name] = value
	}
	d.refs[int(ref)] = out
	return out, nil
}

// decodeSpecial maps Seroval's special-reference table.
func decodeSpecial(raw any) any {
	special, _ := raw.(float64)
	switch int(special) {
	case 0, 1:
		return nil
	case 2:
		return true
	case 3:
		return false
	case 5:
		return math.NaN()
	case 6:
		return math.Inf(1)
	case 7:
		return math.Inf(-1)
	default:
		return nil
	}
}

// decodeServerFnResponse unwraps TanStack's {result,error,context} envelope
// and returns the handler result. A non-null error becomes a Go error.
func decodeServerFnResponse(raw []byte) (any, error) {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("anidream: decode Seroval response: %w", err)
	}
	decoder := &serovalDecoder{refs: make(map[int]any)}
	value, err := decoder.decode(root)
	if err != nil {
		return nil, fmt.Errorf("anidream: decode Seroval node: %w", err)
	}
	envelope, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("anidream: unexpected server function envelope %T", value)
	}
	if errValue := envelope["error"]; errValue != nil {
		return nil, fmt.Errorf("anidream: server function: %s", serovalErrorText(errValue))
	}
	return envelope["result"], nil
}

// serovalErrorText extracts a human-readable message from a decoded error.
func serovalErrorText(value any) string {
	switch v := value.(type) {
	case map[string]any:
		if message, ok := v["message"].(string); ok && message != "" {
			return message
		}
	case string:
		if v != "" {
			return v
		}
	}
	return "unknown upstream error"
}

// decodeResult re-encodes a decoded Seroval value as JSON and unmarshals it
// into a typed response. Going through JSON keeps the typed structs free of
// map[string]any lookups while preserving Seroval's numeric/string shapes.
func decodeResult(result any, out any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("anidream: re-encode result: %w", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("anidream: decode result: %w", err)
	}
	return nil
}

// encodeServerFnResponse is the inverse of decodeServerFnResponse, used by
// tests to serve canned payloads without hand-writing Seroval JSON.
func encodeServerFnResponse(result any) ([]byte, error) {
	encoder := &serovalEncoder{}
	node := encoder.encode(map[string]any{
		"result":  result,
		"error":   nil,
		"context": map[string]any{},
	})
	return json.Marshal(node)
}
