package live

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFrames(t *testing.T) {
	c := Cursor{1731500000000, 42}
	for _, tt := range []struct {
		name string
		got  []byte
		want string
	}{
		{"patch", patchFrame(c.Epoch, 43, []byte(`[{"op":"delete","id":"3"}]`)), "event: patch\nid: 1731500000000-43\ndata: {\"epoch\":1731500000000,\"seq\":43,\"changes\":[{\"op\":\"delete\",\"id\":\"3\"}]}\n\n"},
		{"ready resumed", readyFrame(c, true), "event: ready\nid: 1731500000000-42\ndata: {\"epoch\":1731500000000,\"seq\":42,\"resumed\":true}\n\n"},
		{"ready new", readyFrame(Cursor{}, false), "event: ready\nid: 0-0\ndata: {\"epoch\":0,\"seq\":0,\"resumed\":false}\n\n"},
		{"sync", syncFrame(c, "gap"), "event: sync\ndata: {\"epoch\":1731500000000,\"seq\":42,\"reason\":\"gap\"}\n\n"},
		{"bye", byeFrame("age"), "event: bye\ndata: {\"reason\":\"age\"}\n\n"},
		{"ping", pingFrame, ": ping\n\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.got) != tt.want {
				t.Fatalf("got %q; want %q", tt.got, tt.want)
			}
		})
	}
	for _, reason := range []string{"epoch", "ahead", "gap", "slow", "reset", "age", "drain"} {
		for _, b := range [][]byte{syncFrame(c, reason), byeFrame(reason)} {
			_, data, _ := strings.Cut(string(b), "data: ")
			if !json.Valid([]byte(strings.TrimSuffix(data, "\n\n"))) {
				t.Fatalf("invalid JSON: %q", b)
			}
		}
	}
}
