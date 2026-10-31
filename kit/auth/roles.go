package auth

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var roleNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// Roles declares the role names and their administrator and default roles.
type Roles struct {
	// Names lists the declared roles in their configured order.
	Names []string
	// Admin is the role that identifies administrators.
	Admin string
	// Default is the role for new accounts.
	Default string
}

// DefaultRoles returns a new copy of the default role set.
func DefaultRoles() Roles {
	return Roles{Names: []string{"manager", "staff"}, Admin: "manager", Default: "staff"}
}

// WithRoles sets the declared roles. New validates the set before starting.
func WithRoles(r Roles) Option {
	r.Names = slices.Clone(r.Names)
	return func(o *options) {
		o.roles = r
		o.roles.Names = slices.Clone(r.Names)
	}
}

// Roles returns a copy of the declared role set.
func (a *Auth) Roles() Roles {
	r := a.roles
	r.Names = slices.Clone(r.Names)
	return r
}

// Valid reports whether role is a declared name.
func (r Roles) Valid(role string) bool { return slices.Contains(r.Names, role) }

// HasRole reports whether the user has one of the given roles.
func (u User) HasRole(roles ...string) bool { return u.Role != "" && slices.Contains(roles, u.Role) }

// Validate checks the role names, generated constants, and selected roles.
func (r Roles) Validate() error {
	const hint = "\nTry: correct the role set passed to auth.WithRoles."
	if len(r.Names) < 1 || len(r.Names) > 16 {
		return fmt.Errorf("auth roles: list length %d must be 1 to 16.%s", len(r.Names), hint)
	}
	names, constants := make(map[string]bool), make(map[string]string)
	for _, name := range r.Names {
		if !roleNamePattern.MatchString(name) {
			return fmt.Errorf("auth role %q must match %s.%s", name, roleNamePattern.String(), hint)
		}
		if strings.Contains(name, "__") {
			return fmt.Errorf("auth role %q must have no double underscore.%s", name, hint)
		}
		if strings.HasSuffix(name, "_") {
			return fmt.Errorf("auth role %q must have no trailing underscore.%s", name, hint)
		}
		if names[name] {
			return fmt.Errorf("auth role %q is repeated: names must be unique.%s", name, hint)
		}
		names[name] = true
		constant := roleConst(name)
		if constant == "Set" {
			return fmt.Errorf("auth role %q has reserved RoleConst value Set.%s", name, hint)
		}
		if previous, exists := constants[constant]; exists {
			return fmt.Errorf("auth roles %q and %q have the same RoleConst value %q.%s", previous, name, constant, hint)
		}
		constants[constant] = name
	}
	if !names[r.Admin] {
		return fmt.Errorf("auth admin_role %q must be in roles.%s", r.Admin, hint)
	}
	if !names[r.Default] {
		return fmt.Errorf("auth default_role %q must be in roles.%s", r.Default, hint)
	}
	return nil
}

// roleConst converts a role name such as front_desk to FrontDesk.
func roleConst(name string) string {
	var out strings.Builder
	for _, part := range strings.Split(name, "_") {
		if part != "" {
			out.WriteString(strings.ToUpper(part[:1]))
			out.WriteString(part[1:])
		}
	}
	return out.String()
}
