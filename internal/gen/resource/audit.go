package resource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	"github.com/stopyellingatme/spinup/internal/gen/fields"
	"github.com/stopyellingatme/spinup/internal/gen/newapp"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
)

// AuditSpec describes the trail and history guard of a resource.
type AuditSpec struct {
	// On enables the database triggers and history files.
	On bool
	// Redact lists the columns whose values the trigger hides.
	Redact []string
	// HistoryRoles limits history to these declared roles.
	HistoryRoles []string
}

var sensitiveColumn = regexp.MustCompile(`(?i)(password|passwd|secret|token|api_?key|hash|credential)`)

func flagString(flags *pflag.FlagSet, name string) string {
	if flags == nil {
		return ""
	}
	s, _ := flags.GetString(name)
	return s
}

func commaList(value string) []string {
	var out []string
	for _, v := range strings.Split(value, ",") {
		v = strings.TrimSpace(v)
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func configureAudit(spec *Spec, flags *pflag.FlagSet, config *project.Config, columns []string) error {
	lock, err := manifest.Load(filepath.Join(spec.Root, "spinup.lock"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read spinup.lock: %w\nTry: restore a valid spinup.lock", err)
	}
	module := lock != nil && slices.Contains(lock.Packs, "audit")
	if Bool(flags, "audit") {
		if err := checkEmbedAudit(lock, !Bool(flags, "api-only")); err != nil {
			return err
		}
	}
	if Bool(flags, "audit") && !module {
		return fmt.Errorf("cannot audit the resource because the audit module is missing\nTry: spinup module add audit")
	}
	if Bool(flags, "audit") && Bool(flags, "no-audit") {
		return fmt.Errorf("cannot combine --audit and --no-audit\nTry: choose one audit flag")
	}
	spec.Audit.On = module && !Bool(flags, "no-audit")
	if !spec.Audit.On {
		if flagString(flags, "audit-redact") != "" || flagString(flags, "history-role") != "" {
			return fmt.Errorf("cannot configure audit because audit is disabled\nTry: enable the audit module and omit --no-audit")
		}
		return nil
	}
	spec.Audit.HistoryRoles = slices.Clone(spec.Roles)
	if flags != nil && flags.Changed("history-role") {
		spec.Audit.HistoryRoles = commaList(flagString(flags, "history-role"))
		if len(spec.Audit.HistoryRoles) == 0 {
			return fmt.Errorf("cannot use an empty --history-role list\nTry: pass --history-role manager")
		}
	}
	for _, role := range spec.Audit.HistoryRoles {
		if !slices.Contains(config.AuthRoles().Names, role) {
			return fmt.Errorf("cannot use history role %q because it is not declared\nTry: declare the role in [auth] and run spinup g roles", role)
		}
	}
	if len(spec.Audit.HistoryRoles) > 0 {
		if err := rolesFileMatches(spec.Root, config); err != nil {
			return err
		}
	}
	for _, col := range columns {
		if sensitiveColumn.MatchString(col) {
			spec.Audit.Redact = append(spec.Audit.Redact, col)
		}
	}
	for _, col := range commaList(flagString(flags, "audit-redact")) {
		if !slices.Contains(columns, col) {
			return fmt.Errorf("cannot redact %q because the table has no such column\nTry: name a column of the table", col)
		}
		if !slices.Contains(spec.Audit.Redact, col) {
			spec.Audit.Redact = append(spec.Audit.Redact, col)
		}
	}
	sort.Strings(spec.Audit.Redact)
	return nil
}

// errEmbedHistory is the refusal of history pages in embed mode (ADR 0039, section 10.4).
var errEmbedHistory = errors.New("cannot generate history pages in embed mode: the history page requires a SvelteKit server\nTry: use an app with --frontend-mode node")

// checkEmbedAudit refuses an explicit audit request in embed mode (ADR 0039, section 10.4).
// Without the audit pack it returns the module refusal of embed mode. A legacy or hand-edited
// lock can hold audit already: then only history pages are refused, and the trail alone passes.
func checkEmbedAudit(lock *manifest.Lock, pages bool) error {
	if lock == nil || lock.Answers["frontend_mode"] != "embed" {
		return nil
	}
	if !slices.Contains(lock.Packs, "audit") {
		return newapp.EmbedModuleError("audit")
	}
	if pages {
		return errEmbedHistory
	}
	return nil
}

func auditColumns(spec Spec) []string {
	cols := []string{"id", "created_at", "updated_at"}
	if spec.Owned {
		cols = append(cols, "user_id")
	}
	for _, f := range spec.Fields {
		cols = append(cols, f.Column())
	}
	return cols
}

// CheckAuditMigration requires the audit schema to precede the new migration.
func CheckAuditMigration(root, migration string) error {
	def, ok, err := findTable(root, "audit_events")
	if err != nil {
		return err
	}
	version := func(name string) uint64 { v, _ := strconv.ParseUint(strings.SplitN(name, "_", 2)[0], 10, 64); return v }
	if !ok || migration == "" || version(def.File) == 0 || (!def.Kit && version(def.File) >= version(migration)) {
		return fmt.Errorf("cannot attach audit triggers because the audit_events migration is missing or is not older than %s\nTry: spinup module add audit, then run the generator again", migration)
	}
	return nil
}

// AuditTriggerSQL returns the Up and Down statements for a validated table.
func AuditTriggerSQL(table, owner, keys string, redact []string) (string, string) {
	args := append([]string{owner, keys}, redact...)
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", "''") + "'"
	}
	up := fmt.Sprintf("CREATE TRIGGER %s_audit AFTER INSERT OR UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION audit_row_change(%s);\nCREATE TRIGGER %s_audit_truncate AFTER TRUNCATE ON %s FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();\n", table, table, strings.Join(quoted, ", "), table, table)
	down := fmt.Sprintf("DROP TRIGGER IF EXISTS %s_audit_truncate ON %s;\nDROP TRIGGER IF EXISTS %s_audit ON %s;\n", table, table, table, table)
	return up, down
}

func resourceAuditTriggers(spec Spec) (string, string) {
	owner := ""
	if spec.Owned {
		owner = "user_id"
	}
	return AuditTriggerSQL(spec.Name.Table(), owner, "id", spec.Audit.Redact)
}

func columnsOf(body string) []string {
	var out []string
	for _, def := range splitTopLevel(body) {
		words := strings.Fields(def)
		if len(words) < 2 {
			continue
		}
		col := strings.Trim(words[0], `"`)
		switch strings.ToLower(col) {
		case "constraint", "primary", "unique", "check", "foreign", "exclude", "like":
			continue
		}
		out = append(out, col)
	}
	return out
}

// ExistingAuditSpec reads a table and the access guard of its generated resource.
// A table without a generated handler has no history operation.
func ExistingAuditSpec(root, target string, flags *pflag.FlagSet) (Spec, bool, error) {
	name, def, ok, err := existingTable(root, target)
	if err != nil {
		return Spec{}, false, err
	}
	if !ok {
		return Spec{}, false, fmt.Errorf("cannot audit %q because no migration creates the table\nTry: name an existing resource or table", target)
	}
	table := name.Table()
	spec := Spec{Root: root, Name: name}
	switch table {
	case "sessions", "login_attempts", "password_resets", "audit_events":
		return Spec{}, false, fmt.Errorf("cannot audit %s because it holds authentication secrets or audit events\nTry: audit users or an application resource", table)
	}
	columns := columnsOf(def.Body)
	keys := []string{"id"}
	if table == "user_identities" {
		keys = []string{"provider", "subject"}
	}
	for _, key := range keys {
		if !slices.Contains(columns, key) {
			return Spec{}, false, fmt.Errorf("cannot audit %s because key column %s is missing\nTry: attach triggers with the table key in a handwritten migration", table, key)
		}
	}
	for _, col := range columns {
		if sensitiveColumn.MatchString(col) && !regexp.MustCompile(`^[a-z][a-z0-9_]*$`).MatchString(col) {
			return Spec{}, false, fmt.Errorf("cannot audit the quoted column %q\nTry: attach its triggers in a handwritten migration", col)
		}
	}

	spec.Owned, ok = tableOwner(def)
	if !ok {
		return Spec{}, false, fmt.Errorf("cannot audit %s because user_id is not an integer owner column\nTry: attach its triggers in a handwritten migration", table)
	}

	spec.Shared = !spec.Owned
	config, err := loadResourceConfig(root)
	if err != nil {
		return Spec{}, false, err
	}
	source, err := os.ReadFile(filepath.Join(root, handlersPath(name)))
	isResource := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Spec{}, false, fmt.Errorf("read resource handler: %w\nTry: check the file permissions", err)
	}
	if isResource {
		spec.Roles, err = getHistoryRoles(source, spec, config)
		if err != nil {
			return Spec{}, false, err
		}
	}
	// spinup g audit is an explicit audit request. Embed mode refuses it before the advice to add audit.
	lock, err := manifest.Load(filepath.Join(root, "spinup.lock"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Spec{}, false, fmt.Errorf("read spinup.lock: %w\nTry: restore a valid spinup.lock", err)
	}
	if err := checkEmbedAudit(lock, isResource && !Bool(flags, "api-only")); err != nil {
		return Spec{}, false, err
	}
	if err := configureAudit(&spec, flags, config, columns); err != nil {
		return Spec{}, false, err
	}
	if !spec.Audit.On {
		return Spec{}, false, fmt.Errorf("cannot audit the table because the audit module is missing\nTry: spinup module add audit")
	}
	return spec, isResource, nil
}

// existingTable shares the resource lookup, including names of auth tables.
func existingTable(root, target string) (fields.Name, tableDef, bool, error) {
	name, err := fields.ParseName(target)
	if err != nil {
		return fields.Name{}, tableDef{}, false, err
	}
	def, ok, err := findTable(root, name.Table())
	if err != nil {
		return name, def, false, err
	}
	if !ok {
		def, ok, err = findTable(root, target)
		if ok {
			name.Plural = target
		}
	}
	return name, def, ok, err
}

// tableOwner detects and validates the integer owner column.
func tableOwner(def tableDef) (owned, valid bool) {
	owned = slices.Contains(columnsOf(def.Body), "user_id")
	if !owned {
		return false, true
	}
	for _, column := range splitTopLevel(def.Body) {
		words := strings.Fields(column)
		if len(words) >= 2 && strings.Trim(words[0], `"`) == "user_id" {
			return true, slices.Contains([]string{"bigint", "int8", "integer", "int", "int4", "smallint", "int2"}, strings.ToLower(words[1]))
		}
	}
	return true, false
}
