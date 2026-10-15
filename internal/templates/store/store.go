package store

import (
	"context"
	"crypto/ed25519"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/compat"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates"
)

//go:embed embedded/*
var archives embed.FS

// TrustedKeys contains the release keys accepted for template downloads and cached archives.
// Release signing adds these keys in M8.3.
var TrustedKeys []ed25519.PublicKey

// Options configures template sources, cache storage, and download verification.
type Options struct {
	Current    *templates.Version
	Embedded   fs.FS
	CacheDir   string
	ExtraDir   string
	Offline    bool
	HTTPClient *http.Client
	BaseURL    string
	Keys       []ed25519.PublicKey
}

// Store resolves template versions in source priority order.
type Store struct{ options Options }

var _ templates.Source = (*Store)(nil)

// New creates a store without accessing the network or writing files.
func New(o Options) (*Store, error) {
	if o.Current != nil {
		if err := validateVersion(o.Current.Version); err != nil {
			return nil, err
		}
	}
	if o.Embedded == nil {
		var err error
		o.Embedded, err = fs.Sub(archives, "embedded")
		if err != nil {
			return nil, fmt.Errorf("cannot open embedded archives: %w\nTry: reinstall spinup", err)
		}
	}
	if o.CacheDir == "" {
		root, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("cannot locate template cache: %w\nTry: configure the user cache directory", err)
		}
		o.CacheDir = filepath.Join(root, "spinup", "templates")
	}
	if o.BaseURL == "" {
		o.BaseURL = "https://github.com/stopyellingatme/spinup/releases/download"
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	if o.Keys == nil {
		o.Keys = TrustedKeys
	}
	keys := make([]ed25519.PublicKey, len(o.Keys))
	for i, key := range o.Keys {
		keys[i] = append(ed25519.PublicKey(nil), key...)
	}
	o.Keys = keys
	return &Store{options: o}, nil
}

// Default creates a store with this build and the template environment settings.
func Default(cliVersion string, offline bool) (*Store, error) {
	return New(Options{Current: Current(cliVersion), ExtraDir: os.Getenv("SPINUP_TEMPLATES_DIR"), Offline: offline || os.Getenv("SPINUP_OFFLINE") == "1"})
}

// Entry identifies a loadable local version and its selected source.
type Entry struct {
	Version string `json:"version"`
	Source  string `json:"source"`
}

// List reports local versions and sources without downloading archives.
func (s *Store) List(ctx context.Context) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, s.unavailable("", err)
	}
	candidates := map[string]bool{}
	if s.options.Current != nil {
		candidates[s.options.Current.Version] = true
	}
	embedded, err := fs.ReadDir(s.options.Embedded, ".")
	if err != nil {
		return nil, s.unavailable("", err)
	}
	for _, entry := range embedded {
		if !entry.IsDir() {
			addArchiveVersion(candidates, entry.Name())
		}
	}
	for _, dir := range []string{s.options.CacheDir, s.options.ExtraDir} {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, s.unavailable("", err)
		}
		for _, entry := range entries {
			if dir == s.options.ExtraDir && entry.IsDir() && validateVersion(entry.Name()) == nil {
				candidates[entry.Name()] = true
			}
			if dir == s.options.CacheDir && !entry.IsDir() {
				addArchiveVersion(candidates, entry.Name())
			}
		}
	}
	versions := make([]string, 0, len(candidates))
	for version := range candidates {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool {
		// Validation precedes sorting. Build metadata does not affect precedence.
		left, _, _ := strings.Cut(versions[i], "+")
		right, _, _ := strings.Cut(versions[j], "+")
		a, _ := compat.ParseVersion(left)
		b, _ := compat.ParseVersion(right)
		if cmp := a.Compare(b); cmp != 0 {
			return cmp < 0
		}
		return versions[i] < versions[j]
	})
	result := make([]Entry, 0, len(versions))
	for _, version := range versions {
		if err := ctx.Err(); err != nil {
			return nil, s.unavailable(version, err)
		}
		_, source, err := s.local(ctx, version)
		if err == nil {
			result = append(result, Entry{Version: version, Source: source})
		}
	}
	return result, nil
}

func addArchiveVersion(versions map[string]bool, name string) {
	if !strings.HasPrefix(name, "templates-v") || !strings.HasSuffix(name, ".tar.zst") {
		return
	}
	version := strings.TrimSuffix(strings.TrimPrefix(name, "templates-v"), ".tar.zst")
	if validateVersion(version) == nil {
		versions[version] = true
	}
}

// Versions lists the loadable local versions in ascending semantic order.
func (s *Store) Versions(ctx context.Context) ([]string, error) {
	entries, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	versions := make([]string, 0, len(entries))
	for _, entry := range entries {
		versions = append(versions, entry.Version)
	}
	return versions, nil
}

// Load resolves a template version and verifies cached or downloaded archive signatures.
func (s *Store) Load(ctx context.Context, version string) (*templates.Version, error) {
	if err := validateVersion(version); err != nil {
		return nil, err
	}
	v, source, err := s.local(ctx, version)
	if err == nil {
		return v, nil
	}
	if source == "cache" {
		if s.options.Offline {
			return nil, s.invalidCache(version, err)
		}
		v, err = s.download(ctx, version)
		if err != nil {
			return nil, s.unavailable(version, err)
		}
		return v, nil
	}
	if source != "" || !errors.Is(err, fs.ErrNotExist) || s.options.Offline {
		return nil, s.unavailable(version, err)
	}
	v, err = s.download(ctx, version)
	if err != nil {
		return nil, s.unavailable(version, err)
	}
	return v, nil
}

func (s *Store) local(ctx context.Context, version string) (*templates.Version, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if s.options.Current != nil && s.options.Current.Version == version {
		return s.options.Current, "current", nil
	}
	if s.options.ExtraDir != "" {
		dir := filepath.Join(s.options.ExtraDir, version)
		if _, err := os.Stat(dir); err == nil {
			v, err := readExtra(dir, version)
			// A malformed existing source must not trigger a network fallback.
			if err != nil {
				return nil, "extra", fmt.Errorf("invalid extra version: %w", err)
			}
			return v, "extra", nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, "", err
		}
	}
	data, err := readFS(s.options.Embedded, archiveName(version), maxArchive)
	if err == nil {
		v, err := ReadArchive(data, version)
		return v, "embedded", err
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, "", err
	}
	cached := os.DirFS(s.options.CacheDir)
	data, err = readFS(cached, archiveName(version), maxArchive)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "", err
		}
		return nil, "cache", err
	}
	sig, err := readFS(cached, archiveName(version)+".sig", ed25519.SignatureSize)
	if err != nil {
		return nil, "cache", fmt.Errorf("cannot read cached signature: %w", err)
	}
	if err := s.verify(data, sig); err != nil {
		return nil, "cache", err
	}
	v, err := ReadArchive(data, version)
	return v, "cache", err
}

func readExtra(dir, version string) (*templates.Version, error) {
	source := os.DirFS(dir)
	stack, err := readFS(source, "stackversions.yaml", maxUnpacked)
	if err != nil {
		return nil, err
	}
	pinned, err := stackversions.Parse(stack)
	if err != nil {
		return nil, err
	}
	info, err := fs.Stat(source, "packs")
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("packs is not a directory")
	}
	packs, err := fs.Sub(source, "packs")
	if err != nil {
		return nil, err
	}
	notes, err := readFS(source, "MIGRATION.json", maxUnpacked)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return &templates.Version{Version: version, Packs: packs, Stack: pinned, KitMigration: notes}, nil
}

func (s *Store) unavailable(version string, reason error) error {
	suggestion := "spinup cache warm " + version
	if s.options.Offline {
		suggestion = "run once without --offline"
	}
	return fmt.Errorf("cannot load templates %q: %w: %w\nTry: %s", version, templates.ErrVersionUnavailable, reason, strings.TrimSpace(suggestion))
}

func (s *Store) invalidCache(version string, reason error) error {
	return fmt.Errorf("cannot load templates %q: %w: %w\nTry: spinup cache clean", version, templates.ErrVersionUnavailable, reason)
}

// Warm downloads and verifies versions that have no loadable local source.
func (s *Store) Warm(ctx context.Context, versions []string) error {
	if s.options.Offline {
		return fmt.Errorf("cannot warm templates: offline mode disables downloads\nTry: remove --offline and unset SPINUP_OFFLINE")
	}
	// Validate the entire request before constructing paths or downloading anything.
	for _, version := range versions {
		if err := validateVersion(version); err != nil {
			return err
		}
	}
	ordered := append([]string(nil), versions...)
	sort.Strings(ordered)
	for _, version := range ordered {
		if _, err := s.Load(ctx, version); err != nil {
			return err
		}
	}
	return nil
}

// WarmProject warms the version recorded by the nearest project lock.
func (s *Store) WarmProject(ctx context.Context, start string) (string, error) {
	if start == "" {
		start = "."
	}
	root, err := project.FindRoot(start)
	if err != nil {
		return "", fmt.Errorf("cannot locate project templates: %w\nTry: run inside a spinup project or pass --dir <directory>", err)
	}
	lock, err := manifest.Load(filepath.Join(root, project.LockFile))
	if err != nil {
		return "", err
	}
	return lock.TemplateVersion, s.Warm(ctx, []string{lock.TemplateVersion})
}

// CacheDir returns the directory used for downloaded archives and signatures.
func (s *Store) CacheDir() string { return s.options.CacheDir }

// Clean removes the template cache directory and its contents.
func (s *Store) Clean() error {
	if err := os.RemoveAll(s.options.CacheDir); err != nil {
		return fmt.Errorf("cannot remove template cache: %w\nTry: check the cache directory permissions", err)
	}
	return nil
}
