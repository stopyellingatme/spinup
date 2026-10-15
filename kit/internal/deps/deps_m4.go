//go:build deps

package deps

// M4 pre-seed.
import (
	_ "github.com/alexedwards/scs/pgxstore"
	_ "github.com/alexedwards/scs/v2"
	_ "golang.org/x/crypto/argon2"
)
