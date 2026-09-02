package camoufoxnative

import (
	"reflect"
	"testing"
)

func TestPointerElementClickParamsUsesElementOrigin(t *testing.T) {
	got := pointerElementClickParams("ctx-1", "shared-1")
	want := map[string]any{
		"context": "ctx-1",
		"actions": []any{
			map[string]any{
				"type":       "pointer",
				"id":         "mouse",
				"parameters": map[string]any{"pointerType": "mouse"},
				"actions": []any{
					map[string]any{
						"type":   "pointerMove",
						"origin": map[string]any{"type": "element", "element": map[string]any{"sharedId": "shared-1"}},
						"x":      0,
						"y":      0,
					},
					map[string]any{"type": "pointerDown", "button": 0},
					map[string]any{"type": "pointerUp", "button": 0},
				},
			},
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pointerElementClickParams() = %#v, want %#v", got, want)
	}
}
