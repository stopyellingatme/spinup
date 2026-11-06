package jobs

import (
	"context"
	"strings"
	"testing"

	"github.com/riverqueue/river/rivertype"
)

func TestPayloadLimitHooks(t *testing.T) {
	cfg := newConfig(nil).river()
	var insert rivertype.HookInsertBegin
	var work rivertype.HookWorkBegin
	for _, plugin := range cfg.Plugins {
		if hook, ok := plugin.(rivertype.HookInsertBegin); ok {
			insert = hook
		}
		if hook, ok := plugin.(rivertype.HookWorkBegin); ok {
			work = hook
		}
	}
	if insert == nil || work == nil {
		t.Fatal("missing payload limits before insertion and worker decoding")
	}
	for _, size := range []int{1024 * 1024, 1024*1024 + 1} {
		payload := []byte(strings.Repeat("x", size))
		for _, err := range []error{
			insert.InsertBegin(context.Background(), &rivertype.JobInsertParams{EncodedArgs: payload}),
			work.WorkBegin(context.Background(), &rivertype.JobRow{EncodedArgs: payload}),
		} {
			if size == 1024*1024 && err != nil {
				t.Errorf("payload at the limit: %v", err)
			}
			if size > 1024*1024 && (err == nil || !strings.Contains(err.Error(), "\nTry: ")) {
				t.Errorf("oversized payload: %v", err)
			}
		}
	}
}

func TestPayloadLimitConfiguration(t *testing.T) {
	for _, limit := range []int{-1, 0, 32} {
		cfg := newConfig([]Option{WithMaxPayloadBytes(limit)})
		want := limit
		if want <= 0 {
			want = DefaultMaxPayloadBytes
		}
		if cfg.maxPayloadBytes != want {
			t.Errorf("limit %d: got %d, want %d", limit, cfg.maxPayloadBytes, want)
		}
		for _, plugin := range cfg.river().Plugins {
			if hook, ok := plugin.(rivertype.HookInsertBegin); ok {
				if err := hook.InsertBegin(context.Background(), &rivertype.JobInsertParams{EncodedArgs: make([]byte, want+1)}); err == nil {
					t.Errorf("limit %d: insert hook accepted oversized arguments", limit)
				}
			}
			if hook, ok := plugin.(rivertype.HookWorkBegin); ok {
				if err := hook.WorkBegin(context.Background(), &rivertype.JobRow{EncodedArgs: make([]byte, want+1)}); err == nil {
					t.Errorf("limit %d: worker hook accepted oversized arguments", limit)
				}
			}
		}
	}
}
