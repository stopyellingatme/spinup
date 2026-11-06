package migrate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/stopyellingatme/spinup/internal/gen/resource/validation"
)

// ValidationKnownFieldsID identifies the generated JSON middleware codemod.
const ValidationKnownFieldsID = "0002_validation_known_fields"

func init() { Register(validationKnownFields()) }

func validationKnownFields() Migration {
	return Migration{
		ID:    ValidationKnownFieldsID,
		Since: "0.1.0-alpha.419",
		Desc:  "Limit and validate JSON bodies in generated resource routes",
		Stage: After,
		Plan: func(c *Ctx) ([]Change, error) {
			source, err := readValidation(c.Root)
			if err != nil || source == nil {
				return nil, err
			}
			_, changed, err := validation.Upgrade(source)
			if err != nil {
				return []Change{{Path: validation.Path, Desc: "Manual step: " + err.Error()}}, err
			}
			if !changed {
				return nil, nil
			}
			return []Change{{Path: validation.Path, Desc: "Add knownFields and validate JSON bodies with a 1 MiB limit"}}, nil
		},
		Apply: func(c *Ctx) error {
			source, err := readValidation(c.Root)
			if err != nil || source == nil {
				return err
			}
			data, changed, err := validation.Upgrade(source)
			if err != nil || !changed {
				return err
			}
			if err := os.WriteFile(filepath.Join(c.Root, filepath.FromSlash(validation.Path)), data, 0o644); err != nil {
				return fmt.Errorf("write %s: %w\nTry: check the file permissions", validation.Path, err)
			}
			return nil
		},
	}
}

func readValidation(root string) ([]byte, error) {
	path := filepath.Join(root, filepath.FromSlash(validation.Path))
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("check %s: %w\nTry: check the file permissions", validation.Path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("cannot migrate %s: the path is not a regular file\nTry: replace the path with a regular middleware file", validation.Path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w\nTry: check the file permissions", validation.Path, err)
	}
	return data, nil
}
