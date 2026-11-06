package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen"
)

type runnerFunc func(context.Context, exec.Cmd) (exec.Result, error)

func (fn runnerFunc) Run(ctx context.Context, cmd exec.Cmd) (exec.Result, error) {
	return fn(ctx, cmd)
}

func TestParallelPostStepsOutputAndErrors(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sqlcFinished := make(chan struct{})
			openAPIFinished := make(chan struct{})
			runners := []postStepRunner{
				func(ctx context.Context, _ string, _ []string, r exec.Runner) error {
					defer close(openAPIFinished)
					select {
					case <-sqlcFinished:
					case <-ctx.Done():
						return errors.New("sqlc did not run in parallel")
					}
					_, _ = fmt.Fprintln(PostStepOutput(ctx), "OpenAPI notice")
					return tool(ctx, "root", r, "openapi-test")
				},
				func(ctx context.Context, _ string, _ []string, r exec.Runner) error {
					defer close(sqlcFinished)
					return tool(ctx, "root", r, "sqlc-test")
				},
				func(ctx context.Context, _ string, _ []string, r exec.Runner) error {
					select {
					case <-openAPIFinished:
					default:
						return errors.New("client ran before OpenAPI finished")
					}
					return tool(ctx, "root", r, "client-test")
				},
			}
			// sqlc finishes first. The errors and output must still list OpenAPI first.
			fake := &exec.Fake{}
			code := 0
			if fail {
				code = 1
			}
			fake.Expect("sqlc-test").Return("SQLC output\n", "", code)
			fake.Expect("openapi-test").Return("OpenAPI output\n", "", code)
			if !fail {
				fake.Expect("client-test").Return("client output\n", "", 0)
			}
			var out bytes.Buffer
			err := runPostSteps(ctx, "root", nil, []gen.PostStep{gen.PostOpenAPI, gen.PostSQLC, gen.PostClient}, runners, fake, &out)
			fake.AssertDone(t)
			if fail {
				if err == nil || !strings.Contains(err.Error(), `post-step "openapi" failed`) || !strings.Contains(err.Error(), `post-step "sqlc" failed`) {
					t.Fatalf("errors = %v", err)
				}
				if strings.Index(err.Error(), `post-step "openapi"`) > strings.Index(err.Error(), `post-step "sqlc"`) {
					t.Fatalf("errors out of order: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			want := "OpenAPI notice\nOpenAPI output\nSQLC output\n"
			if !fail {
				want += "client output\n"
			}
			if out.String() != want {
				t.Fatalf("output = %q, want %q", out.String(), want)
			}
		})
	}
}
