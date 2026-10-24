package digitalocean

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// Tag is an account tag.
type Tag struct {
	Name      string       `json:"name"`
	Resources TagResources `json:"resources,omitzero"`
}

// TagResources counts the resources that have a tag.
type TagResources struct {
	Count int `json:"count"`
}

// TagByName reads one tag. It returns nil, nil when the tag does not exist.
func (c *Client) TagByName(ctx context.Context, name string) (*Tag, error) {
	var out struct {
		Tag Tag `json:"tag"`
	}
	err := c.do(ctx, http.MethodGet, "/v2/tags/"+url.PathEscape(name), nil, &out)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out.Tag, nil
}

// EnsureTag makes sure the account has the tag. A droplet create makes its
// tags, but a firewall create refuses a tag that does not exist (HTTP 422).
// It reports whether it created the tag.
func (c *Client) EnsureTag(ctx context.Context, name string) (bool, error) {
	t, err := c.TagByName(ctx, name)
	if err != nil || t != nil {
		return false, err
	}
	err = c.do(ctx, http.MethodPost, "/v2/tags", Tag{Name: name}, nil)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusUnprocessableEntity {
		// Another run made the tag after the lookup. Look again.
		if t, gerr := c.TagByName(ctx, name); gerr == nil && t != nil {
			return false, nil
		}
	}
	return err == nil, err
}

// DeleteTag deletes a tag. A tag that does not exist is not an error.
func (c *Client) DeleteTag(ctx context.Context, name string) error {
	err := c.do(ctx, http.MethodDelete, "/v2/tags/"+url.PathEscape(name), nil, nil)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return nil
	}
	return err
}
