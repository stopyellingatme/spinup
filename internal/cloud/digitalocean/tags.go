package digitalocean

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// Tag is an account tag.
type Tag struct {
	Name string `json:"name"`
}

// TagExists reports whether the account has the tag.
func (c *Client) TagExists(ctx context.Context, name string) (bool, error) {
	var out struct {
		Tag Tag `json:"tag"`
	}
	err := c.do(ctx, http.MethodGet, "/v2/tags/"+url.PathEscape(name), nil, &out)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return false, nil
	}
	return err == nil, err
}

// EnsureTag makes sure the account has the tag. A droplet create makes its
// tags, but a firewall create refuses a tag that does not exist (HTTP 422).
// It reports whether it created the tag.
func (c *Client) EnsureTag(ctx context.Context, name string) (bool, error) {
	ok, err := c.TagExists(ctx, name)
	if err != nil || ok {
		return false, err
	}
	err = c.do(ctx, http.MethodPost, "/v2/tags", Tag{Name: name}, nil)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusUnprocessableEntity {
		// Another run made the tag after the lookup. Look again.
		if ok, gerr := c.TagExists(ctx, name); gerr == nil && ok {
			return false, nil
		}
	}
	return err == nil, err
}
