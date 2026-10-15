package sdk

import (
	"context"
	"encoding/json"
	"fmt"
)

// API is the supported protocol version.
const API = 1

// MaxMessageBytes is the maximum JSON object size, excluding its newline.
const MaxMessageBytes = 16 << 20

const (
	// CapabilityGenerator permits generator descriptions and plans.
	CapabilityGenerator = "generator"
	// CapabilityCheck permits doctor check descriptions and findings.
	CapabilityCheck = "check"
)

const (
	// ParseError identifies invalid JSON.
	ParseError = -32700
	// InvalidRequest identifies an invalid JSON-RPC request.
	InvalidRequest = -32600
	// MethodNotFound identifies an unsupported method.
	MethodNotFound = -32601
	// InvalidParams identifies invalid method parameters.
	InvalidParams = -32602
	// InternalError identifies a handler failure.
	InternalError = -32603
)

// Request is a single JSON-RPC call. IDs are numbers or strings.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response contains exactly one result or error and echoes the request ID.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error describes a protocol or handler failure.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error returns a diagnostic for the caller.
func (e *Error) Error() string { return fmt.Sprintf("JSON-RPC %d: %s", e.Code, e.Message) }

// HandshakeResult identifies the executable and its supported capabilities.
type HandshakeResult struct {
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	API          int      `json:"api"`
	Capabilities []string `json:"capabilities"`
}

// Generator describes a generator and its positional argument names.
type Generator struct {
	Name  string   `json:"name"`
	Short string   `json:"short"`
	Usage string   `json:"usage"`
	Args  []string `json:"args,omitempty"`
}

// Check describes a doctor check.
type Check struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

// DescribeResult lists the generators and checks supplied by the executable.
type DescribeResult struct {
	Generators []Generator `json:"generators"`
	Checks     []Check     `json:"checks"`
}

// PlanParams supplies a generator name, positional arguments, and project answers.
type PlanParams struct {
	Generator string         `json:"generator"`
	Args      []string       `json:"args"`
	Answers   map[string]any `json:"answers,omitempty"`
}

// File is one planned file. Data encodes as base64 in JSON. Mode uses permission bits.
type File struct {
	Path  string `json:"path"`
	Data  []byte `json:"data_base64"`
	Class string `json:"class"`
	Mode  uint32 `json:"mode"`
}

// PlanResult returns files for the host to validate and apply, plus suggested commands.
type PlanResult struct {
	Files []File   `json:"files"`
	Next  []string `json:"next,omitempty"`
}

// CheckParams selects a described check.
type CheckParams struct {
	Check string `json:"check"`
}

// Finding is a doctor result. Status is ok, warn, fail, or skip.
type Finding struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Try     string `json:"try,omitempty"`
}

// CheckResult returns findings for a check.
type CheckResult struct {
	Findings []Finding `json:"findings"`
}

// Handler implements API 1. Handlers return plans and must not write project files.
type Handler interface {
	Handshake(context.Context) (HandshakeResult, error)
	Describe(context.Context) (DescribeResult, error)
	Plan(context.Context, PlanParams) (PlanResult, error)
	Check(context.Context, CheckParams) (CheckResult, error)
}
