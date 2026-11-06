package resource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/stopyellingatme/spinup/internal/gen/resource/validation"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// validationReplacement checks compatibility before resource files enter the plan.
func validationReplacement(root string) ([]byte, bool, error) {
	source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(requiredFieldsPath)))
	if errors.Is(err, os.ErrNotExist) {
		return []byte(requiredFieldsGo), true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w\nTry: check the file permissions", requiredFieldsPath, err)
	}
	return validation.Upgrade(source)
}

// validationEdits authorizes replacement only after the exact-version check.
func validationEdits(plan vfs.Plan) []string {
	if _, ok := plan.Get(requiredFieldsPath); ok {
		return []string{requiredFieldsPath}
	}
	return nil
}
