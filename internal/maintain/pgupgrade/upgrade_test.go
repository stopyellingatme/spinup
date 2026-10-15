package pgupgrade

import (
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/maintain/pg"
)

func TestLayout(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, mount, data, volume, want string }{
		{"standard", "/var/lib/postgresql/data", "/var/lib/postgresql/data/pgdata", "demo_postgres_data", "pgdata"},
		{"custom", "/var/lib/postgresql/data", "/var/lib/postgresql/data/16/docker", "demo_pgdata_16", "16/docker"},
		{"outside", "/var/lib/postgresql/data", "/other/data", "demo_postgres_data", ""},
		{"traversal", "/var/lib/postgresql/data", "/var/lib/postgresql/data/../old", "demo_postgres_data", ""},
		{"root", "/var/lib/postgresql/data", "/var/lib/postgresql/data", "demo_postgres_data", ""},
		{"missing", "/var/lib/postgresql/data", "", "demo_postgres_data", ""},
		{"bind", "/var/lib/postgresql/data", "/var/lib/postgresql/data/pgdata", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := oldPath(pg.Details{Mount: tt.mount, Data: tt.data, Volume: tt.volume})
			if tt.want == "" {
				if err == nil || !strings.Contains(err.Error(), "Try:") {
					t.Fatalf("got %q, %v", got, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}
