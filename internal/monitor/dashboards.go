package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func (c *client) sync(ctx context.Context) error {
	// Paginate search so a large Grafana library cannot silently lose dashboards.
	var uids []string
	for page := 1; ; page++ {
		data, err := c.api(ctx, "grafana", "GET", fmt.Sprintf("/api/search?type=dash-db&limit=1000&page=%d", page), nil)
		if err != nil {
			return err
		}
		var results []struct {
			UID string `json:"uid"`
		}
		if err = json.Unmarshal(data, &results); err != nil {
			return failure("decode dashboard search", err)
		}
		for _, r := range results {
			uids = append(uids, r.UID)
		}
		if len(results) < 1000 {
			break
		}
	}
	sort.Strings(uids)
	for _, uid := range uids {
		if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`).MatchString(uid) {
			return failure("sync dashboard", fmt.Errorf("invalid dashboard UID"))
		}
		data, err := c.api(ctx, "grafana", "GET", "/api/dashboards/uid/"+url.PathEscape(uid), nil)
		if err != nil {
			return err
		}
		var response struct {
			Meta struct {
				Provisioned           bool
				ProvisionedExternalID string `json:"provisionedExternalId"`
			}
			Dashboard map[string]any
		}
		if err = json.Unmarshal(data, &response); err != nil {
			return failure("decode dashboard", err)
		}
		// Unknown provisioning sources are skipped conservatively. Custom providers can allow UI edits.
		source := "/" + strings.TrimPrefix(filepath.ToSlash(response.Meta.ProvisionedExternalID), "/")
		if strings.Contains(source, "/stack/") || response.Meta.Provisioned && response.Meta.ProvisionedExternalID == "" {
			continue
		}
		if response.Dashboard == nil || response.Dashboard["uid"] != uid {
			return failure("sync dashboard", fmt.Errorf("dashboard UID does not match the search result"))
		}
		delete(response.Dashboard, "id")
		pretty, err := json.MarshalIndent(response.Dashboard, "", "  ")
		if err != nil {
			return failure("format dashboard", err)
		}
		rel := filepath.Join("monitoring", "grafana", "dashboards", "custom", uid+".json")
		// os.Root prevents symlink traversal into stack/ or outside the project.
		root, err := os.OpenRoot(c.root)
		if err != nil {
			return failure("open dashboard directory", err)
		}
		err = writeDashboard(root, rel, append(pretty, '\n'))
		_ = root.Close()
		if err != nil {
			return failure("write dashboard", err)
		}
		fmt.Fprintln(c.out, filepath.ToSlash(rel))
	}
	return nil
}

func writeDashboard(root *os.Root, rel string, data []byte) error {
	// Reject symlink components, including links that remain inside the project.
	current := ""
	for _, part := range []string{"monitoring", "grafana", "dashboards", "custom", filepath.Base(rel)} {
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("dashboard path contains a symlink")
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := root.MkdirAll(filepath.Dir(rel), 0755); err != nil {
		return err
	}
	// Replace the directory entry so an existing hard link cannot modify a stack dashboard.
	dir, err := root.OpenRoot(filepath.Dir(rel))
	if err != nil {
		return err
	}
	defer dir.Close()
	name := filepath.Base(rel)
	temporary := "." + name + ".spinup-tmp"
	file, err := dir.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Remove(temporary) }()
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return dir.Rename(temporary, name)
}
