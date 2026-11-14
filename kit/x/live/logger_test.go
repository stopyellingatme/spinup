package live

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// The default logger of a hub follows a later slog.SetDefault.
func TestDefaultLoggerFollowsSetDefault(t *testing.T) {
	var o options
	if err := o.resolve(); err != nil {
		t.Fatal(err)
	}
	old := slog.Default()
	defer slog.SetDefault(old)
	var out bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
	o.logger.InfoContext(context.Background(), "live: test line")
	if !strings.Contains(out.String(), `"msg":"live: test line"`) {
		t.Fatalf("the record did not reach the new default logger: %q", out.String())
	}
}
