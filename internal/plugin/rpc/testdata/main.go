// This executable supplies protocol failures for the host tests.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/plugin-sdk/sdk"
)

func main() {
	behavior, _ := os.ReadFile("behavior")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request sdk.Request
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		var result any
		switch string(behavior) {
		case "timeout":
			time.Sleep(time.Hour)
		case "huge":
			for i := 0; i < 17; i++ {
				fmt.Print(strings.Repeat("x", 1<<20))
			}
			time.Sleep(time.Hour)
		case "bad-id":
			request.ID = json.RawMessage("99")
		case "bad-json":
			fmt.Println("{bad}")
			return
		}
		switch request.Method {
		case "handshake":
			api := 1
			if string(behavior) == "api" {
				api = 2
			}
			result = sdk.HandshakeResult{Name: "hello", Version: "1", API: api, Capabilities: []string{sdk.CapabilityGenerator, sdk.CapabilityCheck}}
		case "describe":
			result = sdk.DescribeResult{Generators: []sdk.Generator{{Name: "hello", Args: []string{"name"}}}, Checks: []sdk.Check{{Name: "hello", Title: "Health"}}}
		case "plan":
			path := "hello.txt"
			if string(behavior) == "escape" {
				path = "../escape"
			}
			content := []byte("hello\n")
			if string(behavior) == "environment" {
				cwd, _ := os.Getwd()
				content, _ = json.Marshal(struct {
					Env []string
					CWD string
				}{os.Environ(), cwd})
				fmt.Fprint(os.Stderr, "first\nsecond\npartial")
			}
			result = sdk.PlanResult{Files: []sdk.File{{Path: path, Data: content, Class: "managed", Mode: 0644}}}
		case "check":
			status := "warn"
			if string(behavior) == "check-fail" {
				status = "fail"
			}
			result = sdk.CheckResult{Findings: []sdk.Finding{{Status: status, Message: "Review hello", Try: "repair hello"}}}
		}
		raw, _ := json.Marshal(result)
		encoded, _ := json.Marshal(sdk.Response{JSONRPC: "2.0", ID: request.ID, Result: raw})
		fmt.Println(string(encoded))
		if string(behavior) == "handshake-exit" && request.Method == "handshake" {
			return
		}
		if string(behavior) == "handshake-exit-nonzero" && request.Method == "handshake" {
			os.Exit(7)
		}
		if string(behavior) == "exit" && request.Method != "handshake" {
			os.Exit(1)
		}
	}
}
