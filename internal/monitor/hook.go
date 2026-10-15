package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/stopyellingatme/spinup/internal/deploy"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func init() { deploy.RegisterPostRelease(deploy.Hook{Name: "grafana-annotation", Run: annotate}) }

func annotate(ctx context.Context, h *deploy.HookContext) error {
	// A running container in this app's monitoring project identifies the installed pack.
	c := client{vm: h.Remote, app: h.App, out: io.Discard}
	id, err := c.container(ctx, "grafana")
	if err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	if !deploy.ValidReleaseID(h.Release) {
		return failure("annotate release", fmt.Errorf("invalid release ID"))
	}
	res, err := h.Remote.Run(ctx, remote.Cmd{Argv: []string{"cat", "/opt/" + h.App + "/releases/" + h.Release + "/release.json"}})
	if err != nil {
		return failure("read release metadata", err)
	}
	var release struct {
		Release string `json:"release"`
		SHA     string `json:"git_sha"`
		User    string `json:"user"`
	}
	if err = json.Unmarshal(res.Stdout, &release); err != nil {
		return failure("decode release metadata", err)
	}
	if release.Release != h.Release {
		return failure("annotate release", fmt.Errorf("release metadata does not match"))
	}
	_, err = c.api(ctx, "grafana", "POST", "/api/annotations", map[string]any{"tags": []string{"deploy", h.App, h.Release}, "text": fmt.Sprintf("Release: %s\nGit SHA: %s\nUser: %s", h.Release, release.SHA, release.User)})
	return err
}
