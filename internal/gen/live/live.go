package live

import (
	"fmt"
	"slices"

	"github.com/stopyellingatme/spinup/internal/gen/fields"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

// Dir contains the feed files of the application.
const Dir = "api/internal/live"

// GeneratedPath is the managed registration file.
const GeneratedPath = Dir + "/feeds_gen.go"

// RequirePack rejects generation without the live module.
func RequirePack(lock *manifest.Lock) error {
	if lock == nil || !slices.Contains(lock.Packs, "live") {
		return fmt.Errorf("cannot generate a live feed because the live module is missing\nTry: spinup module add live")
	}
	return nil
}

// FeedSpec describes the scope and access guard of a resource feed.
type FeedSpec struct {
	// Module is the module path of the API.
	Module string
	// Name is the resource name.
	Name fields.Name
	// Owned scopes the feed to its owner.
	Owned bool
	// Shared shares the feed between signed-in users.
	Shared bool
	// Roles limits access to these roles.
	Roles []string
}

// FeedPath returns the feed file path relative to the project root.
func FeedPath(name fields.Name) string { return Dir + "/" + name.PluralSnake() + ".go" }
