package sdk

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// ReadMessage reads one newline-terminated JSON object with a bounded allocation.
// An unterminated final object is a framing error.
func ReadMessage(r *bufio.Reader) ([]byte, error) {
	var data []byte
	for {
		chunk, err := r.ReadSlice('\n')
		size := len(chunk)
		if size > 0 && chunk[size-1] == '\n' {
			size--
		}
		if len(data)+size > MaxMessageBytes {
			return nil, fmt.Errorf("message exceeds %d bytes", MaxMessageBytes)
		}
		data = append(data, chunk[:size]...)
		if err == nil {
			return data, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) && len(data) > 0 {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
}

// Serve handles calls on stdin and stdout until EOF. Diagnostics belong on stderr.
func Serve(h Handler) error { return ServeIO(context.Background(), os.Stdin, os.Stdout, h) }

// ServeIO handles calls sequentially on explicit streams for tests and embedding.
func ServeIO(ctx context.Context, in io.Reader, out io.Writer, h Handler) error {
	r := bufio.NewReader(in)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := ReadMessage(r)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read plugin request: %w", err)
		}
		response := dispatch(ctx, data, h)
		encoded, err := json.Marshal(response)
		if err != nil {
			return fmt.Errorf("encode plugin response: %w", err)
		}
		if len(encoded) > MaxMessageBytes {
			return fmt.Errorf("plugin response exceeds %d bytes", MaxMessageBytes)
		}
		if _, err = out.Write(append(encoded, '\n')); err != nil {
			return fmt.Errorf("write plugin response: %w", err)
		}
	}
}

func validID(id json.RawMessage) bool {
	var value any
	if json.Unmarshal(id, &value) != nil {
		return false
	}
	switch value.(type) {
	case string, float64:
		return true
	}
	return false
}

func dispatch(ctx context.Context, data []byte, h Handler) Response {
	response := Response{JSONRPC: "2.0", ID: json.RawMessage("null")}
	var request Request
	if err := json.Unmarshal(data, &request); err != nil {
		response.Error = &Error{Code: ParseError, Message: "invalid JSON"}
		return response
	}
	if validID(request.ID) {
		response.ID = request.ID
	}
	if request.JSONRPC != "2.0" || !validID(request.ID) || request.Method == "" {
		response.Error = &Error{Code: InvalidRequest, Message: "expected a JSON-RPC 2.0 request with an ID"}
		return response
	}
	result, err := call(ctx, request, h)
	if err != nil {
		var rpcErr *Error
		if !errors.As(err, &rpcErr) {
			rpcErr = &Error{Code: InternalError, Message: err.Error()}
		}
		response.Error = rpcErr
		return response
	}
	response.Result, err = json.Marshal(result)
	if err != nil {
		response.Error = &Error{Code: InternalError, Message: err.Error()}
		response.Result = nil
	}
	return response
}

func call(ctx context.Context, r Request, h Handler) (any, error) {
	switch r.Method {
	case "handshake":
		return h.Handshake(ctx)
	case "describe":
		return h.Describe(ctx)
	case "plan":
		var p PlanParams
		if json.Unmarshal(r.Params, &p) != nil || p.Generator == "" {
			return nil, &Error{Code: InvalidParams, Message: "plan requires a generator and arguments"}
		}
		return h.Plan(ctx, p)
	case "check":
		var p CheckParams
		if json.Unmarshal(r.Params, &p) != nil || p.Check == "" {
			return nil, &Error{Code: InvalidParams, Message: "check requires a check name"}
		}
		return h.Check(ctx, p)
	default:
		return nil, &Error{Code: MethodNotFound, Message: "unknown method: " + r.Method}
	}
}
