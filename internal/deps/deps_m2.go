//go:build deps

package deps

// M2 pre-seed.
import (
	_ "github.com/klauspost/compress/zstd"
	_ "golang.org/x/tools/go/ast/astutil"
)
