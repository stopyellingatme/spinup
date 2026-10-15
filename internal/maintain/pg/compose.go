package pg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/stackversions"
)

func (u *upgrade) updateCompose(ctx context.Context, image stackversions.Image, volume string) error {
	return u.updateComposeAt(ctx, image, volume, "")
}

func (u *upgrade) updateComposeAt(ctx context.Context, image stackversions.Image, volume, dataPath string) error {
	edge := u.dir + "/compose.edge.yml"
	data, err := u.run(ctx, "cat", edge)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err = yaml.Unmarshal(data, &doc); err != nil {
		return failure("parse Compose file", err)
	}
	services, _ := doc["services"].(map[string]any)
	pg, _ := services["postgres"].(map[string]any)
	if pg == nil {
		return failure("update Compose file", errors.New("postgres service is missing"))
	}
	tag := u.cfg.App.Name + "-postgres:" + image.Tag + "-pgbackrest" + stackversions.MustLoad().Apt["pgbackrest"]
	pg["image"] = tag
	if volume != "" {
		target := "/var/lib/postgresql/data"
		pgdata := target + "/pgdata"
		if u.o.To == 18 {
			target = "/var/lib/postgresql"
			pgdata = target + "/18/docker"
		}
		mounts, _ := pg["volumes"].([]any)
		found := false
		for i, m := range mounts {
			if text, ok := m.(string); ok && strings.HasPrefix(text, "postgres_data:") {
				mounts[i] = "postgres_data:" + target
				found = true
			}
			if item, ok := m.(map[string]any); ok && item["source"] == "postgres_data" {
				item["target"] = target
				found = true
			}
		}
		if !found {
			return failure("update Compose file", errors.New("postgres_data mount is missing"))
		}
		env, ok := pg["environment"].(map[string]any)
		if !ok {
			return failure("update Compose file", errors.New("postgres environment must use mapping syntax"))
		}
		if dataPath != "" {
			pgdata = dataPath
		}
		env["PGDATA"] = pgdata
		volumes, _ := doc["volumes"].(map[string]any)
		if volumes == nil {
			return failure("update Compose file", errors.New("volume definitions are missing"))
		}
		volumes["postgres_data"] = map[string]any{"external": true, "name": volume}
	}
	data, err = yaml.Marshal(doc)
	if err != nil {
		return failure("render Compose file", err)
	}
	dockerfile, err := u.run(ctx, "cat", u.dir+"/Dockerfile.postgres")
	if err != nil {
		return err
	}
	lines := strings.Split(string(dockerfile), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(line, "FROM ") {
			if found {
				return failure("update Dockerfile.postgres", errors.New("expected one FROM instruction"))
			}
			lines[i] = "FROM " + image.Ref()
			found = true
		}
	}
	if !found {
		return failure("update Dockerfile.postgres", errors.New("FROM instruction is missing"))
	}
	if err = u.vm.Upload(ctx, []byte(strings.Join(lines, "\n")), u.dir+"/Dockerfile.postgres", 0644); err != nil {
		return failure("write Dockerfile.postgres", err)
	}
	if err = u.vm.Upload(ctx, data, edge, 0644); err != nil {
		return failure("write Compose file", err)
	}
	return nil
}
func (u *upgrade) repositoryChange() {
	u.repositoryChangeAt(fmt.Sprintf("%s_pgdata_%d", u.cfg.App.Name, u.o.To))
}
func (u *upgrade) repositoryChangeAt(volume string) {
	fmt.Fprintf(u.o.Out, `Before the next deploy, commit these repository changes:
1. Set answers.db_version to %q (a JSON string) in spinup.lock.
2. Render the new answer with these commands (review local changes first):
   spinup restore deploy/Dockerfile.postgres --force
   spinup restore deploy/compose.edge.yml --force
   spinup restore deploy/compose.dev.yml --force
3. In deploy/compose.edge.yml, set volumes.postgres_data to:
   postgres_data:
     external: true
     name: %s
4. Commit spinup.lock and the rendered deploy files, including Dockerfile.postgres and compose.edge.yml.
spinup.toml has no db_version setting. No command changes this answer directly. These are manual steps.
The VM swap survives the next spinup deploy only after these repository changes.
`, fmt.Sprint(u.o.To), volume)
	if u.usesPGBackRest() {
		fmt.Fprintln(u.o.Out, "For pgBackRest, render and commit its configuration before the full backup:")
		fmt.Fprintln(u.o.Out, "   spinup restore deploy/pgbackrest/pgbackrest.conf --force")
	}
}

func (u *upgrade) checkRepositorySwap(volume string) error {
	lock, err := manifest.Load(filepath.Join(u.root, "spinup.lock"))
	if err != nil {
		return failure("check repository before cleanup", err)
	}
	major := strconv.Itoa(u.current.major)
	var missing []string
	if lock.Answers["db_version"] != major {
		missing = append(missing, fmt.Sprintf("spinup.lock answers.db_version must be %q", major))
	}
	if volume != u.cfg.App.Name+"_pgdata_"+major {
		missing = append(missing, "the active PostgreSQL major must match the completed swap volume")
	}
	data, err := os.ReadFile(filepath.Join(u.root, "deploy/compose.edge.yml"))
	if err != nil {
		return failure("check deploy/compose.edge.yml before cleanup", err)
	}
	var doc struct {
		Volumes map[string]struct {
			External bool   `yaml:"external"`
			Name     string `yaml:"name"`
		} `yaml:"volumes"`
	}
	if err = yaml.Unmarshal(data, &doc); err != nil {
		return failure("parse deploy/compose.edge.yml before cleanup", err)
	}
	pg := doc.Volumes["postgres_data"]
	if !pg.External || pg.Name != volume {
		missing = append(missing, fmt.Sprintf("deploy/compose.edge.yml volumes.postgres_data must have external: true and name: %s", volume))
	}
	if len(missing) > 0 {
		return fmt.Errorf("cleanup refused: %s\nTry: complete and commit the repository changes from pg-major-upgrade before --cleanup", strings.Join(missing, "\n"))
	}
	return nil
}
