package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// KeepReleases is the number of releases that the VM keeps (ADR 0009 §2).
const KeepReleases = 5

var releaseIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{12}(-r[1-9][0-9]*)?$`)

// ReleaseID returns <UTC time YYYYMMDDTHHMMSSZ>-<12-char git SHA> (ADR 0009 §2).
func ReleaseID(at time.Time, sha string) (string, error) {
	if len(sha) < 12 || !regexp.MustCompile(`^[0-9a-f]+$`).MatchString(sha) {
		return "", fmt.Errorf("invalid git SHA %q\nTry: commit the work and run spinup deploy again", sha)
	}
	return at.UTC().Format("20060102T150405Z") + "-" + sha[:12], nil
}

// ValidReleaseID reports whether id is a release ID, including a redeploy suffix.
func ValidReleaseID(id string) bool { return releaseIDPattern.MatchString(id) }

// releaseTime returns the time encoded in a valid release ID.
func releaseTime(id string) time.Time {
	t, _ := time.Parse("20060102T150405Z", id[:16])
	return t
}

// Release is the release.json snapshot of one release. It never holds a secret value.
type Release struct {
	Release          string            `json:"release"`
	App              string            `json:"app"`
	Env              string            `json:"env"`
	GitSHA           string            `json:"git_sha"`
	Color            string            `json:"color"`
	Transport        string            `json:"transport"`
	Images           []Image           `json:"images"`
	MigrationVersion string            `json:"migration_version"`
	User             string            `json:"user"`
	CreatedAt        time.Time         `json:"created_at"`
	Secrets          map[string]string `json:"secrets"`
}

// ReleaseEntry is one entry of releases.json, the release history (newest first).
type ReleaseEntry struct {
	Release          string    `json:"release"`
	GitSHA           string    `json:"git_sha"`
	Color            string    `json:"color"`
	Images           []Image   `json:"images"`
	MigrationVersion string    `json:"migration_version"`
	User             string    `json:"user"`
	CreatedAt        time.Time `json:"created_at"`
}

// SecretHashes returns the SHA-256 of each secret value, as release.json stores them.
func SecretHashes(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for name, value := range values {
		sum := sha256.Sum256([]byte(value))
		out[name] = hex.EncodeToString(sum[:])
	}
	return out
}

// EnvFile is deploy/environments/<env>.toml.
type EnvFile struct {
	SmokeURLs []string                  `toml:"smoke_urls"`
	Replicas  map[string]int            `toml:"replicas"`
	Resources map[string]ResourceLimits `toml:"resources"`
}

// ResourceLimits are the Compose limits of one service.
type ResourceLimits struct {
	CPUs   string `toml:"cpus"`
	Memory string `toml:"memory"`
}

// LoadEnvFile reads deploy/environments/<env>.toml from the project root.
func LoadEnvFile(root, env string) (EnvFile, error) {
	name := filepath.Join(root, "deploy", "environments", env+".toml")
	data, err := os.ReadFile(name)
	if err != nil {
		return EnvFile{}, fmt.Errorf("read deploy/environments/%s.toml: %w\nTry: create the file from deploy/environments/staging.toml", env, err)
	}
	var f EnvFile
	if err := toml.Unmarshal(data, &f); err != nil {
		return EnvFile{}, fmt.Errorf("parse deploy/environments/%s.toml: %w\nTry: correct the TOML syntax", env, err)
	}
	for _, u := range f.SmokeURLs {
		if !strings.HasPrefix(u, "/") || strings.ContainsAny(u, " \t\r\n") {
			return EnvFile{}, fmt.Errorf("deploy/environments/%s.toml: smoke URL %q is not a path\nTry: use paths that start with /", env, u)
		}
	}
	return f, nil
}

var envValuePattern = regexp.MustCompile(`^[A-Za-z0-9._:/@+=,%-]*$`)

// dotEnv renders the release .env file that Compose reads. It holds no secret.
// Keys are in a fixed order, so the file is deterministic.
func dotEnv(pairs [][2]string) ([]byte, error) {
	var b strings.Builder
	b.WriteString("# Written by spinup deploy. It holds no secret.\n")
	for _, kv := range pairs {
		if !envValuePattern.MatchString(kv[1]) {
			return nil, fmt.Errorf("the deploy variable %s has the unsafe value %q\nTry: correct spinup.toml or deploy/environments/<env>.toml", kv[0], kv[1])
		}
		b.WriteString(kv[0] + "=" + kv[1] + "\n")
	}
	return []byte(b.String()), nil
}

// publicOrigin returns PUBLIC_ORIGIN and PUBLIC_HOST for an environment (docs/deploy.md).
func publicOrigin(publicURL, host string) (origin, site string, err error) {
	if publicURL == "" {
		publicURL = "http://" + host
	}
	u, err := url.Parse(publicURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", "", fmt.Errorf("public_url %q is not an http or https URL\nTry: set public_url in spinup.toml, for example https://app.example.com", publicURL)
	}
	origin = u.Scheme + "://" + u.Host
	site = u.Hostname()
	if u.Scheme == "http" {
		site = "http://" + u.Hostname()
	}
	return origin, site, nil
}

// releaseFile is one file of the release directory.
type releaseFile struct {
	Path string // relative to the release directory, with slashes
	Data []byte
	Mode fs.FileMode
}

// excludedDeployFiles are files of deploy/ that the VM never needs.
var excludedDeployFiles = []string{"compose.dev.yml", "Dockerfile.api", "Dockerfile.web"}

// deployFiles reads deploy/ for the release directory, without environments/, cloud-init/,
// local build files, and .gitkeep files. The order is sorted.
func deployFiles(root string) ([]releaseFile, error) {
	base := filepath.Join(root, "deploy")
	var files []releaseFile
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "environments" || rel == "cloud-init" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == ".gitkeep" || slices.Contains(excludedDeployFiles, rel) || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("deploy/%s is not a regular file", rel)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files = append(files, releaseFile{Path: rel, Data: data, Mode: 0644})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the deploy files: %w\nTry: check the deploy/ directory, or run spinup restore deploy/compose.yml", err)
	}
	for _, need := range []string{"compose.yml", "compose.edge.yml", "Caddyfile"} {
		if !slices.ContainsFunc(files, func(f releaseFile) bool { return f.Path == need }) {
			return nil, fmt.Errorf("deploy/%s is missing\nTry: run spinup restore deploy/%s", need, need)
		}
	}
	return files, nil
}

// edgeComposeFiles returns the Compose files of the base project: compose.edge.yml and
// other compose.*.yml files that a pack adds (for example compose.backups.yml).
func edgeComposeFiles(files []releaseFile) []string {
	out := []string{"compose.edge.yml"}
	for _, f := range files {
		if strings.Contains(f.Path, "/") || !strings.HasPrefix(f.Path, "compose.") || !strings.HasSuffix(f.Path, ".yml") {
			continue
		}
		switch f.Path {
		case "compose.yml", "compose.edge.yml", "compose.override.yml", "compose.dev.yml":
			continue
		}
		out = append(out, f.Path)
	}
	return out
}

// releaseDirs returns the sorted directories that the release files need.
func releaseDirs(dir string, files []releaseFile) []string {
	set := map[string]bool{dir: true}
	for _, f := range files {
		for d := path.Dir(f.Path); d != "."; d = path.Dir(d) {
			set[dir+"/"+d] = true
		}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	slices.Sort(out)
	return out
}

// migration is a goose migration file.
type migration struct {
	Version int64
	Name    string
}

var migrationName = regexp.MustCompile(`^([0-9]+)_.*\.sql$`)

// migrations lists api/db/migrations/*.sql in version order. A missing directory gives none.
func migrations(root string) ([]migration, error) {
	entries, err := os.ReadDir(filepath.Join(root, "api", "db", "migrations"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read api/db/migrations: %w\nTry: check the migrations directory", err)
	}
	var out []migration
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if e.IsDir() || m == nil {
			continue
		}
		v, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			continue
		}
		out = append(out, migration{Version: v, Name: e.Name()})
	}
	slices.SortFunc(out, func(a, b migration) int {
		if a.Version != b.Version {
			if a.Version < b.Version {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out, nil
}

// migrationVersion returns the highest version, or "" when there is no migration.
func migrationVersion(ms []migration) string {
	if len(ms) == 0 {
		return ""
	}
	return strconv.FormatInt(ms[len(ms)-1].Version, 10)
}

// pendingMigrations returns the migrations newer than the deployed version.
func pendingMigrations(ms []migration, deployed string) []string {
	var since int64 = -1
	if deployed != "" {
		v, err := strconv.ParseInt(deployed, 10, 64)
		if err == nil {
			since = v
		}
	}
	var out []string
	for _, m := range ms {
		if m.Version > since {
			out = append(out, m.Name)
		}
	}
	return out
}

// readRemoteFileScript prints a file when it exists. A missing file prints nothing.
const readRemoteFileScript = `if [ -f "$1" ]; then cat -- "$1"; fi`

// readRelease reads releases/<id>/release.json, or current/release.json when id is "".
// It returns nil when the file does not exist.
func readRelease(ctx context.Context, r remote.Runner, app, id string) (*Release, error) {
	root, err := appRoot(app)
	if err != nil {
		return nil, err
	}
	p := root + "/current/release.json"
	if id != "" {
		p = root + "/releases/" + id + "/release.json"
	}
	result, err := r.Run(ctx, remote.Cmd{Argv: []string{"sh", "-c", readRemoteFileScript, "spinup-read", p}})
	if err != nil {
		return nil, fmt.Errorf("read %s: %w\nTry: check the SSH connection", p, err)
	}
	if len(result.Stdout) == 0 {
		return nil, nil
	}
	var rel Release
	if err := json.Unmarshal(result.Stdout, &rel); err != nil {
		return nil, fmt.Errorf("decode %s: %w\nTry: inspect the file on the VM", p, err)
	}
	return &rel, nil
}

// ReadReleases reads releases.json, the release history (newest first). A missing file gives none.
func ReadReleases(ctx context.Context, r remote.Runner, app string) ([]ReleaseEntry, error) {
	root, err := appRoot(app)
	if err != nil {
		return nil, err
	}
	result, err := r.Run(ctx, remote.Cmd{Argv: []string{"sh", "-c", readRemoteFileScript, "spinup-read", root + "/releases.json"}})
	if err != nil {
		return nil, fmt.Errorf("read releases.json: %w\nTry: check the SSH connection", err)
	}
	if len(strings.TrimSpace(string(result.Stdout))) == 0 {
		return nil, nil
	}
	var entries []ReleaseEntry
	if err := json.Unmarshal(result.Stdout, &entries); err != nil {
		return nil, fmt.Errorf("decode releases.json: %w\nTry: inspect /opt/%s/releases.json on the VM", err, app)
	}
	return entries, nil
}

// prependRelease puts e first, removes an older entry with the same ID, and keeps KeepReleases entries.
// It returns the kept entries and the removed ones.
func prependRelease(entries []ReleaseEntry, e ReleaseEntry) (kept, pruned []ReleaseEntry) {
	kept = []ReleaseEntry{e}
	for _, old := range entries {
		if old.Release == e.Release {
			continue
		}
		if len(kept) < KeepReleases {
			kept = append(kept, old)
		} else {
			pruned = append(pruned, old)
		}
	}
	return kept, pruned
}
