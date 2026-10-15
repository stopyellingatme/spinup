package testkit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// PostgresImage is the image for the fallback test server.
// The main loop keeps it equal to the postgres entry in
// internal/stackversions/stackversions.yaml.
const PostgresImage = "postgres:18.6-trixie@sha256:74935e72241653ca55e0414067e6d8763aceb8a810eb51b452253ec3dcfc4336"

// ImageEnv names the environment variable that overrides [PostgresImage].
const ImageEnv = "SPINUP_TEST_PG_IMAGE"

const containerStartTimeout = 3 * time.Minute

// server is the PostgreSQL server for this test process.
type server struct {
	url   string        // admin URL: a role that can create databases
	admin *pgxpool.Pool // pool on the admin URL
	skip  string        // non-empty when no server is available
	err   error         // non-nil when the server failed to start
}

var (
	serverOnce sync.Once
	theServer  server
)

// getServer returns the server for this process. It skips the test when no
// server is available, or fails it when CI is set.
func getServer(t testing.TB) *server {
	t.Helper()
	serverOnce.Do(func() { theServer = startServer() })
	s := &theServer
	switch {
	case s.err != nil:
		t.Fatalf("testkit: %v", s.err)
	case s.skip != "" && os.Getenv("CI") != "":
		t.Fatalf("testkit: %s (CI is set, so this is a failure)", s.skip)
	case s.skip != "":
		t.Skip("testkit: " + s.skip)
	}
	return s
}

func startServer() server {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		var skip string
		var err error
		url, skip, err = startContainer()
		if skip != "" || err != nil {
			return server{skip: skip, err: err}
		}
	}
	admin, err := pgxpool.New(context.Background(), url)
	if err != nil {
		// The parser error can hold the password, so it is not shown.
		return server{err: errors.New("open the admin pool: DATABASE_URL is not valid (it is not shown because it can hold a password)\n" +
			"Try: use the form postgres://user:password@host:5432/dbname?sslmode=disable")}
	}
	return server{url: url, admin: admin}
}

// startContainer starts one PostgreSQL container. Ryuk, the testcontainers
// reaper, removes it when the test process exits.
func startContainer() (url, skip string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), containerStartTimeout)
	defer cancel()
	if why := dockerUnavailable(ctx); why != "" {
		return "", "no PostgreSQL server: DATABASE_URL is empty and Docker is not available (" + why + ")\n" +
			"Try: export DATABASE_URL=postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable, or start Docker", nil
	}
	image := os.Getenv(ImageEnv)
	if image == "" {
		image = PostgresImage
	}
	ctr, err := postgres.Run(ctx, image, postgres.BasicWaitStrategies())
	if err != nil {
		return "", "", fmt.Errorf("start the PostgreSQL container %s: %w\nTry: check Docker, or set DATABASE_URL", image, err)
	}
	url, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return "", "", fmt.Errorf("read the container connection string: %w", err)
	}
	return url, "", nil
}

// dockerUnavailable returns why Docker cannot run containers, or "" when it can.
func dockerUnavailable(ctx context.Context) (why string) {
	defer func() {
		if r := recover(); r != nil {
			why = fmt.Sprint(r)
		}
	}()
	provider, err := testcontainers.ProviderDocker.GetProvider()
	if err != nil {
		return err.Error()
	}
	defer func() { _ = provider.Close() }()
	if err := provider.Health(ctx); err != nil {
		return err.Error()
	}
	return ""
}
