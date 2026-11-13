package webbundle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/stopyellingatme/spinup/internal/doctor"
)

const (
	defaultBrotliLimit int64 = 400_000
	defaultRawLimit    int64 = 1_500_000
)

func init() {
	doctor.Register(doctor.Check{ID: "web-bundle", Title: "Web bundle budget", Scope: doctor.Local, Run: runCheck})
}

type budgetFile struct {
	ClientBrotliBytes *int64 `json:"client_br_bytes"`
	ClientRawBytes    *int64 `json:"client_raw_bytes"`
}

type asset struct {
	path  string
	bytes int64
}

func runCheck(_ context.Context, c *doctor.Context) doctor.Result {
	web := filepath.Join(c.Root, "web")
	info, err := os.Stat(web)
	if errors.Is(err, os.ErrNotExist) || err == nil && !info.IsDir() {
		return skip("no web directory")
	}
	if err != nil {
		return failure("cannot inspect web: %v", err)
	}
	build := filepath.Join(web, "build")
	if _, err := os.Stat(build); errors.Is(err, os.ErrNotExist) {
		return skip("web/build is missing: build the web app first")
	} else if err != nil {
		return failure("cannot inspect web/build: %v", err)
	}
	brLimit, rawLimit, err := budget(web)
	if err != nil {
		return failure("cannot read web/bundle-budget.json: %v", err)
	}
	client := filepath.Join(build, "client", "_app", "immutable")
	if _, err := os.Stat(client); errors.Is(err, os.ErrNotExist) {
		client = filepath.Join(build, "_app", "immutable")
	} else if err != nil {
		return failure("cannot inspect client assets: %v", err)
	}
	raw, brotli, assets, err := bundle(client)
	if err != nil {
		return failure("cannot read web build: %v", err)
	}
	if raw <= rawLimit && brotli <= brLimit {
		return doctor.Result{Status: doctor.OK, Message: fmt.Sprintf("client bundle: %s raw, %s brotli", byteCount(raw), byteCount(brotli))}
	}
	parts := make([]string, 0, 2)
	if raw > rawLimit {
		parts = append(parts, fmt.Sprintf("%s raw (limit %s)", byteCount(raw), byteCount(rawLimit)))
	}
	if brotli > brLimit {
		parts = append(parts, fmt.Sprintf("%s brotli (limit %s)", byteCount(brotli), byteCount(brLimit)))
	}
	return doctor.Result{
		Status:  doctor.Warn,
		Message: fmt.Sprintf("client bundle exceeds the budget: %s; largest files: %s", strings.Join(parts, ", "), largest(assets)),
		Try:     "reduce client JavaScript or CSS, or update web/bundle-budget.json",
	}
}

func budget(web string) (int64, int64, error) {
	path := filepath.Join(web, "bundle-budget.json")
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaultBrotliLimit, defaultRawLimit, nil
	}
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var value budgetFile
	if err := decoder.Decode(&value); err != nil {
		return 0, 0, err
	}
	if err := ensureEOF(decoder); err != nil {
		return 0, 0, err
	}
	brLimit, rawLimit := defaultBrotliLimit, defaultRawLimit
	if value.ClientBrotliBytes != nil {
		brLimit = *value.ClientBrotliBytes
	}
	if value.ClientRawBytes != nil {
		rawLimit = *value.ClientRawBytes
	}
	if brLimit < 0 || rawLimit < 0 {
		return 0, 0, errors.New("budget values must not be negative")
	}
	return brLimit, rawLimit, nil
}

func ensureEOF(decoder *json.Decoder) error {
	var value any
	err := decoder.Decode(&value)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}

func bundle(root string) (int64, int64, []asset, error) {
	var raw, brotli int64
	var assets []asset
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() || !isAsset(path) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		raw += info.Size()
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		assets = append(assets, asset{path: filepath.ToSlash(rel), bytes: info.Size()})
		br, err := os.Stat(path + ".br")
		if err == nil && br.Mode().IsRegular() {
			brotli += br.Size()
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
	return raw, brotli, assets, err
}

func isAsset(path string) bool {
	extension := strings.ToLower(filepath.Ext(path))
	return extension == ".js" || extension == ".css"
}

func largest(assets []asset) string {
	sort.Slice(assets, func(i, j int) bool {
		if assets[i].bytes != assets[j].bytes {
			return assets[i].bytes > assets[j].bytes
		}
		return assets[i].path < assets[j].path
	})
	if len(assets) > 3 {
		assets = assets[:3]
	}
	items := make([]string, 0, len(assets))
	for _, asset := range assets {
		items = append(items, fmt.Sprintf("%s (%s)", asset.path, byteCount(asset.bytes)))
	}
	return strings.Join(items, ", ")
}

func byteCount(value int64) string {
	text := strconv.FormatInt(value, 10)
	start := len(text) % 3
	if start == 0 {
		start = 3
	}
	for index := start; index < len(text); index += 4 {
		text = text[:index] + "," + text[index:]
	}
	return text + " bytes"
}

func skip(message string) doctor.Result {
	return doctor.Result{Status: doctor.Skip, Message: message}
}

func failure(format string, args ...any) doctor.Result {
	return doctor.Result{
		Status:  doctor.Fail,
		Message: fmt.Sprintf(format, args...),
		Try:     "correct web/bundle-budget.json and run spinup doctor again",
	}
}
