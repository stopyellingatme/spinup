package audit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	"github.com/stopyellingatme/spinup/internal/db/ops"
	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/gen/resource"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/ui"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

func init() {
	registry.Register(gen.Generator{
		Name: "audit", Short: "Attach audit triggers and add resource history", Usage: "<resource-or-table>",
		Example: `  # Add audit triggers and a history page to a resource
  spinup g audit ticket

  # Add the trail only, and hide a column
  spinup g audit users --api-only --audit-redact phone`,
		Flags: func(fs *pflag.FlagSet) {
			fs.Bool("api-only", false, "skip history web pages")
			fs.String("audit-redact", "", "additional columns to redact from the trail")
			fs.String("history-role", "", "comma-separated roles that can read history")
		}, Run: run,
	})
}

func run(_ context.Context, in gen.Input) (*gen.Output, error) {
	if len(in.Args) != 1 {
		return nil, fmt.Errorf("expected one resource or table\nTry: spinup g audit <resource-or-table>")
	}
	spec, isResource, err := resource.ExistingAuditSpec(in.Root, in.Args[0], in.Flags)
	if err != nil {
		return nil, err
	}
	table := spec.Name.Table()
	keys := "id"
	if table == "user_identities" {
		keys = "provider,subject"
	}
	migration, err := planMigration(spec, table, keys)
	if err != nil {
		return nil, err
	}
	var plan vfs.Plan
	if err := plan.Add(migration); err != nil {
		return nil, err
	}
	steps := []gen.PostStep{gen.PostSQLC, gen.PostGofmt}
	if isResource {
		history, post, err := resource.PlanHistory(spec, !resource.Bool(in.Flags, "api-only"))
		if err != nil {
			return nil, err
		}
		plan, err = plan.Merge(history)
		if err != nil {
			return nil, err
		}
		steps = append(steps, post...)
	}
	next := []string{"spinup db migrate up"}
	if isResource {
		next = append(next, "wrap Create, Update, and Delete writes in audit.InTx(ctx, s.DB, func(tx pgx.Tx) error { /* use s.Queries.WithTx(tx) */ return nil })")
		next = append(next, "add a History button that links to /"+spec.Name.PluralKebab()+"/{id}/history on the detail page")
	}
	return &gen.Output{Plan: plan, PostSteps: steps, Next: next}, nil
}

func planMigration(spec resource.Spec, table, keys string) (vfs.File, error) {
	dir := filepath.Join(spec.Root, ops.MigrationsDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return vfs.File{}, fmt.Errorf("read migrations: %w\nTry: check the directory permissions", err)
	}
	// Match only an Up statement. A Down DROP or a comment does not attach a trigger.
	trigger := regexp.MustCompile(`(?is)\bCREATE\s+(?:CONSTRAINT\s+)?TRIGGER\s+\S+\s+[^;]*?\bON\s+(?:"?public"?\.)?"?` + regexp.QuoteMeta(table) + `"?\s+[^;]*?\bEXECUTE\s+(?:FUNCTION|PROCEDURE)\s+(?:public\.)?audit_(?:row_change|truncate)\s*\(`)
	tableVersion := ""
	uncertain := false
	createTable := regexp.MustCompile(`(?is)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:"?public"?\.)?"?` + regexp.QuoteMeta(table) + `"?\s*\(`)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return vfs.File{}, fmt.Errorf("read migration: %w\nTry: check the file permissions", err)
		}
		if createTable.MatchString(migrationUp(string(data))) {
			tableVersion = strings.SplitN(entry.Name(), "_", 2)[0]
			break
		}
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return vfs.File{}, fmt.Errorf("read migration: %w\nTry: check the file permissions", err)
		}
		up := migrationUp(string(data))
		if trigger.MatchString(up) {
			version := strings.SplitN(entry.Name(), "_", 2)[0]
			// The audit pack attaches users and user_identities in a DO block only
			// when the table exists. That attach depends on the order in which the
			// migrations ran. An attach in any other migration is certain.
			conditional := strings.Contains(up, "to_regclass('public."+table+"')")
			if conditional && (olderVersion(version, tableVersion) || orderDependent(table)) {
				uncertain = true
				continue
			}

			return vfs.File{}, fmt.Errorf("cannot audit %s because a migration already attaches its audit trigger\nTry: review the existing audit migration", table)
		}
	}
	name, err := ops.NextMigrationName(dir, "audit_"+table)
	if err != nil {
		var ue *ui.Error
		if errors.As(err, &ue) {
			return vfs.File{}, fmt.Errorf("%w\nTry: %s", err, ue.Try)
		}
		return vfs.File{}, err
	}
	if err := resource.CheckAuditMigration(spec.Root, name); err != nil {
		return vfs.File{}, err
	}
	owner := ""
	if spec.Owned {
		owner = "user_id"
	}
	if table == "users" || table == "user_identities" {
		owner = ""
	}
	up, down := resource.AuditTriggerSQL(table, owner, keys, spec.Audit.Redact)
	if uncertain {
		// The conditional attach can have run already. Replace its triggers.
		up = down + up
	}
	sql := "-- +goose Up\n" + up + "\n-- +goose Down\n" + down
	return vfs.File{Path: ops.MigrationsDir + "/" + name, Data: []byte(sql), Class: string(manifest.Seed)}, nil
}

// orderDependent reports whether a pack migration with a lower version than the
// audit migration can create the table after the audit migration ran. Auth 00004
// creates user_identities when OIDC comes later. A copy of 00004 from a spinup
// version before ADR 0025 decision 3.9 has no attach block of its own.
func orderDependent(table string) bool {
	return table == "user_identities"
}

func olderVersion(schema, next string) bool {
	a, err := strconv.ParseUint(schema, 10, 64)
	if err != nil || a == 0 {
		return false
	}
	b, err := strconv.ParseUint(next, 10, 64)
	return err == nil && a < b
}

func migrationUp(sql string) string {
	down := regexp.MustCompile(`(?m)^--\s*\+goose\s+Down\b`)
	if loc := down.FindStringIndex(sql); loc != nil {
		sql = sql[:loc[0]]
	}
	sql = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(sql, "")
	lines := strings.Split(sql, "\n")
	for i, line := range lines {
		if j := strings.Index(line, "--"); j >= 0 && !strings.Contains(line[:j], "'") {
			lines[i] = line[:j]
		}
	}
	return strings.Join(lines, "\n")
}
