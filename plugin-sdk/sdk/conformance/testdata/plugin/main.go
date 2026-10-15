package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/plugin-sdk/sdk"
)

type plugin struct{ mode string }

// Handshake identifies the plugin and its capabilities.
func (p plugin) Handshake(context.Context) (sdk.HandshakeResult, error) {
	if p.mode == "slow" {
		time.Sleep(time.Minute)
	}
	result := sdk.HandshakeResult{Name: "example", Version: "0.1.0", API: sdk.API, Capabilities: []string{"generator", "check"}}
	switch p.mode {
	case "api":
		result.API = 99
	case "name":
		result.Name = "../bad"
	case "capability":
		result.Capabilities = []string{"shell"}
	case "missing-capability":
		result.Capabilities = []string{"check"}
	}
	return result, nil
}

// Describe lists the executable generators and checks.
func (p plugin) Describe(context.Context) (sdk.DescribeResult, error) {
	name := "hello"
	if p.mode == "describe" {
		name = "../bad"
	}
	return sdk.DescribeResult{Generators: []sdk.Generator{{Name: name, Short: "Hello", Usage: "hello <Name>", Args: []string{"Name"}}}, Checks: []sdk.Check{{Name: "ready", Title: "Ready"}}}, nil
}

// Plan returns proposed files without applying them.
func (p plugin) Plan(_ context.Context, args sdk.PlanParams) (sdk.PlanResult, error) {
	malicious := len(args.Args) > 0 && (strings.ContainsAny(args.Args[0], "/\\:") || len(args.Args[0]) > 100)
	if malicious && p.mode != "malicious" && p.mode != "huge" {
		return sdk.PlanResult{}, &sdk.Error{Code: sdk.InvalidParams, Message: "invalid argument"}
	}
	file := sdk.File{Path: "hello.txt", Data: []byte("Hello\n"), Class: "scaffold", Mode: 0644}
	switch p.mode {
	case "traversal":
		file.Path = "../escape"
	case "protected":
		file.Path = ".github/workflows/evil.yml"
	case "nondeterministic":
		file.Data = fmt.Appendf(nil, "%d", os.Getpid())
	case "write":
		_ = os.WriteFile("written", nil, 0644)
	case "parent-write":
		_ = os.WriteFile("../written", nil, 0644)
	case "modify":
		_ = os.WriteFile("behavior", []byte("changed"), 0644)
	case "chmod":
		_ = os.Chmod("behavior", 0755)
	case "class":
		file.Class = "invalid"
	case "mode":
		file.Mode = 04755
	case "malicious":
		if malicious {
			file.Path = "../escape"
		}
	case "huge":
		if len(args.Args[0]) > 100 {
			file.Path = "../escape"
		}
	case "oversize":
		file.Data = make([]byte, sdk.MaxMessageBytes)
	}
	result := sdk.PlanResult{Files: []sdk.File{file}}
	if p.mode == "duplicate" {
		result.Files = append(result.Files, file)
	}
	return result, nil
}

// Check reports findings for the selected check.
func (p plugin) Check(context.Context, sdk.CheckParams) (sdk.CheckResult, error) {
	status := "ok"
	if p.mode == "finding" {
		status = "invalid"
	}
	return sdk.CheckResult{Findings: []sdk.Finding{{Status: status, Message: "Ready"}}}, nil
}
func main() {
	mode, _ := os.ReadFile("behavior")
	p := plugin{mode: string(mode)}
	if p.mode == "unknown" || p.mode == "malformed" || p.mode == "envelope" || p.mode == "shape" {
		reader := bufio.NewReader(os.Stdin)
		for {
			line, err := sdk.ReadMessage(reader)
			if err != nil {
				return
			}
			var req sdk.Request
			_ = json.Unmarshal(line, &req)
			if p.mode == "envelope" {
				fmt.Println(`{"jsonrpc":"1.0","id":1,"result":{}}`)
				continue
			}
			if p.mode == "shape" && req.Method == "plan" {
				fmt.Println(`{"jsonrpc":"2.0","id":2,"result":{}}`)
				continue
			}
			if p.mode == "unknown" && req.Method == "spinup.conformance.unknown" || p.mode == "malformed" && !json.Valid(line) {
				fmt.Println(`{"jsonrpc":"2.0","id":2,"result":{}}`)
				continue
			}
			_ = sdk.ServeIO(context.Background(), strings.NewReader(string(line)+"\n"), os.Stdout, p)
		}
	}
	if err := sdk.Serve(p); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
