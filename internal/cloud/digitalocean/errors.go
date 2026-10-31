package digitalocean

import (
	"fmt"
	"slices"
	"strings"
)

// APIError is an error answer from the DigitalOcean API.
type APIError struct {
	Status    int
	ID        string
	Message   string
	RequestID string
	Method    string
	Path      string
}

// Error describes the failure and ends with a Try line.
func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "DigitalOcean API call %s %s failed with HTTP %d", e.Method, e.Path, e.Status)
	if e.ID != "" {
		fmt.Fprintf(&b, " (%s)", e.ID)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	if e.RequestID != "" {
		fmt.Fprintf(&b, " [request %s]", e.RequestID)
	}
	b.WriteString("\n")
	b.WriteString(e.Try())
	return b.String()
}

// Try returns the Try line for the status.
func (e *APIError) Try() string {
	switch {
	case e.Status == 401:
		return "Try: run spinup deploy digital-ocean login, or check DIGITALOCEAN_ACCESS_TOKEN"
	case e.Status == 403:
		return fmt.Sprintf("Try: the token lacks a scope for %s %s. Run spinup deploy digital-ocean login again, or use a token with that scope", e.Method, e.Path)
	case e.Status == 422 && strings.Contains(strings.ToLower(e.Message), "droplet limit"):
		return "Try: raise the droplet limit in the DigitalOcean control panel, or destroy a droplet you do not use"
	case e.Status == 422 && containsAny(strings.ToLower(e.Message), "size", "region", "not available", "image"):
		return "Try: run spinup deploy digital-ocean --plan to list available sizes, or pass --size/--region/--image"
	case e.Status == 429:
		return "Try: wait a few minutes and run the command again"
	default:
		return "Try: check status.digitalocean.com and run the command again"
	}
}

func containsAny(value string, terms ...string) bool {
	return slices.ContainsFunc(terms, func(term string) bool { return strings.Contains(value, term) })
}
