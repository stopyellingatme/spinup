package digitalocean

import "context"

// Size is a droplet size. Memory is in MiB.
type Size struct {
	Slug         string   `json:"slug"`
	VCPUs        int      `json:"vcpus"`
	Memory       int      `json:"memory"`
	Disk         int      `json:"disk"`
	PriceMonthly float64  `json:"price_monthly"`
	PriceHourly  float64  `json:"price_hourly"`
	Regions      []string `json:"regions"`
	Available    bool     `json:"available"`
	Description  string   `json:"description"`
}

// Region is a data center region.
type Region struct {
	Slug      string   `json:"slug"`
	Name      string   `json:"name"`
	Available bool     `json:"available"`
	Sizes     []string `json:"sizes"`
}

// Sizes lists the droplet sizes.
func (c *Client) Sizes(ctx context.Context) ([]Size, error) {
	return listAll[Size](ctx, c, "/v2/sizes", nil, "sizes")
}

// Regions lists the regions.
func (c *Client) Regions(ctx context.Context) ([]Region, error) {
	return listAll[Region](ctx, c, "/v2/regions", nil, "regions")
}
