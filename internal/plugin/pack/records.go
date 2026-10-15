package pack

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/plugin"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

type approval struct{ Source, SHA256 string }

func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'f') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}
func readAllowlist(root string, missingOK bool) (map[string]approval, error) {
	data, err := os.ReadFile(filepath.Join(root, plugin.AllowlistFile))
	if err != nil && (!missingOK || !os.IsNotExist(err)) {
		return nil, problem("read plugin allowlist", err)
	}
	result := map[string]approval{}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 || !namePattern.MatchString(fields[0]) || !validHash(fields[2]) {
			return nil, problem("read plugin allowlist", fmt.Errorf("invalid line %d", i+1))
		}
		if _, exists := result[fields[0]]; exists {
			return nil, problem("read plugin allowlist", fmt.Errorf("duplicate plugin %q", fields[0]))
		}
		result[fields[0]] = approval{Source: fields[1], SHA256: fields[2]}
	}
	return result, nil
}
func allowBytes(entries map[string]approval) []byte {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	var out strings.Builder
	for _, name := range names {
		entry := entries[name]
		fmt.Fprintf(&out, "%s %s %s\n", name, entry.Source, entry.SHA256)
	}
	return []byte(out.String())
}

type span struct{ start, end int }
type pluginBlock struct {
	span
	version span
}

func pluginBlocks(data []byte) ([]pluginBlock, error) {
	var blocks []pluginBlock
	parser := unstable.Parser{KeepComments: true}
	parser.Reset(data)
	active := -1
	for parser.NextExpression() {
		n := parser.Expression()
		if n.Kind == unstable.Table || n.Kind == unstable.ArrayTable {
			it := n.Key()
			var keys []string
			offset := 0
			for it.Next() {
				if len(keys) == 0 {
					offset = int(it.Node().Raw.Offset)
				}
				keys = append(keys, string(it.Node().Data))
			}
			start := bytes.LastIndexByte(data[:offset], '\n') + 1
			if active >= 0 {
				blocks[active].end = start
				active = -1
			}
			if n.Kind == unstable.ArrayTable && len(keys) == 1 && keys[0] == "plugins" {
				blocks = append(blocks, pluginBlock{span: span{start: start, end: len(data)}})
				active = len(blocks) - 1
			}
		}
		if n.Kind == unstable.KeyValue {
			it := n.Key()
			var keys []string
			for it.Next() {
				keys = append(keys, string(it.Node().Data))
			}
			if active >= 0 && len(keys) == 1 && keys[0] == "version" {
				value := n.Value().Raw
				blocks[active].version = span{start: int(value.Offset), end: int(value.Offset + value.Length)}
			}
		}
	}
	if err := parser.Error(); err != nil {
		return nil, problem("read plugin declarations", err)
	}
	return blocks, nil
}
func declarationBytes(data []byte, cfg *project.Config, decl project.Plugin, remove bool) ([]byte, error) {
	blocks, err := pluginBlocks(data)
	if err != nil {
		return nil, err
	}
	if len(blocks) != len(cfg.Plugins) {
		return nil, problem("edit plugin declarations", fmt.Errorf("plugins must use [[plugins]] tables"))
	}
	for i, entry := range cfg.Plugins {
		if entry.Name != decl.Name {
			continue
		}
		block := blocks[i]
		if remove {
			// Keep comment nodes, including comments after assignments.
			parser := unstable.Parser{KeepComments: true}
			parser.Reset(data[block.start:block.end])
			var comments bytes.Buffer
			var collect func(*unstable.Node)
			collect = func(n *unstable.Node) {
				if n.Kind == unstable.Comment {
					comments.Write(n.Data)
					comments.WriteByte('\n')
				}
				it := n.Children()
				for it.Next() {
					collect(it.Node())
				}
			}
			for parser.NextExpression() {
				for n := parser.Expression(); n != nil; n = n.Next() {
					collect(n)
				}
			}
			out := append(bytes.Clone(data[:block.start]), comments.Bytes()...)
			out = append(out, data[block.end:]...)
			if _, err := project.Parse(out); err != nil {
				return nil, problem("remove plugin declaration", err)
			}
			return out, nil
		}
		if block.version.end <= block.version.start {
			return nil, problem("update plugin declaration", fmt.Errorf("version key is missing"))
		}
		value, err := toml.Marshal(map[string]string{"version": decl.Version})
		if err != nil {
			return nil, err
		}
		_, raw, _ := bytes.Cut(value, []byte("="))
		out := append(bytes.Clone(data[:block.version.start]), bytes.TrimSpace(raw)...)
		out = append(out, data[block.version.end:]...)
		if _, err := project.Parse(out); err != nil {
			return nil, problem("update plugin declaration", err)
		}
		return out, nil
	}
	if remove {
		return nil, problem("remove plugin declaration", fmt.Errorf("plugin %q is not declared", decl.Name))
	}
	raw := []byte(fmt.Sprintf("\n[[plugins]]\nname = %s\nsource = %s\nversion = %s\n", tomlString(decl.Name), tomlString(decl.Source), tomlString(decl.Version)))
	out := append(bytes.Clone(data), raw...)
	if _, err := project.Parse(out); err != nil {
		return nil, problem("add plugin declaration", err)
	}
	return out, nil
}
func tomlString(s string) string {
	raw, _ := toml.Marshal(map[string]string{"v": s})
	_, value, _ := bytes.Cut(raw, []byte("="))
	return string(bytes.TrimSpace(value))
}

func (m *Manager) writeRecords(cfg *project.Config, lock *manifest.Lock, decl project.Plugin, loaded *plugin.Loaded) error {
	data, err := os.ReadFile(filepath.Join(m.Root, project.ConfigFile))
	if err != nil {
		return problem("read plugin declarations", err)
	}
	data, err = declarationBytes(data, cfg, decl, loaded == nil)
	if err != nil {
		return err
	}
	allow, err := readAllowlist(m.Root, true)
	if err != nil {
		return err
	}
	history, err := m.historyBytes(cfg, lock, allow, loaded)
	if err != nil {
		return err
	}
	entries := make([]manifest.Plugin, 0, len(lock.Plugins)+1)
	for _, entry := range lock.Plugins {
		if entry.Name != decl.Name {
			entries = append(entries, entry)
		}
	}
	delete(allow, decl.Name)
	if loaded != nil {
		entries = append(entries, manifest.Plugin{Name: decl.Name, Version: decl.Version, API: loaded.Meta.API, SHA256: loaded.SHA256})
		allow[decl.Name] = approval{Source: decl.Source, SHA256: loaded.SHA256}
	}
	lock.Plugins = entries
	locked, err := lock.Marshal()
	if err != nil {
		return err
	}
	var plan vfs.Plan
	for _, file := range []vfs.File{{Path: project.ConfigFile, Data: data, Mode: 0644}, {Path: project.LockFile, Data: locked, Mode: 0644}, {Path: plugin.AllowlistFile, Data: allowBytes(allow), Mode: 0644}, {Path: historyFile, Data: history, Mode: 0644}} {
		if err := plan.Add(file); err != nil {
			return err
		}
	}
	if _, err := vfs.Apply(m.Root, plan, vfs.ApplyOptions{DryRun: m.DryRun}); err != nil {
		return problem("write plugin records", err)
	}
	return nil
}
