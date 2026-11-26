package auth

import (
	"bytes"
	"encoding/gob"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
)

func TestSessionCodecGobCompatibility(t *testing.T) {
	gob.Register(time.Time{})
	t.Parallel()
	codec := &sessionCodec{}
	gobCodec := scs.GobCodec{}
	deadline := time.Date(2026, 11, 26, 12, 0, 0, 1, time.UTC)

	// A single entry gives gob a stable map iteration order.
	values := map[string]any{sessionUserKey: int64(42)}
	want, err := gobCodec.Encode(deadline, values)
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Encode(deadline, values)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("the encoded bytes differ from GobCodec")
	}
	checkSessionDecode(t, gobCodec, got, deadline, values)

	values = map[string]any{
		sessionUserKey: int64(42), sessionVersionKey: int64(1),
		"int": int(2), "int32": int32(3), "string": "value",
		"bool": true, "float64": float64(1.5),
	}
	payload := encodeSession(t, deadline, values)
	for range 2 {
		got := checkSessionDecode(t, codec, payload, deadline, values)
		got[sessionUserKey] = int64(99)
		delete(got, sessionVersionKey)
	}
	if len(codec.entries) != 1 {
		t.Fatalf("cache size = %d, want 1", len(codec.entries))
	}

	original := bytes.Clone(payload)
	payload[0] ^= 0xff
	if _, ok := codec.entries[string(original)]; !ok {
		t.Fatal("the cache key changed with the input buffer")
	}
	checkSessionDecode(t, codec, original, deadline, values)
}

func TestSessionCodecSkipsUncacheablePayloads(t *testing.T) {
	t.Parallel()
	codec := &sessionCodec{}
	deadline := time.Now().UTC()
	values := map[string]any{"value": []byte("mutable")}
	payload := encodeSession(t, deadline, values)
	got := checkSessionDecode(t, codec, payload, deadline, values)
	got["value"].([]byte)[0] = 'M'
	checkSessionDecode(t, codec, payload, deadline, values)

	// A time.Time holds a *time.Location, which a cache hit would share.
	values = map[string]any{"value": deadline}
	payload = encodeSession(t, deadline, values)
	checkSessionDecode(t, codec, payload, deadline, values)

	values = map[string]any{"value": strings.Repeat("x", sessionCacheMaxBytes)}
	payload = encodeSession(t, deadline, values)
	checkSessionDecode(t, codec, payload, deadline, values)
	if len(codec.entries) != 0 {
		t.Fatalf("cache size = %d, want 0", len(codec.entries))
	}
}

func TestSessionCodecEviction(t *testing.T) {
	t.Parallel()
	codec := &sessionCodec{}
	deadline := time.Now().UTC()
	var first, last []byte
	for id := range sessionCacheMaxEntries + 1 {
		values := map[string]any{sessionUserKey: int64(id)}
		last = encodeSession(t, deadline, values)
		if id == 0 {
			first = last
		}
		checkSessionDecode(t, codec, last, deadline, values)
	}
	if len(codec.entries) != sessionCacheMaxEntries {
		t.Fatalf("cache size = %d, want %d", len(codec.entries), sessionCacheMaxEntries)
	}
	if _, ok := codec.entries[string(first)]; ok {
		t.Fatal("the cache still has the oldest entry")
	}
	if _, ok := codec.entries[string(last)]; !ok {
		t.Fatal("the cache lost the newest entry")
	}
	checkSessionDecode(t, codec, first, deadline, map[string]any{sessionUserKey: int64(0)})
}

func TestSessionCodecConcurrent(t *testing.T) {
	t.Parallel()
	codec := &sessionCodec{}
	deadline := time.Now().UTC()
	payloads := make([][]byte, 16)
	for id := range payloads {
		payloads[id] = encodeSession(t, deadline, map[string]any{sessionUserKey: int64(id)})
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 10 {
				for id, payload := range payloads {
					gotDeadline, values, err := codec.Decode(payload)
					if err != nil || !gotDeadline.Equal(deadline) || values[sessionUserKey] != int64(id) {
						t.Errorf("Decode() = %v, %v, %v, want user ID %d and deadline %v", gotDeadline, values, err, id, deadline)
						return
					}
					delete(values, sessionUserKey)
				}
			}
		})
	}
	wg.Wait()
	if len(codec.entries) != len(payloads) {
		t.Fatalf("cache size = %d, want %d", len(codec.entries), len(payloads))
	}
}

func TestSessionCodecCorruptPayload(t *testing.T) {
	t.Parallel()
	codec := &sessionCodec{}
	payload := []byte("invalid gob")
	_, _, wantErr := (scs.GobCodec{}).Decode(payload)
	deadline, values, err := codec.Decode(payload)
	if err == nil || wantErr == nil || err.Error() != wantErr.Error() || !deadline.IsZero() || values != nil {
		t.Fatalf("Decode() = %v, %v, %v, want zero deadline, nil values, and error %v", deadline, values, err, wantErr)
	}
	if len(codec.entries) != 0 {
		t.Fatal("the cache has an entry for the corrupt payload")
	}
}

func checkSessionDecode(t *testing.T, codec scs.Codec, payload []byte, deadline time.Time, values map[string]any) map[string]any {
	t.Helper()
	gotDeadline, gotValues, err := codec.Decode(payload)
	if err != nil || !gotDeadline.Equal(deadline) || !reflect.DeepEqual(gotValues, values) {
		t.Fatalf("Decode() = %v, %v, %v, want %v, %v, nil", gotDeadline, gotValues, err, deadline, values)
	}
	return gotValues
}

func encodeSession(tb testing.TB, deadline time.Time, values map[string]any) []byte {
	tb.Helper()
	payload, err := (scs.GobCodec{}).Encode(deadline, values)
	if err != nil {
		tb.Fatal(err)
	}
	return payload
}

func BenchmarkSessionDecode(b *testing.B) {
	payload := encodeSession(b, time.Now().UTC(), map[string]any{sessionUserKey: int64(42), sessionVersionKey: int64(1)})
	cache := &sessionCodec{}
	if _, _, err := cache.Decode(payload); err != nil {
		b.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		codec scs.Codec
	}{
		{"Gob", scs.GobCodec{}},
		{"Cached", cache},
	} {
		b.Run(tc.name, func(b *testing.B) {
			for b.Loop() {
				if _, _, err := tc.codec.Decode(payload); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	b.Run("CachedParallel", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, _, err := cache.Decode(payload); err != nil {
					b.Error(err)
					return
				}
			}
		})
	})
}
