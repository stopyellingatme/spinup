package lint

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Finding describes one unsafe migration statement.
type Finding struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
	Try     string `json:"try"`
}

type statement struct {
	tokens []string
	line   int
}

// Lint checks the Up section of one goose migration.
func Lint(name string, sql []byte) ([]Finding, error) {
	return lint(name, sql, nil)
}

// LintDir checks SQL migrations in name order.
func LintDir(dir string) ([]Finding, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations directory %q: %w", dir, err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	created := map[string]bool{}
	var findings []Finding
	for _, name := range names {
		data, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil {
			return nil, fmt.Errorf("read migration %q: %w", name, readErr)
		}
		got, lintErr := lint(name, data, created)
		if lintErr != nil {
			return nil, lintErr
		}
		findings = append(findings, got...)
		for _, table := range createdTables(data) {
			created[table] = true
		}
	}
	return findings, nil
}

func lint(name string, sql []byte, earlier map[string]bool) ([]Finding, error) {
	up, noTransaction := upSection(sql)
	overrides, bad := overrideLines(name, up)
	statements := tokenize(up)
	createdHere := map[string]bool{}
	for _, stmt := range statements {
		if table, ok := createTable(stmt.tokens); ok {
			createdHere[table] = true
		}
	}
	findings := bad
	for _, stmt := range statements {
		rule, message, hint := ruleFor(stmt.tokens, earlier, createdHere, noTransaction)
		if rule == "" || overrides[stmt.line] {
			continue
		}
		findings = append(findings, Finding{File: name, Line: stmt.line, Rule: rule, Message: message, Try: hint})
	}
	sort.SliceStable(findings, func(i, j int) bool { return findings[i].Line < findings[j].Line })
	return findings, nil
}

func upSection(sql []byte) ([]byte, bool) {
	lines := bytes.Split(sql, []byte("\n"))
	inUp, noTransaction := false, false
	var out [][]byte
	for _, line := range lines {
		text := strings.TrimSpace(string(line))
		switch text {
		case "-- +goose Up":
			inUp = true
			out = append(out, nil)
			continue
		case "-- +goose Down":
			inUp = false
			out = append(out, nil)
			continue
		case "-- +goose NO TRANSACTION":
			noTransaction = true
		}
		if inUp {
			out = append(out, line)
		} else {
			out = append(out, nil)
		}
	}
	return bytes.Join(out, []byte("\n")), noTransaction
}

func overrideLines(name string, sql []byte) (map[int]bool, []Finding) {
	allowed := map[int]bool{}
	var findings []Finding
	for index, line := range bytes.Split(sql, []byte("\n")) {
		text := strings.TrimSpace(string(line))
		if !strings.HasPrefix(text, "-- spinup:allow-destructive") {
			continue
		}
		reason := strings.TrimSpace(strings.TrimPrefix(text, "-- spinup:allow-destructive"))
		lineNo := index + 1
		if reason == "" {
			findings = append(findings, Finding{File: name, Line: lineNo, Rule: "bad-override", Message: "destructive override needs a reason", Try: "add a reason after -- spinup:allow-destructive"})
			continue
		}
		allowed[lineNo+1] = true
	}
	return allowed, findings
}

func createdTables(sql []byte) []string {
	statements := tokenize(mustUp(sql))
	var tables []string
	for _, stmt := range statements {
		if table, ok := createTable(stmt.tokens); ok {
			tables = append(tables, table)
		}
	}
	return tables
}

func mustUp(sql []byte) []byte { up, _ := upSection(sql); return up }

func createTable(tokens []string) (string, bool) {
	if len(tokens) < 3 || tokens[0] != "CREATE" {
		return "", false
	}
	i := 1
	for i < len(tokens) && (tokens[i] == "UNLOGGED" || tokens[i] == "TEMP" || tokens[i] == "TEMPORARY" || tokens[i] == "GLOBAL" || tokens[i] == "LOCAL") {
		i++
	}
	if i >= len(tokens) || tokens[i] != "TABLE" {
		return "", false
	}
	i++
	if len(tokens) > i+2 && tokens[i] == "IF" && tokens[i+1] == "NOT" && tokens[i+2] == "EXISTS" {
		i += 3
	}
	if i >= len(tokens) {
		return "", false
	}
	return identifier(tokens[i]), true
}

func ruleFor(tokens []string, earlier, createdHere map[string]bool, noTransaction bool) (string, string, string) {
	if len(tokens) == 0 {
		return "", "", ""
	}
	if tokens[0] == "DROP" && len(tokens) > 1 && tokens[1] == "TABLE" {
		return "drop-table", "dropping a table breaks old application versions", "remove the table in a later release after all old versions stop using it"
	}
	if tokens[0] == "DO" || tokens[0] == "EXECUTE" {
		return "unchecked-dynamic-sql", "dynamic SQL can hide unsafe schema changes", "avoid dynamic DDL, or add -- spinup:allow-destructive with a reason"
	}
	if tokens[0] == "ALTER" && len(tokens) > 3 && tokens[1] == "TABLE" {
		for _, clause := range alterTableClauses(tokens) {
			if len(clause) == 0 {
				continue
			}
			if clause[0] == "DROP" && dropColumn(clause) {
				return "drop-column", "dropping a column breaks old application versions", "remove the column in a later release after all old versions stop using it"
			}
			if clause[0] == "RENAME" {
				return "rename", "renaming database objects breaks old application versions", "add a compatible replacement and remove the old name in a later release"
			}
			if clause[0] == "ALTER" && alterColumnType(clause) {
				return "alter-type", "changing a column type can break old application versions", "add a new compatible column and migrate data in a later release"
			}
			if clause[0] == "ALTER" && alterColumnSetNotNull(clause) {
				return "set-not-null", "adding NOT NULL can reject writes from old application versions", "backfill first, then add the constraint in a later release"
			}
			if clause[0] == "ADD" && addRequiredColumn(clause) {
				return "set-not-null", "adding a required column without a default breaks old application versions", "add the column as nullable or with a default, then constrain it in a later release"
			}
		}
	}
	if tokens[0] == "CREATE" && indexStatement(tokens) {
		if contains(tokens, "CONCURRENTLY") {
			if !noTransaction {
				return "concurrent-in-tx", "CREATE INDEX CONCURRENTLY cannot run in a transaction", "add -- +goose NO TRANSACTION to this migration"
			}
			return "", "", ""
		}
		if table, ok := indexTable(tokens); ok && !createdHere[table] && (earlier == nil || earlier[table]) {
			return "index-not-concurrent", "creating an index on an existing table can block writes", "use CREATE INDEX CONCURRENTLY in a NO TRANSACTION migration"
		}
	}
	return "", "", ""
}

func indexStatement(tokens []string) bool {
	return len(tokens) > 1 && (tokens[1] == "INDEX" || len(tokens) > 2 && tokens[1] == "UNIQUE" && tokens[2] == "INDEX")
}

func indexTable(tokens []string) (string, bool) {
	for i := 0; i+1 < len(tokens); i++ {
		if tokens[i] == "ON" {
			i++
			if i < len(tokens) && tokens[i] == "ONLY" {
				i++
			}
			if i < len(tokens) {
				return identifier(tokens[i]), true
			}
		}
	}
	return "", false
}

func contains(tokens []string, want string) bool {
	for _, token := range tokens {
		if token == want {
			return true
		}
	}
	return false
}

func containsSequence(tokens []string, first, second string) bool {
	for i := 0; i+1 < len(tokens); i++ {
		if tokens[i] == first && tokens[i+1] == second {
			return true
		}
	}
	return false
}

func identifier(token string) string {
	token = strings.Trim(token, `"`)
	if index := strings.LastIndex(token, "."); index >= 0 {
		token = token[index+1:]
	}
	return strings.ToLower(strings.Trim(token, `"`))
}

func alterClauses(tokens []string) [][]string {
	var clauses [][]string
	depth, start := 0, 0
	for i, token := range tokens {
		switch token {
		case "(":
			depth++
		case ")":
			if depth > 0 {
				depth--
			}
		case ",":
			if depth == 0 {
				clauses = append(clauses, tokens[start:i])
				start = i + 1
			}
		}
	}
	return append(clauses, tokens[start:])
}

func alterTableClauses(tokens []string) [][]string {
	i := 2
	if i+2 < len(tokens) && tokens[i] == "IF" && tokens[i+1] == "EXISTS" {
		i += 2
	}
	if i < len(tokens) && tokens[i] == "ONLY" {
		i++
	}
	if i >= len(tokens) {
		return nil
	}
	return alterClauses(tokens[i+1:])
}

func dropColumn(tokens []string) bool {
	i := 1
	if i < len(tokens) && tokens[i] == "COLUMN" {
		i++
	}
	if i+1 < len(tokens) && tokens[i] == "IF" && tokens[i+1] == "EXISTS" {
		i += 2
	}
	if i >= len(tokens) {
		return false
	}
	return tokens[i] != "CONSTRAINT" && tokens[i] != "DEFAULT" && tokens[i] != "NOT"
}

func alterColumnType(tokens []string) bool {
	i := 1
	if i < len(tokens) && tokens[i] == "COLUMN" {
		i++
	}
	return i+1 < len(tokens) && (tokens[i+1] == "TYPE" || i+2 < len(tokens) && tokens[i+1] == "SET" && tokens[i+2] == "DATA")
}

func alterColumnSetNotNull(tokens []string) bool {
	i := 1
	if i < len(tokens) && tokens[i] == "COLUMN" {
		i++
	}
	return i+2 < len(tokens) && tokens[i+1] == "SET" && tokens[i+2] == "NOT"
}

func addRequiredColumn(tokens []string) bool {
	i := 1
	if i < len(tokens) && tokens[i] == "COLUMN" {
		i++
	}
	if i >= len(tokens) || tokens[i] == "CONSTRAINT" || tokens[i] == "PRIMARY" || tokens[i] == "UNIQUE" || tokens[i] == "CHECK" || tokens[i] == "FOREIGN" {
		return false
	}
	return containsSequence(tokens[i:], "NOT", "NULL") && !contains(tokens[i:], "DEFAULT") && !(contains(tokens[i:], "GENERATED") && contains(tokens[i:], "STORED"))
}

func tokenize(sql []byte) []statement {
	var statements []statement
	var tokens []string
	line, start := 1, 0
	flush := func() {
		if len(tokens) != 0 {
			statements = append(statements, statement{tokens: tokens, line: start})
			tokens = nil
		}
	}
	for i := 0; i < len(sql); {
		if sql[i] == '\n' {
			line++
			i++
			continue
		}
		if isSpace(sql[i]) {
			i++
			continue
		}
		if i+1 < len(sql) && sql[i] == '-' && sql[i+1] == '-' {
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(sql) && sql[i] == '/' && sql[i+1] == '*' {
			i += 2
			for i+1 < len(sql) && (sql[i] != '*' || sql[i+1] != '/') {
				if sql[i] == '\n' {
					line++
				}
				i++
			}
			if i+1 < len(sql) {
				i += 2
			}
			continue
		}
		if sql[i] == '\'' {
			i = skipQuoted(sql, i, '\'', &line)
			continue
		}
		if sql[i] == '$' {
			if end, ok := dollarEnd(sql, i); ok {
				line += bytes.Count(sql[i:end], []byte("\n"))
				i = end
				continue
			}
		}
		if sql[i] == ';' {
			flush()
			i++
			continue
		}
		if sql[i] == ',' || sql[i] == '(' || sql[i] == ')' {
			tokens = append(tokens, string(sql[i]))
			i++
			continue
		}
		if len(tokens) == 0 {
			start = line
		}
		if sql[i] == '"' {
			end := skipQuoted(sql, i, '"', &line)
			tokens = append(tokens, strings.ToUpper(string(sql[i:end])))
			i = end
			continue
		}
		if isWord(sql[i]) {
			end := i + 1
			for end < len(sql) && (isWord(sql[end]) || sql[end] == '.') {
				end++
			}
			tokens = append(tokens, strings.ToUpper(string(sql[i:end])))
			i = end
			continue
		}
		i++
	}
	flush()
	return statements
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\r' }
func isWord(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

func skipQuoted(sql []byte, i int, quote byte, line *int) int {
	i++
	for i < len(sql) {
		if sql[i] == '\n' {
			*line++
		}
		if sql[i] == quote {
			if i+1 < len(sql) && sql[i+1] == quote {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return i
}

func dollarEnd(sql []byte, i int) (int, bool) {
	j := i + 1
	for j < len(sql) && (isWord(sql[j]) || sql[j] == '$') {
		if sql[j] == '$' {
			tag := sql[i : j+1]
			if end := bytes.Index(sql[j+1:], tag); end >= 0 {
				return j + 1 + end + len(tag), true
			}
			return 0, false
		}
		j++
	}
	return 0, false
}
