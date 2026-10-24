package oracle

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

type diffCase struct {
	Encoding string `json:"encoding,omitempty"`
	Name     string `json:"name"`
	Old      []byte `json:"old"`
	New      []byte `json:"new"`
	Delete   bool   `json:"delete"`
	Expected []byte `json:"expected"`
}

func diffCases(t *testing.T) []diffCase {
	t.Helper()
	pairs := [][2]string{{"", ""}, {"", "a\nb\n"}, {"a\nb\n", ""}, {"a\na\n", "a\na\n"}, {"a\nx\nz\n", "a\ny\nz\n"}, {"x\na\ny\n", "u\na\nv\n"}, {"a\nb\nc\n", "b\na\nc\n"}, {"x\na\na\ny\n", "u\na\na\nv\n"}, {"a", "a\n"}, {"a\n", "a"}, {"a", "b"}, {"a\nz", "b\nz"}, {"a\x00", "b\x00"}, {"a\r\nb\r\n", "a\r\nc\r\n"}, {"é界\n", "😀\n"}, {"\xff\n", "valid\n"}}
	random := rand.New(rand.NewPCG(0x523035, 0x13))
	for range 240 {
		n := random.IntN(50)
		old := make([]string, n)
		for j := range old {
			old[j] = fmt.Sprintf("line %d\n", random.IntN(18))
		}
		next := append([]string{}, old...)
		for range 1 + random.IntN(12) {
			p := random.IntN(len(next) + 1)
			switch random.IntN(3) {
			case 0:
				next = append(next[:p], append([]string{fmt.Sprintf("insert %d\n", random.IntN(12))}, next[p:]...)...)
			case 1:
				if p < len(next) {
					next = append(next[:p], next[p+1:]...)
				}
			case 2:
				if p < len(next) {
					next[p] = fmt.Sprintf("replace %d\n", random.IntN(10))
				}
			}
		}
		a, b := strings.Join(old, ""), strings.Join(next, "")
		if random.IntN(2) == 0 {
			a = strings.TrimSuffix(a, "\n")
		}
		if random.IntN(2) == 0 {
			b = strings.TrimSuffix(b, "\n")
		}
		pairs = append(pairs, [2]string{a, b})
	}
	// Exercise separate hunks, joined context windows, and the binary scan boundary.
	pairs = append(pairs, [2]string{strings.Repeat("context\n", 10) + "old\n" + strings.Repeat("context\n", 20) + "tail\n", strings.Repeat("context\n", 10) + "new\n" + strings.Repeat("context\n", 20) + "end\n"})
	for _, size := range []int{7999, 8000, (1 << 20) - 1, 1 << 20, (1 << 20) + 1} {
		pairs = append(pairs, [2]string{strings.Repeat("a", size) + "\x00", "new\n"})
	}
	root := t.TempDir()
	cases := make([]diffCase, 0, len(pairs))
	for i, pair := range pairs {
		name := fmt.Sprintf("case-%03d.txt", i)
		before, after := []byte(pair[0]), []byte(pair[1])
		remove := len(before) > 0 && len(after) == 0
		if len(before) > 0 {
			if err := os.WriteFile(filepath.Join(root, name), before, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		plan := vfs.Plan{Files: []vfs.File{{Path: name, Data: after, Delete: remove}}}
		changes, err := vfs.Diff(root, plan)
		if err != nil {
			t.Fatal(err)
		}
		item := diffCase{Name: name, Old: before, New: after, Delete: remove, Expected: []byte(changes[0].UnifiedDiff)}
		if max(len(before), len(after)) > 8000 {
			encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
			if err != nil {
				t.Fatal(err)
			}
			item.Encoding = "zstd"
			item.Old = encoder.EncodeAll(item.Old, nil)
			item.New = encoder.EncodeAll(item.New, nil)
			item.Expected = encoder.EncodeAll(item.Expected, nil)
			if err := encoder.Close(); err != nil {
				t.Fatal(err)
			}
		}
		cases = append(cases, item)
	}
	return cases
}
