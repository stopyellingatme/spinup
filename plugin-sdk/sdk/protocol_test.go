package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type testHandler struct{}

func (testHandler) Handshake(context.Context) (HandshakeResult, error) {
	return HandshakeResult{Name: "hello", Version: "1.0.0", API: API, Capabilities: []string{CapabilityGenerator, CapabilityCheck}}, nil
}
func (testHandler) Describe(context.Context) (DescribeResult, error) {
	return DescribeResult{Generators: []Generator{{Name: "hello"}}}, nil
}
func (testHandler) Plan(_ context.Context, p PlanParams) (PlanResult, error) {
	return PlanResult{Files: []File{{Path: "hello.txt", Data: []byte(strings.Join(p.Args, " ")), Class: "scaffold", Mode: 0644}}}, nil
}
func (testHandler) Check(context.Context, CheckParams) (CheckResult, error) {
	return CheckResult{}, nil
}
func TestServeIO(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, input string
		code        int
	}{
		{"handshake", `{"jsonrpc":"2.0","id":1,"method":"handshake"}`, 0},
		{"describe", `{"jsonrpc":"2.0","id":2,"method":"describe"}`, 0},
		{"plan", `{"jsonrpc":"2.0","id":3,"method":"plan","params":{"generator":"hello","args":["world"]}}`, 0},
		{"check", `{"jsonrpc":"2.0","id":4,"method":"check","params":{"check":"hello"}}`, 0},
		{"unknown", `{"jsonrpc":"2.0","id":5,"method":"missing"}`, MethodNotFound},
		{"bad JSON", `{`, ParseError},
		{"version", `{"jsonrpc":"1.0","id":6,"method":"handshake"}`, InvalidRequest},
		{"params", `{"jsonrpc":"2.0","id":7,"method":"plan","params":true}`, InvalidParams},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			if err := ServeIO(context.Background(), strings.NewReader(tc.input+"\n"), &out, testHandler{}); err != nil {
				t.Fatal(err)
			}
			var response Response
			if err := json.Unmarshal(out.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if tc.code == 0 {
				if response.Error != nil {
					t.Fatal(response.Error)
				}
			} else if response.Error == nil || response.Error.Code != tc.code {
				t.Fatalf("response=%s", out.Bytes())
			}
		})
	}
}
func TestReadMessageLimit(t *testing.T) {
	t.Parallel()
	if err := ServeIO(context.Background(), strings.NewReader(strings.Repeat("x", MaxMessageBytes+1)+"\n"), &bytes.Buffer{}, testHandler{}); err == nil {
		t.Fatal("accepted oversized request")
	}
}
