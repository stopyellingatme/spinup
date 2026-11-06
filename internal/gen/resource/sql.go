package resource

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/stopyellingatme/spinup/internal/db/ops"
	"github.com/stopyellingatme/spinup/internal/gen/fields"
)

// sqlType returns the column type and constraints of f, without the name.
// A ref column of an owner reference gets no inline foreign key, because a
// composite key on the table checks it.
func sqlType(f fields.Field, ownerChecked bool) string {
	var b strings.Builder
	if f.Type.Ref != "" {
		b.WriteString("bigint")
	} else {
		b.WriteString(f.Type.SQL)
	}
	if !f.Nullable {
		b.WriteString(" NOT NULL")
	}
	if f.Type.Ref != "" && !ownerChecked {
		parent, _ := fields.ParseName(f.Type.Ref)
		fmt.Fprintf(&b, " REFERENCES %s (id)", parent.Table())
	}
	if len(f.Type.Enum) > 0 {
		fmt.Fprintf(&b, " CHECK (%s IN (%s))", f.Column(), sqlList(f.Type.Enum))
	}
	return b.String()
}

func sqlList(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = "'" + v + "'"
	}
	return strings.Join(quoted, ", ")
}

// migrationSQL renders the goose migration that creates the table of spec.
// An owned table gets a unique (user_id, id) index, so that the owner
// references of later resources can use it.
func migrationSQL(spec Spec) []byte {
	table := spec.Name.Table()
	var b strings.Builder
	b.WriteString("-- +goose Up\n")
	keyed := map[string]bool{}
	for _, r := range spec.ownerRefs {
		if !r.AddKey || keyed[r.Table] {
			continue
		}
		keyed[r.Table] = true
		// The override must be the line just before the statement.
		fmt.Fprintf(&b, "-- spinup:allow-destructive the owner check of %s.%s needs a unique (user_id, id) key on %s\n", table, r.Column, r.Table)
		fmt.Fprintf(&b, "CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (user_id, id);\n", ownerKeyName(r.Table), r.Table)
	}
	lines := []string{"id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY"}
	if spec.Owned {
		lines = append(lines, "user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE")
	}
	for _, f := range spec.Fields {
		_, checked := spec.ownerRefFor(f.Column())
		lines = append(lines, f.Column()+" "+sqlType(f, checked))
	}
	lines = append(lines, "created_at timestamptz NOT NULL DEFAULT now()", "updated_at timestamptz NOT NULL DEFAULT now()")
	for _, r := range spec.ownerRefs {
		if !r.Self {
			lines = append(lines, fmt.Sprintf("CONSTRAINT %s FOREIGN KEY (%s, user_id) REFERENCES %s (id, user_id)", r.constraint(table), r.Column, r.Table))
		}
	}
	fmt.Fprintf(&b, "CREATE TABLE %s (\n    %s\n);\n", table, strings.Join(lines, ",\n    "))
	if spec.Owned {
		fmt.Fprintf(&b, "CREATE UNIQUE INDEX %s_user_id_id_idx ON %s (user_id, id);\n", table, table)
	}
	for _, f := range spec.Fields {
		if f.Type.Ref != "" {
			fmt.Fprintf(&b, "CREATE INDEX %s_%s_idx ON %s (%s);\n", table, f.Column(), table, f.Column())
		}
	}
	for _, r := range spec.ownerRefs {
		// A reference to the table itself needs the unique index above.
		if r.Self {
			fmt.Fprintf(&b, "ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s, user_id) REFERENCES %s (id, user_id);\n", table, r.constraint(table), r.Column, table)
		}
	}
	if spec.Audit.On {
		up, _ := resourceAuditTriggers(spec)
		b.WriteString(up)
	}
	b.WriteString("\n-- +goose Down\n")
	if spec.Audit.On {
		_, down := resourceAuditTriggers(spec)
		b.WriteString(down)
	}
	// An owner key added to a parent table stays: other tables can use it.
	fmt.Fprintf(&b, "DROP TABLE %s;\n", table)
	return []byte(b.String())
}

// queriesSQL renders the sqlc queries of spec. The list query also returns
// the total count of the filtered rows. An uncorrelated subquery computes it
// once, from the index, and the page itself still stops after the limit.
// The count query serves an empty page, which has no row to carry the total.
func queriesSQL(spec Spec) []byte {
	table := spec.Name.Table()
	columns := []string{"id"}
	if spec.Owned {
		columns = append(columns, "user_id")
	}
	var writable, placeholders, sets []string
	for i, f := range spec.Fields {
		columns = append(columns, f.Column())
		writable = append(writable, f.Column())
		placeholders = append(placeholders, fmt.Sprintf("$%d", i+1))
		sets = append(sets, fmt.Sprintf("%s = $%d", f.Column(), i+2))
	}
	if spec.Owned {
		writable = append(writable, "user_id")
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(spec.Fields)+1))
	}
	columns = append(columns, "created_at", "updated_at")
	all := strings.Join(columns, ", ")
	singular, plural := spec.Name.Pascal(), spec.Name.PluralPascal()

	listFilter, totalFilter, countFilter, idFilter, updateFilter := "", "", "", "", ""
	if spec.Owned {
		listFilter = "WHERE user_id = sqlc.arg(user_id)\n"
		totalFilter = " WHERE counted.user_id = sqlc.arg(user_id)"
		countFilter = " WHERE user_id = $1"
		idFilter = " AND user_id = $2"
		updateFilter = fmt.Sprintf(" AND user_id = $%d", len(spec.Fields)+2)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "-- Queries for the %s table. sqlc turns them into Go code in api/internal/store.\n", table)
	b.WriteString("-- Run `spinup db sqlc` after you change this file or a migration.\n\n")
	fmt.Fprintf(&b, "-- name: List%s :many\n", plural)
	fmt.Fprintf(&b, "SELECT sqlc.embed(%s), (SELECT count(*) FROM %s AS counted%s)::bigint AS total\nFROM %s\n%sORDER BY id\nLIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);\n\n", table, table, totalFilter, table, listFilter)
	fmt.Fprintf(&b, "-- name: Count%s :one\n", plural)
	fmt.Fprintf(&b, "SELECT count(*) FROM %s%s;\n\n", table, countFilter)
	fmt.Fprintf(&b, "-- name: Get%s :one\n", singular)
	fmt.Fprintf(&b, "SELECT %s\nFROM %s\nWHERE id = $1%s;\n\n", all, table, idFilter)
	fmt.Fprintf(&b, "-- name: Create%s :one\n", singular)
	fmt.Fprintf(&b, "INSERT INTO %s (%s)\nVALUES (%s)\nRETURNING %s;\n\n", table, strings.Join(writable, ", "), strings.Join(placeholders, ", "), all)
	fmt.Fprintf(&b, "-- name: Update%s :one\n", singular)
	fmt.Fprintf(&b, "UPDATE %s\nSET %s, updated_at = now()\nWHERE id = $1%s\nRETURNING %s;\n\n", table, strings.Join(sets, ", "), updateFilter, all)
	fmt.Fprintf(&b, "-- name: Delete%s :execrows\n", singular)
	fmt.Fprintf(&b, "DELETE FROM %s\nWHERE id = $1%s;\n", table, idFilter)
	return []byte(b.String())
}

// migrationsDir returns the absolute migrations directory of root.
func migrationsDir(root string) string {
	return filepath.Join(root, filepath.FromSlash(ops.MigrationsDir))
}

// tableDef is the CREATE TABLE statement of a table in a migration file.
type tableDef struct {
	// File is the migration file name.
	File string
	// Body is the text between the parentheses of the statement.
	Body string
}

// findTable returns the migration that creates table, or false when no
// migration in root creates it. Only the Up section of each file counts.
func findTable(root, table string) (tableDef, bool, error) {
	dir := migrationsDir(root)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return tableDef{}, false, nil
	}
	if err != nil {
		return tableDef{}, false, fmt.Errorf("read %s: %w\nTry: check the directory permissions", ops.MigrationsDir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	create := regexp.MustCompile(`(?is)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:"?public"?\.)?"?` + regexp.QuoteMeta(table) + `"?\s*\(`)
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return tableDef{}, false, fmt.Errorf("read %s/%s: %w\nTry: check the file permissions", ops.MigrationsDir, name, err)
		}
		up := upSection(string(data))
		loc := create.FindStringIndex(up)
		if loc == nil {
			continue
		}
		return tableDef{File: name, Body: parenBody(up[loc[1]:])}, true, nil
	}
	return tableDef{}, false, nil
}

var gooseDown = regexp.MustCompile(`(?m)^--\s*\+goose\s+Down\b`)

// upSection returns the text before the goose Down annotation, without
// line comments.
func upSection(sql string) string {
	if loc := gooseDown.FindStringIndex(sql); loc != nil {
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

// parenBody returns the text up to the parenthesis that closes an open one.
func parenBody(s string) string {
	depth, quoted := 1, false
	for i, r := range s {
		switch {
		case r == '\'':
			quoted = !quoted
		case quoted:
		case r == '(':
			depth++
		case r == ')':
			depth--
			if depth == 0 {
				return s[:i]
			}
		}
	}
	return s
}

// splitTopLevel splits a column list at the commas outside parentheses and
// quotes.
func splitTopLevel(s string) []string {
	var parts []string
	depth, quoted, start := 0, false, 0
	for i, r := range s {
		switch {
		case r == '\'':
			quoted = !quoted
		case quoted:
		case r == '(':
			depth++
		case r == ')':
			depth--
		case r == ',' && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

var (
	referencesRE = regexp.MustCompile(`(?i)\bREFERENCES\s+(?:"?public"?\.)?"?(\w+)"?`)
	checkInRE    = regexp.MustCompile(`(?i)\bCHECK\s*\(\s*"?\w+"?\s+IN\s*\(\s*'([^']*)'`)
	// foreignKeyRE matches a table constraint FOREIGN KEY (a, ...) REFERENCES t.
	// The first column is the reference, such as project_id of an owner key.
	foreignKeyRE = regexp.MustCompile(`(?i)\bFOREIGN\s+KEY\s*\(\s*"?(\w+)"?[^)]*\)\s*REFERENCES\s+(?:"?public"?\.)?"?(\w+)"?`)
)

// requiredColumn is a column that an INSERT must set: NOT NULL, without a
// default, and not an identity.
type requiredColumn struct {
	Name string
	// Value is an SQL literal that suits the column type, or "" for a type
	// that has no sample value.
	Value string
	// Ref is the table that the column references, if any.
	Ref string
}

// requiredColumns returns the columns of a CREATE TABLE body that an INSERT
// must set.
func requiredColumns(body string) []requiredColumn {
	defs := splitTopLevel(body)
	tableRefs := map[string]string{}
	for _, def := range defs {
		if m := foreignKeyRE.FindStringSubmatch(def); m != nil {
			if _, ok := tableRefs[m[1]]; !ok {
				tableRefs[m[1]] = m[2]
			}
		}
	}
	var cols []requiredColumn
	for _, def := range defs {
		words := strings.Fields(def)
		if len(words) < 2 {
			continue
		}
		switch strings.ToLower(words[0]) {
		case "constraint", "primary", "unique", "check", "foreign", "exclude", "like":
			continue
		}
		upper := strings.ToUpper(def)
		if !strings.Contains(upper, "NOT NULL") && !strings.Contains(upper, "PRIMARY KEY") {
			continue
		}
		if strings.Contains(upper, "DEFAULT") || strings.Contains(upper, "GENERATED") || strings.Contains(upper, "SERIAL") {
			continue
		}
		col := requiredColumn{Name: strings.Trim(words[0], `"`)}
		if m := referencesRE.FindStringSubmatch(def); m != nil {
			col.Ref = m[1]
		} else if ref, ok := tableRefs[col.Name]; ok {
			col.Ref = ref
		} else if m := checkInRE.FindStringSubmatch(def); m != nil {
			col.Value = "'" + m[1] + "'"
		} else {
			col.Value = sampleSQL(strings.ToLower(words[1]))
		}
		cols = append(cols, col)
	}
	return cols
}

// sampleSQL returns a sample literal for a column type, or "".
func sampleSQL(typ string) string {
	typ = strings.TrimRight(strings.SplitN(typ, "(", 2)[0], "[]")
	switch typ {
	case "text", "varchar", "character", "char", "citext", "name":
		return "'sample'"
	case "integer", "int", "int2", "int4", "int8", "smallint", "bigint", "double", "real", "float4", "float8", "numeric", "decimal":
		return "1"
	case "boolean", "bool":
		return "true"
	case "timestamptz", "timestamp":
		return "'2026-01-02T03:04:05Z'"
	case "date":
		return "'2026-01-02'"
	case "uuid":
		return "'00000000-0000-4000-8000-000000000001'"
	case "jsonb", "json":
		return "'{}'"
	}
	return ""
}

// parentInsert is one INSERT statement that a test runs before it creates a
// row with references.
type parentInsert struct {
	Table string
	SQL   string
	// Owned reports that SQL takes the owner user ID as $1.
	Owned bool
}

// parentInserts returns the INSERT statements that create one row in each
// table that the ref fields of spec need, dependencies first. A table whose
// migration it cannot read gets an INSERT with default values. The owner
// column of an owned table takes $1, so that the test can give all rows of
// one chain to the same user, as the owner keys require.
func parentInserts(root string, spec Spec) ([]parentInsert, error) {
	var out []parentInsert
	done := map[string]bool{spec.Name.Table(): true}
	var visit func(table string, depth int) error
	visit = func(table string, depth int) error {
		if done[table] {
			return nil
		}
		done[table] = true
		def, ok, err := findTable(root, table)
		if err != nil {
			return err
		}
		var names, values []string
		owned := false
		if ok {
			for _, col := range requiredColumns(def.Body) {
				value := col.Value
				switch {
				case col.Name == "user_id" && col.Ref == "users":
					owned = true
					value = "$1"
				case col.Ref != "":
					if depth < 5 {
						if err := visit(col.Ref, depth+1); err != nil {
							return err
						}
					}
					value = fmt.Sprintf("(SELECT max(id) FROM %s)", col.Ref)
				}
				if value == "" {
					continue
				}
				names = append(names, col.Name)
				values = append(values, value)
			}
		}
		stmt := fmt.Sprintf("INSERT INTO %s DEFAULT VALUES", table)
		if len(names) > 0 {
			stmt = fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(names, ", "), strings.Join(values, ", "))
		}
		out = append(out, parentInsert{Table: table, SQL: stmt, Owned: owned})
		return nil
	}
	for _, f := range spec.Fields {
		if f.Type.Ref == "" {
			continue
		}
		parent, _ := fields.ParseName(f.Type.Ref)
		if err := visit(parent.Table(), 0); err != nil {
			return nil, err
		}
	}
	return out, nil
}
