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

// ownerRef is a ref field of an owned resource whose target table is also
// owned. The migration gives the field a composite foreign key on
// (column, user_id). PostgreSQL then checks in the write statement itself
// that the referenced row exists and belongs to the same user. A row of
// another user and a missing row violate the same constraint, so the API
// gives the same answer for both.
type ownerRef struct {
	// Column is the ref column, such as project_id.
	Column string
	// Table is the referenced table.
	Table string
	// Self reports a reference to the table of the resource itself.
	Self bool
	// AddKey reports that no migration gives Table a unique (user_id, id)
	// key, so the new migration must add one.
	AddKey bool
}

// constraint returns the name of the composite foreign key in table.
func (r ownerRef) constraint(table string) string {
	return pgName(table + "_" + r.Column + "_fkey")
}

// ownerKeyName returns the name of the unique (user_id, id) index that a
// child migration adds to an owned table that lacks one.
func ownerKeyName(table string) string { return pgName(table + "_user_id_id_key") }

// pgName shortens an identifier to the 63 bytes that PostgreSQL keeps.
func pgName(name string) string {
	if len(name) > 63 {
		return name[:63]
	}
	return name
}

// planOwnerRefs returns the ref fields of spec that must refer to rows of
// the same user: references from an owned resource to itself or to another
// owned table. A table is owned when its migration has the column
// user_id bigint NOT NULL REFERENCES users. A reference to any other table
// keeps the plain foreign key.
func planOwnerRefs(spec Spec) ([]ownerRef, error) {
	if !spec.Owned {
		return nil, nil
	}
	var refs []ownerRef
	for _, f := range spec.Fields {
		if f.Type.Ref == "" {
			continue
		}
		if f.Type.Ref == spec.Name.Snake() {
			refs = append(refs, ownerRef{Column: f.Column(), Table: spec.Name.Table(), Self: true})
			continue
		}
		parent, _ := fields.ParseName(f.Type.Ref)
		def, ok, err := findTable(spec.Root, parent.Table())
		if err != nil {
			return nil, err
		}
		if !ok || !ownedBody(def.Body) {
			continue
		}
		key, err := hasOwnerKey(spec.Root, parent.Table(), def.Body)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ownerRef{Column: f.Column(), Table: parent.Table(), AddKey: !key})
	}
	return refs, nil
}

// ownedBody reports whether a CREATE TABLE body has the owner column of an
// owned resource.
func ownedBody(body string) bool {
	for _, col := range requiredColumns(body) {
		if col.Name == "user_id" && col.Ref == "users" {
			return true
		}
	}
	return false
}

// uniqueOwnerColumns matches the column list of a unique key on user_id and
// id, in either order.
const uniqueOwnerColumns = `\(\s*(?:"?user_id"?\s*,\s*"?id"?|"?id"?\s*,\s*"?user_id"?)\s*\)`

var inlineOwnerKey = regexp.MustCompile(`(?i)\bUNIQUE\s*` + uniqueOwnerColumns)

// hasOwnerKey reports whether table has a unique key on (user_id, id): a
// UNIQUE constraint in its CREATE TABLE body, or a unique index in the Up
// section of any migration. A composite foreign key needs this key. A missed
// key only costs a second index, because the new migration adds it with
// IF NOT EXISTS.
func hasOwnerKey(root, table, body string) (bool, error) {
	if inlineOwnerKey.MatchString(body) {
		return true, nil
	}
	index := regexp.MustCompile(`(?is)\bCREATE\s+UNIQUE\s+INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?(?:"?\w+"?\s+)?ON\s+(?:ONLY\s+)?(?:"?public"?\.)?"?` + regexp.QuoteMeta(table) + `"?\s*(?:USING\s+btree\s*)?` + uniqueOwnerColumns)
	dir := migrationsDir(root)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w\nTry: check the directory permissions", ops.MigrationsDir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return false, fmt.Errorf("read %s/%s: %w\nTry: check the file permissions", ops.MigrationsDir, name, err)
		}
		if index.MatchString(upSection(string(data))) {
			return true, nil
		}
	}
	return false, nil
}

// ownerRefFor returns the owner reference of column, if any.
func (s Spec) ownerRefFor(column string) (ownerRef, bool) {
	for _, r := range s.ownerRefs {
		if r.Column == column {
			return r, true
		}
	}
	return ownerRef{}, false
}

// refMessage is the field error of a reference to a row that does not exist
// or that belongs to another user.
const refMessage = "must refer to an existing record"
