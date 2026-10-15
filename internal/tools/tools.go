package tools

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/stackversions"
)

const maxDownloadSize = 200 << 20

var loadBinaries = func() (map[string]stackversions.Binary, error) {
	stack, err := stackversions.Load()
	if err != nil {
		return nil, err
	}
	return stack.Binaries, nil
}

// Options configures a tool installation or inspection.
type Options struct {
	Names      []string
	Dir        string
	GOOS       string
	GOARCH     string
	HTTPClient *http.Client
	BaseURL    func(repo, version, asset string) string
	Offline    bool
	Force      bool
	Runner     exec.Runner
}

// Installed reports one installed tool.
type Installed struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Path    string `json:"path"`
	Skipped bool   `json:"skipped"`
}

// Entry reports the state of one pinned tool.
type Entry struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Path        string `json:"path"`
	Installed   string `json:"installed"`
	PathFirst   string `json:"path_first"`
	PathMatches bool   `json:"path_matches"`
}

// Install downloads, verifies, and atomically installs the selected tools.
func Install(ctx context.Context, o Options) ([]Installed, error) {
	if o.Offline {
		return nil, errors.New("cannot install tools: offline mode disables downloads\nTry: remove --offline and retry")
	}
	binaries, err := loadBinaries()
	if err != nil {
		return nil, fmt.Errorf("load pinned tools: %w\nTry: reinstall spinup", err)
	}
	dir, err := targetDir(o.Dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create tools directory %q: %w\nTry: check its permissions", dir, err)
	}
	names, err := selectedNames(o.Names, binaries)
	if err != nil {
		return nil, err
	}
	platform := optionPlatform(o)
	client := o.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	baseURL := o.BaseURL
	if baseURL == nil {
		baseURL = defaultBaseURL
	}
	runner := o.Runner
	if runner == nil {
		runner = exec.OSRunner{}
	}
	installed := make([]Installed, 0, len(names))
	for _, name := range names {
		binary := binaries[name]
		asset, ok := binary.Assets[platform]
		if !ok {
			return nil, unsupportedPlatform(name, platform, binary)
		}
		path := filepath.Join(dir, executableName(name, o.GOOS))
		if !o.Force && currentVersion(ctx, runner, path, binary.Version) {
			installed = append(installed, Installed{Name: name, Version: binary.Version, Path: path, Skipped: true})
			continue
		}
		temporary, err := download(ctx, client, baseURL(binary.Repo, binary.Version, asset.Name), dir, asset.SHA256)
		if err != nil {
			return nil, fmt.Errorf("install %s: %w", name, err)
		}
		switch name {
		case "age":
			err = installAge(temporary, dir, o.GOOS)
		case "tofu":
			err = installTofu(temporary, path, o.GOOS)
		default:
			err = installBinary(temporary, path)
		}
		_ = os.Remove(temporary)
		if err != nil {
			return nil, fmt.Errorf("install %s: %w", name, err)
		}
		installed = append(installed, Installed{Name: name, Version: binary.Version, Path: path})
	}
	return installed, nil
}

// List returns each pinned tool and its local installation state.
func List(ctx context.Context, o Options) ([]Entry, error) {
	binaries, err := loadBinaries()
	if err != nil {
		return nil, fmt.Errorf("load pinned tools: %w\nTry: reinstall spinup", err)
	}
	dir, err := targetDir(o.Dir)
	if err != nil {
		return nil, err
	}
	names, err := selectedNames(o.Names, binaries)
	if err != nil {
		return nil, err
	}
	runner := o.Runner
	if runner == nil {
		runner = exec.OSRunner{}
	}
	entries := make([]Entry, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, executableName(name, o.GOOS))
		version := "missing"
		if output, err := versionOutput(ctx, runner, path); err == nil {
			version = output
		}
		first, _ := exec.LookPath(executableName(name, o.GOOS))
		entries = append(entries, Entry{Name: name, Version: binaries[name].Version, Path: path, Installed: version, PathFirst: first, PathMatches: first == path})
	}
	return entries, nil
}

// Path returns an installed spinup-managed tool path when it exists.
func Path(name string) (string, bool) {
	dir, err := targetDir("")
	if err != nil {
		return "", false
	}
	path := filepath.Join(dir, executableName(name, runtime.GOOS))
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return path, true
}

func targetDir(dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	if dir = os.Getenv("SPINUP_TOOLS_DIR"); dir != "" {
		return dir, nil
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate the user data directory: %w\nTry: set SPINUP_TOOLS_DIR", err)
	}
	return filepath.Join(root, "spinup", "bin"), nil
}

func selectedNames(names []string, binaries map[string]stackversions.Binary) ([]string, error) {
	if len(names) == 0 {
		names = []string{"sops", "age", "tofu"}
	}
	seen := make(map[string]bool, len(names))
	selected := make([]string, 0, len(names))
	for _, name := range names {
		if _, ok := binaries[name]; !ok {
			return nil, fmt.Errorf("unknown tool %q\nTry: use sops, age, or tofu", name)
		}
		if !seen[name] {
			seen[name] = true
			selected = append(selected, name)
		}
	}
	return selected, nil
}

func optionPlatform(o Options) string {
	goos, goarch := o.GOOS, o.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return goos + "-" + goarch
}
func unsupportedPlatform(name, platform string, binary stackversions.Binary) error {
	supported := make([]string, 0, len(binary.Assets))
	for item := range binary.Assets {
		supported = append(supported, item)
	}
	sort.Strings(supported)
	return fmt.Errorf("tool %q does not support %s; supported platforms: %s\nTry: install it manually for this platform", name, platform, strings.Join(supported, ", "))
}
func defaultBaseURL(repo, version, asset string) string {
	return "https://github.com/" + repo + "/releases/download/v" + version + "/" + asset
}

func download(ctx context.Context, client *http.Client, url, dir, expected string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create download request: %w\nTry: check the release URL", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("download release asset: %w\nTry: check the network connection and retry", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download release asset: server returned %s\nTry: retry the download", response.Status)
	}
	if response.ContentLength > maxDownloadSize {
		return "", errors.New("download release asset: asset exceeds 200 MiB\nTry: check the pinned release asset")
	}
	file, err := os.CreateTemp(dir, ".spinup-download-*")
	if err != nil {
		return "", fmt.Errorf("create temporary download: %w\nTry: check the tools directory permissions", err)
	}
	name := file.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(name)
		}
	}()
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, maxDownloadSize+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return "", fmt.Errorf("write release asset: %w\nTry: check disk space and retry", errors.Join(copyErr, closeErr))
	}
	if size > maxDownloadSize {
		return "", errors.New("download release asset: asset exceeds 200 MiB\nTry: check the pinned release asset")
	}
	actual := fmt.Sprintf("%x", hash.Sum(nil))
	if actual != expected {
		return "", fmt.Errorf("verify release asset checksum: got %s, want %s\nTry: retry the download or report a compromised release", actual, expected)
	}
	remove = false
	return name, nil
}

func installBinary(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read binary: %w\nTry: retry the download", err)
	}
	return installBytes(data, destination)
}
func installBytes(data []byte, destination string) error {
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".spinup-install-*")
	if err != nil {
		return fmt.Errorf("create temporary binary: %w\nTry: check the tools directory permissions", err)
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write binary: %w\nTry: check disk space and retry", err)
	}
	if err := temporary.Chmod(0755); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set binary mode: %w\nTry: check the tools directory permissions", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close binary: %w\nTry: check disk space and retry", err)
	}
	if err := os.Rename(name, destination); err != nil {
		return fmt.Errorf("activate binary: %w\nTry: check the tools directory permissions", err)
	}
	return nil
}

func installAge(source, dir, goos string) error {
	files, err := ageFiles(source, goos)
	if err != nil {
		return err
	}
	for _, name := range []string{"age", "age-keygen"} {
		if err := installBytes(files[name], filepath.Join(dir, executableName(name, goos))); err != nil {
			return err
		}
	}
	return nil
}
func ageFiles(source, goos string) (map[string][]byte, error) {
	if goos == "windows" {
		return ageZipFiles(source)
	}
	return ageTarFiles(source)
}
func ageTarFiles(source string) (map[string][]byte, error) {
	file, err := os.Open(source)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return nil, archiveError(err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, archiveError(err)
		}
		if header.Typeflag == tar.TypeDir && (header.Name == "age" || header.Name == "age/") {
			continue
		}
		if header.Typeflag != tar.TypeReg || (header.Name != "age/age" && header.Name != "age/age-keygen") {
			return nil, errors.New("release archive contains an unsafe path\nTry: check the pinned release asset")
		}
		name := strings.TrimPrefix(header.Name, "age/")
		if _, ok := files[name]; ok {
			return nil, errors.New("release archive contains duplicate binaries\nTry: check the pinned release asset")
		}
		data, err := io.ReadAll(io.LimitReader(reader, maxDownloadSize+1))
		if err != nil || int64(len(data)) != header.Size || len(data) > maxDownloadSize {
			return nil, archiveError(err)
		}
		files[name] = data
	}
	return requireAgeFiles(files)
}
func ageZipFiles(source string) (map[string][]byte, error) {
	archive, err := zip.OpenReader(source)
	if err != nil {
		return nil, archiveError(err)
	}
	defer archive.Close()
	files := map[string][]byte{}
	for _, file := range archive.File {
		if file.FileInfo().IsDir() && file.Name == "age/" {
			continue
		}
		if file.FileInfo().Mode()&os.ModeType != 0 || (file.Name != "age/age.exe" && file.Name != "age/age-keygen.exe") {
			return nil, errors.New("release archive contains an unsafe path\nTry: check the pinned release asset")
		}
		name := strings.TrimSuffix(strings.TrimPrefix(file.Name, "age/"), ".exe")
		if _, ok := files[name]; ok || file.UncompressedSize64 > maxDownloadSize {
			return nil, errors.New("release archive contains invalid binaries\nTry: check the pinned release asset")
		}
		reader, err := file.Open()
		if err != nil {
			return nil, archiveError(err)
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, maxDownloadSize+1))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || len(data) > maxDownloadSize {
			return nil, archiveError(errors.Join(readErr, closeErr))
		}
		files[name] = data
	}
	return requireAgeFiles(files)
}
func requireAgeFiles(files map[string][]byte) (map[string][]byte, error) {
	if len(files) != 2 || len(files["age"]) == 0 || len(files["age-keygen"]) == 0 {
		return nil, errors.New("release archive misses age binaries\nTry: check the pinned release asset")
	}
	return files, nil
}
func archiveError(err error) error {
	if err == nil {
		return errors.New("release archive is invalid\nTry: check the pinned release asset")
	}
	return fmt.Errorf("read release archive: %w\nTry: check the pinned release asset", err)
}
func executableName(name, goos string) string {
	if goos == "windows" {
		return name + ".exe"
	}
	return name
}
func currentVersion(ctx context.Context, runner exec.Runner, path, expected string) bool {
	actual, err := versionOutput(ctx, runner, path)
	return err == nil && hasVersion(actual, expected)
}
func versionOutput(ctx context.Context, runner exec.Runner, path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	result, err := runner.Run(ctx, exec.Cmd{Name: path, Args: []string{"--version"}})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(result.Stdout)), nil
}
func hasVersion(output, version string) bool {
	for _, field := range strings.FieldsFunc(output, func(r rune) bool { return (r < '0' || r > '9') && r != '.' && r != 'v' }) {
		if strings.TrimPrefix(field, "v") == version {
			return true
		}
	}
	return false
}
