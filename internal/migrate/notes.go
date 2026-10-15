package migrate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// KitNotes describes the kit changes supplied in MIGRATION.json.
type KitNotes struct {
	// Schema is the kit notes format version. Only schema 1 is supported.
	Schema int `json:"schema"`
	// Version is the kit version described by these notes.
	Version string `json:"version"`
	// Steps lists the changes in presentation order.
	Steps []KitStep `json:"steps"`
}

// KitStep describes a codemod, an inline fix, or a manual change.
type KitStep struct {
	// ID identifies the step within the notes.
	ID string `json:"id"`
	// Desc describes the change for the user.
	Desc string `json:"desc"`
	// Kind is codemod, fix-inline, or manual.
	Kind string `json:"kind"`
	// Migration names the registered migration for a codemod step.
	Migration string `json:"migration,omitempty"`
}

// ParseKitNotes decodes schema 1 kit notes and rejects unknown fields.
func ParseKitNotes(data []byte) (*KitNotes, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var notes *KitNotes
	if err := decoder.Decode(&notes); err != nil {
		return nil, notesError(err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, notesError(fmt.Errorf("expected one JSON document"))
	}
	if notes == nil {
		return nil, notesError(fmt.Errorf("expected a JSON object"))
	}
	if notes.Schema != 1 {
		return nil, notesError(fmt.Errorf("unsupported schema %d", notes.Schema))
	}
	for _, step := range notes.Steps {
		switch step.Kind {
		case "codemod":
			if strings.TrimSpace(step.Migration) == "" {
				return nil, notesError(fmt.Errorf("codemod step %q has no migration", step.ID))
			}
		case "fix-inline", "manual":
		default:
			return nil, notesError(fmt.Errorf("step %q has unknown kind %q", step.ID, step.Kind))
		}
	}
	return notes, nil
}

func notesError(err error) error {
	return fmt.Errorf("parse MIGRATION.json: %w\nTry: provide schema 1 kit notes with valid steps", err)
}
