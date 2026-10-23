package digitalocean

import (
	"context"
	"net/http"
)

// Team is the team of an account.
type Team struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// Account is the account of the token.
type Account struct {
	Email         string `json:"email"`
	UUID          string `json:"uuid"`
	Status        string `json:"status"`
	DropletLimit  int    `json:"droplet_limit"`
	EmailVerified bool   `json:"email_verified"`
	Team          *Team  `json:"team,omitempty"`
}

// Account reads GET /v2/account.
func (c *Client) Account(ctx context.Context) (Account, error) {
	var out struct {
		Account Account `json:"account"`
	}
	err := c.do(ctx, http.MethodGet, "/v2/account", nil, &out)
	return out.Account, err
}
