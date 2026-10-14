package main

import (
	"reflect"
	"testing"
)

func TestTranslate(t *testing.T) {
	cases := []struct {
		typ, code uint16
		value     int32
		want      map[string]any
	}{
		{evKey, 1, 1, map[string]any{"input": "front", "action": "press", "t": int64(5)}},
		{evKey, 28, 0, map[string]any{"input": "knob", "action": "release", "t": int64(5)}},
		{evKey, 50, 1, map[string]any{"input": "mute", "action": "press", "t": int64(5)}},
		{evKey, 5, 1, map[string]any{"input": "preset-4", "action": "press", "t": int64(5)}},
		{evRel, relHWhee, -1, map[string]any{"input": "knob", "action": "turn", "delta": -1, "t": int64(5)}},
		{evKey, 28, 2, nil}, // key repeat
		{evKey, 99, 1, nil}, // unknown key
		{0, 0, 0, nil},      // sync marker
	}
	for _, c := range cases {
		if got := translate(c.typ, c.code, c.value, 5); !reflect.DeepEqual(got, c.want) {
			t.Errorf("translate(%d, %d, %d) = %v, want %v", c.typ, c.code, c.value, got, c.want)
		}
	}
}
