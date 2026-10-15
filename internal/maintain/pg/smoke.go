package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/deploy"
)

func checkReleaseVersion(body []byte, sha string) error {
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return fmt.Errorf("decode /version: %w", err)
	}
	if !strings.Contains(v.Version, sha) {
		return fmt.Errorf("/version reports %q, expected commit %s", v.Version, sha)
	}
	return nil
}

func (u *upgrade) smoke(ctx context.Context) error {
	get := u.d.HTTPGet
	if get == nil {
		get = deploy.DefaultDeps().HTTPGet
	}
	sleep := u.d.Sleep
	if sleep == nil {
		sleep = deploy.DefaultDeps().Sleep
	}
	envFile, err := deploy.LoadEnvFile(u.root, u.env.Name)
	if err != nil {
		return err
	}
	origin, err := u.publicOrigin()
	if err != nil {
		return err
	}
	retry := func(check func() error) error {
		var last error
		for i := 0; i < 15; i++ {
			if last = check(); last == nil {
				return nil
			}
			if i < 14 {
				if err := sleep(ctx, 2*time.Second); err != nil {
					return err
				}
			}
		}
		return failure("smoke check", last)
	}
	for _, path := range append([]string{"/healthz"}, envFile.SmokeURLs...) {
		if err = retry(func() error {
			status, _, e := get(ctx, origin+path)
			if e != nil {
				return e
			}
			if status < 200 || status > 299 {
				return fmt.Errorf("GET %s: HTTP %d", path, status)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return retry(func() error {
		var body []byte
		if len(u.services()) == 2 {
			ids, e := u.run(ctx, "docker", "ps", "-q", "--filter", "label=com.docker.compose.project="+u.cfg.App.Name, "--filter", "label=com.docker.compose.service=caddy")
			if e != nil {
				return e
			}
			fields := strings.Fields(string(ids))
			if len(fields) != 1 {
				return errors.New("the Caddy service is not running")
			}
			body, e = u.run(ctx, "docker", "exec", fields[0], "wget", "-q", "-T", "5", "-O", "-", "http://api-"+u.rel.Color+":8080/version")
			if e != nil {
				return e
			}
		} else {
			status, data, e := get(ctx, origin+"/version")
			if e != nil {
				return e
			}
			if status != 200 {
				return fmt.Errorf("GET /version: HTTP %d", status)
			}
			body = data
		}
		return checkReleaseVersion(body, u.rel.GitSHA[:12])
	})
}

func (u *upgrade) publicOrigin() (string, error) {
	origin := u.env.PublicURL
	if origin == "" {
		origin = "http://" + u.env.Host
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", failure("read public URL", errors.New("public_url must be an HTTP or HTTPS URL"))
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}
