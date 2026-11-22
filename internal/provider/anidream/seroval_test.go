package anidream

import "testing"

func TestMarshalServerFnPayloadRejectsUnsupportedType(t *testing.T) {
	t.Parallel()
	_, err := marshalServerFnPayload(map[string]any{
		"data": map[string]any{"bad": make(chan int)},
	})
	if err == nil {
		t.Fatal("expected an error for an unsupported Seroval value")
	}
}
