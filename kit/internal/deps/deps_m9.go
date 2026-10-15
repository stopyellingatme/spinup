//go:build deps

package deps

// M9 pre-seed.
import (
	_ "github.com/coreos/go-oidc/v3/oidc"
	_ "github.com/wneessen/go-mail"
	_ "golang.org/x/oauth2"
)
