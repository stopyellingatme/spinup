package deploy

import (
	"fmt"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/services"
)

// PublicRoutesFile is the managed Caddy file of public services in a release directory.
const PublicRoutesFile = "Caddyfile.d/spinup-services.caddy"

// PublicRoutes renders the public service routes in service name order.
// It returns nil bytes and no error when no service has a public route.
// It does not change the input or write files.
func PublicRoutes(list []services.Service) ([]byte, error) {
	var public []services.Service
	for _, s := range list {
		if s.IsPublic() {
			public = append(public, s)
		}
	}
	if len(public) == 0 {
		return nil, nil
	}
	if err := services.Validate(list, nil); err != nil {
		return nil, fmt.Errorf("render public service routes: %w", err)
	}
	slices.SortFunc(public, func(a, b services.Service) int { return strings.Compare(a.Name, b.Name) })

	var out strings.Builder
	out.WriteString("# Managed by spinup deploy from spinup.toml. Do not edit.\n")
	fmt.Fprintf(&out, "# Do not use deploy/%s for a user file.\n", PublicRoutesFile)
	for _, s := range public {
		fmt.Fprintf(&out, "\nredir %s %s/ 308\nhandle_path %s/* {\n", s.Public, s.Public, s.Public)
		if s.AuthMode() == services.AuthSession {
			out.WriteString(`	route {
		@crossOrigin {
			not method GET HEAD OPTIONS
			not header Origin {http.request.scheme}://{http.request.host}
		}
		respond @crossOrigin 403
		import ../upstream.caddy api 8080 {
			method GET
			rewrite /api/session
			header_up X-Forwarded-Method {method}
			header_up X-Forwarded-Uri {http.request.orig_uri}
			@good status 2xx
			handle_response @good
		}
`)
			fmt.Fprintf(&out, "\t\timport ../upstream.caddy %s %d\n\t}\n", s.Name, s.Port)
		} else {
			fmt.Fprintf(&out, "\timport ../upstream.caddy %s %d\n", s.Name, s.Port)
		}
		out.WriteString("}\n")
	}
	return []byte(out.String()), nil
}
