package mcp

import (
	"bytes"
	"encoding/json"
	"sync"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type requestIDsKey struct{}

type callIdentity struct{ name, arguments string }

type requestIDs struct {
	single string
	mu     sync.Mutex
	batch  map[callIdentity][]string
}

// parseRequestIDs is a test seam for counting body parses.
var parseRequestIDs = readRequestIDs

// readRequestIDs parses the envelopes once. The SDK does not expose request IDs.
// Arguments identify batch calls but never become telemetry attributes.
func readRequestIDs(body []byte) *requestIDs {
	type envelope struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	ids := &requestIDs{}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return ids
	}
	if body[0] != '[' {
		var message envelope
		if json.Unmarshal(body, &message) == nil {
			ids.single = requestID(message.ID)
		}
		return ids
	}
	var messages []envelope
	if json.Unmarshal(body, &messages) != nil {
		return ids
	}
	ids.batch = make(map[callIdentity][]string)
	for _, message := range messages {
		if message.Method == "tools/call" {
			key := callIdentity{message.Params.Name, string(message.Params.Arguments)}
			ids.batch[key] = append(ids.batch[key], requestID(message.ID))
		}
	}
	return ids
}

func requestID(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		var id string
		_ = json.Unmarshal(raw, &id)
		return id
	}
	return string(raw)
}

func (ids *requestIDs) take(req sdkmcp.Request) string {
	if ids.batch == nil {
		return ids.single
	}
	params, ok := req.GetParams().(*sdkmcp.CallToolParamsRaw)
	if !ok || params == nil {
		return ""
	}
	key := callIdentity{params.Name, string(params.Arguments)}
	ids.mu.Lock()
	defer ids.mu.Unlock()
	queue := ids.batch[key]
	if len(queue) == 0 {
		return ""
	}
	// The SDK hides wire IDs. Identical calls consume IDs in middleware order.
	ids.batch[key] = queue[1:]
	return queue[0]
}
