package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/compat"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

const repository = "https://github.com/stopyellingatme/spinup"

// Updater supplies the release sources and local update dependencies.
// Empty fields use the public release service and the running executable.
type Updater struct {
	APIBase      string
	DownloadBase string
	Client       *http.Client
	Runner       runexec.Runner
	LookPath     func(string) (string, error)
	Executable   string
	OS           string
	Arch         string
}

// Options selects the release and verification policy.
type Options struct {
	Current          string
	Channel          string
	Version          string
	Check            bool
	RequireSignature bool
}

// Result describes a release check or completed update.
type Result struct {
	Current   string `json:"current"`
	Version   string `json:"version"`
	Available bool   `json:"available"`
	Updated   bool   `json:"updated"`
	Command   string `json:"command,omitempty"`
	Warning   string `json:"warning,omitempty"`
}

type assetInfo struct {
	Name string `json:"name"`
}
type release struct {
	Tag        string      `json:"tag_name"`
	Draft      bool        `json:"draft"`
	Prerelease bool        `json:"prerelease"`
	Published  time.Time   `json:"published_at"`
	Assets     []assetInfo `json:"assets"`
}

func failure(why any) error {
	return fmt.Errorf("cannot update spinup: %v\nTry: check the release version and connection, then retry", why)
}

func (u Updater) defaults() Updater {
	if u.APIBase == "" {
		u.APIBase = "https://api.github.com/repos/stopyellingatme/spinup"
	}
	if u.DownloadBase == "" {
		u.DownloadBase = repository + "/releases/download"
	}
	if u.Client == nil {
		u.Client = &http.Client{Timeout: 30 * time.Second}
	}
	if u.Runner == nil {
		u.Runner = runexec.OSRunner{}
	}
	if u.LookPath == nil {
		u.LookPath = runexec.LookPath
	}
	if u.OS == "" {
		u.OS = runtime.GOOS
	}
	if u.Arch == "" {
		u.Arch = runtime.GOARCH
	}
	return u
}

func (u Updater) fetch(ctx context.Context, address string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, failure(err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := u.Client.Do(req)
	if err != nil {
		return nil, failure(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, failure(fmt.Sprintf("download %s returned HTTP %d", address, resp.StatusCode))
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, failure(err)
	}
	if int64(len(b)) > limit {
		return nil, failure("release response exceeds the size limit")
	}
	return b, nil
}

func (u Updater) resolve(ctx context.Context, o Options) (release, error) {
	if o.Channel != "" && o.Channel != "stable" && o.Channel != "next" {
		return release{}, failure("channel must be stable or next")
	}
	endpoint := strings.TrimRight(u.APIBase, "/") + "/releases"
	if o.Version != "" {
		v := strings.TrimPrefix(o.Version, "v")
		if _, err := compat.ParseVersion(v); err != nil {
			return release{}, failure(err)
		}
		endpoint += "/tags/v" + v
	} else {
		endpoint += "?per_page=100"
	}
	data, err := u.fetch(ctx, endpoint, 16<<20)
	if err != nil {
		return release{}, err
	}
	var releases []release
	if o.Version != "" {
		var r release
		if err = json.Unmarshal(data, &r); err != nil {
			return release{}, failure(err)
		}
		releases = []release{r}
	} else if err = json.Unmarshal(data, &releases); err != nil {
		return release{}, failure(err)
	}
	var chosen release
	for _, r := range releases {
		v, err := compat.ParseVersion(r.Tag)
		if err != nil || r.Draft {
			continue
		}
		if o.Version == "" && o.Channel != "next" && (r.Prerelease || v.Pre != "") {
			continue
		}
		if o.Version != "" && strings.TrimPrefix(r.Tag, "v") != strings.TrimPrefix(o.Version, "v") {
			continue
		}
		if chosen.Tag == "" || r.Published.After(chosen.Published) {
			chosen = r
		}
	}
	if chosen.Tag == "" {
		return release{}, failure("no matching release exists")
	}
	return chosen, nil
}

func resultFor(current string, r release) Result {
	result := Result{Current: current, Version: strings.TrimPrefix(r.Tag, "v")}
	old, err := compat.ParseVersion(current)
	latest, _ := compat.ParseVersion(r.Tag)
	result.Available = err != nil || latest.Compare(old) > 0
	return result
}

// Check queries release metadata without downloading assets or changing the binary.
func (u Updater) Check(ctx context.Context, o Options) (Result, error) {
	u = u.defaults()
	r, err := u.resolve(ctx, o)
	if err != nil {
		return Result{}, err
	}
	return resultFor(o.Current, r), nil
}

// Homebrew reports whether a resolved executable belongs to a Homebrew installation.
func Homebrew(path string) bool {
	path = filepath.ToSlash(filepath.Clean(path))
	for _, prefix := range []string{"/opt/homebrew/", "/home/linuxbrew/.linuxbrew/", strings.TrimRight(os.Getenv("HOMEBREW_PREFIX"), "/") + "/"} {
		if prefix != "/" && strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return strings.Contains(path, "/Caskroom/") || strings.HasPrefix(path, "/usr/local/Cellar/") || strings.HasPrefix(path, "/usr/local/Homebrew/")
}

// Update verifies a release and atomically replaces the executable, unless Check is set.
func (u Updater) Update(ctx context.Context, o Options) (Result, error) {
	u = u.defaults()
	path := u.Executable
	var err error
	if path == "" {
		path, err = os.Executable()
		if err != nil {
			return Result{}, failure(err)
		}
	}
	// Resolve links so an installation link continues to point to the new binary.
	if resolved, e := filepath.EvalSymlinks(path); e == nil {
		path = resolved
	}
	if Homebrew(path) {
		return Result{Current: o.Current, Command: "brew upgrade --cask spinup"}, nil
	}
	if o.Version == "" && (o.Current == "dev" || strings.Contains(o.Current, "-alpha.")) {
		return Result{}, fmt.Errorf("cannot update spinup: a development build needs an explicit --version\nTry: spinup self-update --version <version>")
	}
	r, err := u.resolve(ctx, o)
	if err != nil {
		return Result{}, err
	}
	result := resultFor(o.Current, r)
	if o.Check || (!result.Available && o.Version == "") {
		return result, nil
	}
	ext := "tar.gz"
	if u.OS == "windows" {
		ext = "zip"
	}
	asset := fmt.Sprintf("spinup_%s_%s_%s.%s", result.Version, u.OS, u.Arch, ext)
	if !hasAsset(r, asset) || !hasAsset(r, "checksums.txt") {
		return Result{}, failure("the release is missing the archive or checksums asset")
	}
	cosign, lookupErr := u.LookPath("cosign")
	if lookupErr != nil && o.RequireSignature {
		return Result{}, fmt.Errorf("cannot update spinup: cosign is required for signature verification\nTry: install cosign and retry spinup self-update --require-signature")
	}
	base := strings.TrimRight(u.DownloadBase, "/") + "/v" + result.Version + "/"
	archive, err := u.fetch(ctx, base+asset, 256<<20)
	if err != nil {
		return Result{}, err
	}
	checksums, err := u.fetch(ctx, base+"checksums.txt", 4<<20)
	if err != nil {
		return Result{}, err
	}
	if err = verifyChecksum(archive, checksums, asset); err != nil {
		return Result{}, err
	}
	if lookupErr == nil {
		if !hasAsset(r, "checksums.txt.sigstore.json") {
			return Result{}, failure("the signature bundle asset is missing")
		}
		bundle, e := u.fetch(ctx, base+"checksums.txt.sigstore.json", 4<<20)
		if e != nil {
			return Result{}, e
		}
		if e = u.verify(ctx, cosign, result.Version, checksums, bundle); e != nil {
			return Result{}, e
		}
	} else {
		result.Warning = "cosign is absent. The archive checksum was verified without a signature. Install cosign or use --require-signature."
	}
	binary, err := extract(archive, u.OS)
	if err != nil {
		return Result{}, err
	}
	if err = replace(path, binary, u.OS); err != nil {
		return Result{}, err
	}
	result.Updated = true
	return result, nil
}

func hasAsset(r release, name string) bool {
	for _, a := range r.Assets {
		if a.Name == name {
			return true
		}
	}
	return false
}

func verifyChecksum(archive, checksums []byte, name string) error {
	expected := ""
	count := 0
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			count++
			expected = fields[0]
		}
	}
	decoded, err := hex.DecodeString(expected)
	actual := sha256.Sum256(archive)
	if count != 1 || err != nil || len(decoded) != sha256.Size || !strings.EqualFold(expected, hex.EncodeToString(actual[:])) {
		return failure("the archive checksum is missing, duplicated, or does not match")
	}
	return nil
}

func (u Updater) verify(ctx context.Context, cosign, version string, checksums, bundle []byte) error {
	dir, err := os.MkdirTemp("", "spinup-verify-*")
	if err != nil {
		return failure(err)
	}
	defer os.RemoveAll(dir)
	checksumPath := filepath.Join(dir, "checksums.txt")
	bundlePath := filepath.Join(dir, "checksums.txt.sigstore.json")
	if err = os.WriteFile(checksumPath, checksums, 0600); err != nil {
		return failure(err)
	}
	if err = os.WriteFile(bundlePath, bundle, 0600); err != nil {
		return failure(err)
	}
	_, err = u.Runner.Run(ctx, runexec.Cmd{Name: cosign, Args: []string{"verify-blob", "--bundle", bundlePath, "--certificate-identity", repository + "/.github/workflows/release.yml@refs/tags/v" + version, "--certificate-oidc-issuer", "https://token.actions.githubusercontent.com", checksumPath}, Timeout: 30 * time.Second})
	if err != nil {
		return failure(fmt.Sprintf("checksum signature verification failed: %v", err))
	}
	return nil
}
