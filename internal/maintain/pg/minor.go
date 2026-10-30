package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (u *upgrade) minor(ctx context.Context) error {
	image, err := pinnedImage(u.current.major)
	if err != nil {
		return err
	}
	pinned, err := parseVersion(image.Tag)
	if err != nil {
		return err
	}
	if pinned == u.current {
		fmt.Fprintf(u.o.Out, "PostgreSQL %d.%d already matches the pinned image.\n", pinned.major, pinned.minor)
		return nil
	}
	if u.current.minor > pinned.minor {
		return failure("upgrade PostgreSQL minor", errors.New("the running version is newer than the pin. Refusing a downgrade"))
	}
	fmt.Fprintln(u.o.Out, "Take a backup with the configured backend")
	if _, err = u.backup(ctx, ""); err != nil {
		return err
	}
	if err = u.saveRecovery(ctx, ""); err != nil {
		return err
	}
	if err = u.updateCompose(ctx, image, ""); err != nil {
		return err
	}
	fmt.Fprintln(u.o.Out, "Build postgres from the release Dockerfile.postgres")
	if _, err = u.run(ctx, u.base("build", "postgres")...); err != nil {
		return err
	}
	fmt.Fprintln(u.o.Out, "Recreate only postgres (restart downtime)")
	if _, err = u.run(ctx, u.base("up", "-d", "--no-deps", "--no-build", "--force-recreate", "postgres")...); err != nil {
		return err
	}
	fmt.Fprintln(u.o.Out, "Wait for PostgreSQL health")
	if _, err = u.run(ctx, u.base("up", "-d", "--no-deps", "--no-build", "--wait", "--wait-timeout", "120", "postgres")...); err != nil {
		return err
	}
	data, err := u.run(ctx, u.base("ps", "-q", "postgres")...)
	if err != nil {
		return err
	}
	ids := strings.Fields(string(data))
	if len(ids) != 1 {
		return failure("check new version", errors.New("expected one postgres container"))
	}
	data, err = u.sql(ctx, ids[0], "SHOW server_version;\n")
	if err != nil {
		return err
	}
	got, err := parseVersion(strings.TrimSpace(string(data)))
	if err != nil {
		return err
	}
	if got != pinned {
		return failure("check new version", fmt.Errorf("running %d.%d, expected %d.%d", got.major, got.minor, pinned.major, pinned.minor))
	}
	fmt.Fprintf(u.o.Out, "PostgreSQL now runs %d.%d. Downtime was the restart only.\n", got.major, got.minor)
	return nil
}
