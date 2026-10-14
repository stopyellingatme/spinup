package vfs

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
)

type diffLine struct {
	kind byte
	text string
}

func fileDiff(name string, old, new []byte) string {
	if size := max(len(old), len(new)); size > 1<<20 {
		return fmt.Sprintf("File too large to diff (%d bytes)", size)
	}
	if isBinary(old) || isBinary(new) {
		return "Binary files differ"
	}
	oldLines := splitLines(old)
	newLines := splitLines(new)
	lines := patienceDiff(oldLines, newLines)
	changed := make([]bool, len(lines))
	for i, line := range lines {
		changed[i] = line.kind != ' '
	}
	hunks := make([][2]int, 0)
	for i, hasChange := range changed {
		if !hasChange {
			continue
		}
		start, end := max(0, i-3), min(len(lines), i+4)
		if len(hunks) > 0 && start <= hunks[len(hunks)-1][1] {
			if end > hunks[len(hunks)-1][1] {
				hunks[len(hunks)-1][1] = end
			}
			continue
		}
		hunks = append(hunks, [2]int{start, end})
	}
	if len(hunks) == 0 {
		return ""
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "--- a/%s\n+++ b/%s\n", name, name)
	for _, hunk := range hunks {
		oldStart, oldCount, newStart, newCount := hunkRange(lines, hunk[0], hunk[1])
		fmt.Fprintf(&builder, "@@ -%s +%s @@\n", rangeText(oldStart, oldCount), rangeText(newStart, newCount))
		for _, line := range lines[hunk[0]:hunk[1]] {
			builder.WriteByte(line.kind)
			builder.WriteString(strings.TrimSuffix(line.text, "\n"))
			builder.WriteByte('\n')
			if !strings.HasSuffix(line.text, "\n") {
				builder.WriteString("\\ No newline at end of file\n")
			}
		}
	}
	return builder.String()
}

func isBinary(data []byte) bool {
	if len(data) > 8000 {
		data = data[:8000]
	}
	return bytes.IndexByte(data, 0) >= 0
}

func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// patienceDiff anchors on unique shared lines and finds their longest increasing
// subsequence. Regions without anchors become deletions followed by additions.
// The explicit stack avoids recursive depth growth. Memory is linear in input.
func patienceDiff(old, new []string) []diffLine {
	type region struct {
		old, new []string
		equal    bool
	}
	lines := make([]diffLine, 0, len(old)+len(new))
	stack := []region{{old: old, new: new}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		old, new := current.old, current.new
		if current.equal {
			for _, text := range old {
				lines = append(lines, diffLine{kind: ' ', text: text})
			}
			continue
		}
		prefix := 0
		for prefix < min(len(old), len(new)) && old[prefix] == new[prefix] {
			lines = append(lines, diffLine{kind: ' ', text: old[prefix]})
			prefix++
		}
		old, new = old[prefix:], new[prefix:]
		suffix := 0
		for suffix < min(len(old), len(new)) && old[len(old)-suffix-1] == new[len(new)-suffix-1] {
			suffix++
		}
		if suffix > 0 {
			stack = append(stack, region{old: old[len(old)-suffix:], equal: true})
			old, new = old[:len(old)-suffix], new[:len(new)-suffix]
		}
		anchors := patienceAnchors(old, new)
		if len(anchors) == 0 {
			for _, text := range old {
				lines = append(lines, diffLine{kind: '-', text: text})
			}
			for _, text := range new {
				lines = append(lines, diffLine{kind: '+', text: text})
			}
			continue
		}
		oldEnd, newEnd := len(old), len(new)
		for i := len(anchors) - 1; i >= 0; i-- {
			anchor := anchors[i]
			stack = append(stack,
				region{old: old[anchor.old+1 : oldEnd], new: new[anchor.new+1 : newEnd]},
				region{old: old[anchor.old : anchor.old+1], equal: true},
			)
			oldEnd, newEnd = anchor.old, anchor.new
		}
		stack = append(stack, region{old: old[:oldEnd], new: new[:newEnd]})
	}
	return lines
}

type lineAnchor struct {
	old, new int
}

func patienceAnchors(old, new []string) []lineAnchor {
	if len(old) == 0 || len(new) == 0 {
		return nil
	}
	type occurrence struct {
		index, count int
	}
	occurrences := func(lines []string) map[string]occurrence {
		counts := make(map[string]occurrence, len(lines))
		for i, line := range lines {
			entry := counts[line]
			entry.index, entry.count = i, entry.count+1
			counts[line] = entry
		}
		return counts
	}
	oldCounts, newCounts := occurrences(old), occurrences(new)
	anchors := make([]lineAnchor, 0, min(len(old), len(new)))
	for i, line := range old {
		if other := newCounts[line]; oldCounts[line].count == 1 && other.count == 1 {
			anchors = append(anchors, lineAnchor{old: i, new: other.index})
		}
	}
	tails := make([]int, 0, len(anchors))
	previous := make([]int, len(anchors))
	for i, anchor := range anchors {
		position := sort.Search(len(tails), func(j int) bool { return anchors[tails[j]].new >= anchor.new })
		previous[i] = -1
		if position > 0 {
			previous[i] = tails[position-1]
		}
		if position == len(tails) {
			tails = append(tails, i)
		} else {
			tails[position] = i
		}
	}
	selected := make([]lineAnchor, len(tails))
	if len(tails) > 0 {
		index := tails[len(tails)-1]
		for i := len(selected) - 1; i >= 0; i-- {
			selected[i] = anchors[index]
			index = previous[index]
		}
	}
	return selected
}

func hunkRange(lines []diffLine, start, end int) (int, int, int, int) {
	oldStart, newStart := 1, 1
	for _, line := range lines[:start] {
		if line.kind != '+' {
			oldStart++
		}
		if line.kind != '-' {
			newStart++
		}
	}
	oldCount, newCount := 0, 0
	for _, line := range lines[start:end] {
		if line.kind != '+' {
			oldCount++
		}
		if line.kind != '-' {
			newCount++
		}
	}
	if oldCount == 0 {
		oldStart--
	}
	if newCount == 0 {
		newStart--
	}
	return oldStart, oldCount, newStart, newCount
}

func rangeText(start, count int) string {
	if count == 1 {
		return fmt.Sprint(start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}
