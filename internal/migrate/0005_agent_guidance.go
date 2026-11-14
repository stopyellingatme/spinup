package migrate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// AgentGuidanceID identifies the codemod that keeps the notes of an app that has its own
// AGENTS.md and CLAUDE.md (ADR 0035 section 3).
const AgentGuidanceID = "0005_agent_guidance"

const (
	agentsFile        = "AGENTS.md"
	agentsProjectFile = "AGENTS.project.md"
	claudeFile        = "CLAUDE.md"
	agentsNotesHeader = "\n## Notes from the former AGENTS.md\n\n"
)

// agentImports are the import lines of CLAUDE.md, in their order.
var agentImports = []string{"@AGENTS.md", "@AGENTS.project.md"}

func init() { Register(agentGuidance()) }

// agentEdit is one planned change of the codemod.
type agentEdit struct {
	change Change
	apply  func(*os.Root) error
}

func agentGuidance() Migration {
	return Migration{
		ID:    AgentGuidanceID,
		Since: "0.1.0-alpha.783",
		Desc:  "Keep the notes of an app that has its own AGENTS.md and CLAUDE.md",
		Stage: Before,
		Plan: func(c *Ctx) ([]Change, error) {
			edits, err := agentEdits(c)
			changes := make([]Change, 0, len(edits))
			for _, edit := range edits {
				changes = append(changes, edit.change)
			}
			return changes, err
		},
		Apply: func(c *Ctx) error {
			edits, err := agentEdits(c)
			if err != nil {
				return err
			}
			root, err := os.OpenRoot(c.Root)
			if err != nil {
				return agentGuidanceError(".", err)
			}
			defer root.Close()
			for _, edit := range edits {
				if err := edit.apply(root); err != nil {
					return agentGuidanceError(edit.change.Path, err)
				}
			}
			return nil
		},
	}
}

// agentEdits reads the tree and returns the edits in the order of ADR 0035 section 3.4.
// It writes nothing.
func agentEdits(c *Ctx) ([]agentEdit, error) {
	if answer, ok := c.Answers["agent_guidance"].(bool); ok && !answer {
		return nil, nil
	}
	if c.Lock != nil {
		if _, ok := c.Lock.Files[agentsFile]; ok {
			return nil, nil
		}
	}
	root, err := os.OpenRoot(c.Root)
	if err != nil {
		return nil, agentGuidanceError(".", err)
	}
	defer root.Close()
	agents, agentsExists, err := readOptional(root, agentsFile)
	if err != nil {
		return nil, err
	}
	project, projectExists, err := readOptional(root, agentsProjectFile)
	if err != nil {
		return nil, err
	}
	claude, claudeExists, err := readOptional(root, claudeFile)
	if err != nil {
		return nil, err
	}

	var edits []agentEdit
	switch {
	case !agentsExists:
		// No own AGENTS.md: the engine creates the new files. A run that stopped after the
		// move still has AGENTS.project.md, so it gets the import lines below.
	case !projectExists:
		edits = append(edits, agentEdit{
			change: Change{Path: agentsProjectFile, Desc: "Move AGENTS.md to AGENTS.project.md"},
			apply:  func(root *os.Root) error { return root.Rename(agentsFile, agentsProjectFile) },
		})
	default:
		merged := withFinalNewline(project) + agentsNotesHeader + withFinalNewline(agents)
		edits = append(edits, agentEdit{
			change: Change{Path: agentsProjectFile, Desc: "Append the notes of AGENTS.md to AGENTS.project.md, then delete AGENTS.md"},
			apply: func(root *os.Root) error {
				if err := writeKeepMode(root, agentsProjectFile, merged); err != nil {
					return err
				}
				return root.Remove(agentsFile)
			},
		})
	}
	// Without AGENTS.md and AGENTS.project.md, CLAUDE.md stays as it is (ADR 0035 3.4).
	if claudeExists && (agentsExists || projectExists) {
		if updated, changed := addAgentImports(claude); changed {
			edits = append(edits, agentEdit{
				change: Change{Path: claudeFile, Desc: "Import AGENTS.md and AGENTS.project.md in CLAUDE.md"},
				apply:  func(root *os.Root) error { return writeKeepMode(root, claudeFile, updated) },
			})
		}
	}
	return edits, nil
}

// addAgentImports appends the missing import lines to a CLAUDE.md text. It puts one blank
// line before the first new line, and the result ends with a newline.
func addAgentImports(text string) (string, bool) {
	present := map[string]bool{}
	for line := range strings.SplitSeq(text, "\n") {
		present[strings.TrimSpace(line)] = true
	}
	var missing []string
	for _, line := range agentImports {
		if !present[line] {
			missing = append(missing, line)
		}
	}
	if len(missing) == 0 {
		return text, false
	}
	out := withFinalNewline(text)
	if strings.TrimSpace(out) != "" {
		out += "\n"
	}
	return out + strings.Join(missing, "\n") + "\n", true
}

func withFinalNewline(text string) string {
	if text == "" || strings.HasSuffix(text, "\n") {
		return text
	}
	return text + "\n"
}

func readOptional(root *os.Root, path string) (string, bool, error) {
	info, err := root.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, agentGuidanceError(path, err)
	}
	if !info.Mode().IsRegular() {
		return "", false, agentGuidanceError(path, errors.New("it is not a regular file"))
	}
	data, err := root.ReadFile(path)
	if err != nil {
		return "", false, agentGuidanceError(path, err)
	}
	return string(data), true, nil
}

func writeKeepMode(root *os.Root, path, text string) error {
	mode := fs.FileMode(0o644)
	if info, err := root.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	return root.WriteFile(path, []byte(text), mode)
}

func agentGuidanceError(path string, err error) error {
	return fmt.Errorf("keep the agent notes in %s: %w\nTry: move your AGENTS.md to AGENTS.project.md by hand, add the lines @AGENTS.md and @AGENTS.project.md to CLAUDE.md, then run spinup upgrade --continue", path, err)
}
