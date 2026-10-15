package main

import (
	"context"
	"fmt"
	"os"

	"github.com/stopyellingatme/spinup/plugin-sdk/sdk"
)

type hello struct{}

func (hello) Handshake(context.Context) (sdk.HandshakeResult, error) {
	return sdk.HandshakeResult{Name: "exec-hello", Version: "1.0.0", API: sdk.API, Capabilities: []string{sdk.CapabilityGenerator, sdk.CapabilityCheck}}, nil
}
func (hello) Describe(context.Context) (sdk.DescribeResult, error) {
	return sdk.DescribeResult{Generators: []sdk.Generator{{Name: "hello", Short: "Write a greeting", Usage: "hello <name>", Args: []string{"name"}}}, Checks: []sdk.Check{{Name: "hello", Title: "Hello plugin health"}}}, nil
}
func (hello) Plan(_ context.Context, p sdk.PlanParams) (sdk.PlanResult, error) {
	if p.Generator != "hello" || len(p.Args) != 1 {
		return sdk.PlanResult{}, fmt.Errorf("hello requires one name")
	}
	return sdk.PlanResult{Files: []sdk.File{{Path: "hello.txt", Data: []byte("Hello, " + p.Args[0] + "!\n"), Class: "scaffold", Mode: 0644}}}, nil
}
func (hello) Check(context.Context, sdk.CheckParams) (sdk.CheckResult, error) {
	return sdk.CheckResult{Findings: []sdk.Finding{{Status: "ok", Message: "Hello plugin is available"}}}, nil
}
func main() {
	if err := sdk.Serve(hello{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
