package auth

import (
	"maps"
	"sync"
	"time"

	"github.com/alexedwards/scs/v2"
)

const (
	sessionCacheMaxEntries = 4096
	sessionCacheMaxBytes   = 1024
)

type decodedSession struct {
	deadline time.Time
	values   map[string]any
}

// sessionCodec caches gob decodes by payload. The store supplies the payload
// and the encoded deadline.
type sessionCodec struct {
	mu      sync.Mutex
	entries map[string]decodedSession
	order   []string
	oldest  int
}

func (*sessionCodec) Encode(deadline time.Time, values map[string]any) ([]byte, error) {
	return (scs.GobCodec{}).Encode(deadline, values)
}

func (c *sessionCodec) Decode(b []byte) (time.Time, map[string]any, error) {
	gobCodec := scs.GobCodec{}
	if len(b) > sessionCacheMaxBytes {
		return gobCodec.Decode(b)
	}

	c.mu.Lock()
	entry, ok := c.entries[string(b)]
	c.mu.Unlock()
	if ok {
		return entry.deadline, maps.Clone(entry.values), nil
	}

	deadline, values, err := gobCodec.Decode(b)
	if err == nil && cacheableSessionValues(values) {
		c.store(b, deadline, values)
	}
	return deadline, values, err
}

func cacheableSessionValues(values map[string]any) bool {
	for _, value := range values {
		switch value.(type) {
		case int64, int, int32, string, bool, float64:
		default:
			return false
		}
	}
	return true
}

func (c *sessionCodec) store(b []byte, deadline time.Time, values map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[string(b)]; ok {
		return // Another decode inserted this payload.
	}
	if c.entries == nil {
		c.entries = make(map[string]decodedSession)
	}

	key := string(b)
	// The ring records the order of insertion and evicts the oldest entry.
	if len(c.order) == sessionCacheMaxEntries {
		delete(c.entries, c.order[c.oldest])
		c.order[c.oldest] = key
		c.oldest = (c.oldest + 1) % sessionCacheMaxEntries
	} else {
		c.order = append(c.order, key)
	}
	c.entries[key] = decodedSession{deadline: deadline, values: maps.Clone(values)}
}
