package upgrade

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

var planActionOrder = []Action{ActionConflict, ActionMerge, ActionOverwrite, ActionCreate, ActionRename, ActionDelete, ActionOrphan, ActionKeep, ActionUnchanged}

// Conflicts returns sorted paths whose decisions contain conflicts.
func (p *Plan) Conflicts() []string { return p.Summary()[ActionConflict] }

// Summary returns the sorted paths for each action present in the plan.
func (p *Plan) Summary() map[Action][]string {
	result := make(map[Action][]string)
	for _, d := range p.Files {
		result[d.Action] = append(result[d.Action], d.Path)
	}
	for action := range result {
		slices.Sort(result[action])
	}
	return result
}

// Format writes a stable plan grouped by action, with unchanged files as a count.
func (p *Plan) Format(w io.Writer) error {
	var out strings.Builder
	fmt.Fprintf(&out, "Upgrade %s -> %s\n", p.From, p.To)
	files := slices.Clone(p.Files)
	slices.SortFunc(files, func(a, b FileDecision) int { return strings.Compare(a.Path, b.Path) })
	for _, action := range planActionOrder {
		count := 0
		for _, d := range files {
			if d.Action == action {
				count++
			}
		}
		if count == 0 {
			continue
		}
		fmt.Fprintf(&out, "%s: %d\n", action, count)
		if action == ActionUnchanged {
			continue
		}
		for _, d := range files {
			if d.Action != action {
				continue
			}
			if d.From != "" {
				fmt.Fprintf(&out, "  %s -> %s", d.From, d.Path)
			} else {
				fmt.Fprintf(&out, "  %s", d.Path)
			}
			if d.Reason != "" {
				fmt.Fprintf(&out, " (%s)", d.Reason)
			}
			out.WriteByte('\n')
		}
	}
	if _, err := io.WriteString(w, out.String()); err != nil {
		return fmt.Errorf("format upgrade plan: %w\nTry: use a writable output", err)
	}
	return nil
}
