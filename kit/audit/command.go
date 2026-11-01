package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Limits of the operator command (ADR 0025 decision 9).
const (
	// cmdDefaultLimit is the default of audit list --limit.
	cmdDefaultLimit = 100
	// cmdMaxLimit is the largest audit list --limit. Use audit export for more.
	cmdMaxLimit = 1000
	// exportPage is the number of rows that audit export reads and writes at once.
	exportPage = 1000
)

const (
	usageList   = "audit list --resource TABLE [--id ID] [--actor EMAIL] [--action ACTION] [--since DATE] [--until DATE] [--unattributed] [--limit 100] [--json]"
	usageExport = "audit export --since DATE --until DATE --format jsonl|csv [--out FILE]"
	usagePrune  = "audit prune --before DATE --reason TEXT --yes"
	usageAll    = "audit list --resource TABLE | " + usageExport + " | " + usagePrune
)

// Command returns the operator command "audit" of the app binary: list, export,
// and prune. Register it with app.WithCommand("audit", audit.Command(pool)).
// The command never prompts. A missing flag fails and names the flag, and every
// error ends with a Try: line.
func Command(pool *pgxpool.Pool) func(ctx context.Context, args []string) error {
	return func(ctx context.Context, args []string) error {
		return runCommand(ctx, pool, args, os.Stdout, os.Stderr)
	}
}

func runCommand(ctx context.Context, pool *pgxpool.Pool, args []string, out, errOut io.Writer) error {
	if len(args) == 0 {
		return cmdError("audit", "no action was given", usageAll)
	}
	switch args[0] {
	case "list":
		return listCommand(ctx, pool, args[1:], out, errOut)
	case "export":
		return exportCommand(ctx, pool, args[1:], out, errOut)
	case "prune":
		return pruneCommand(ctx, pool, args[1:], out)
	default:
		return cmdError("audit", fmt.Sprintf("unknown action %q. Available actions: list, export, prune", args[0]), usageAll)
	}
}

func cmdError(action, why, try string) error {
	return fmt.Errorf("%s: %s\nTry: %s", action, why, try)
}

func newFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {}
	return flags
}

// parseFlags parses args and refuses positional arguments.
func parseFlags(flags *flag.FlagSet, action string, args []string, usage string) error {
	if err := flags.Parse(args); err != nil {
		why := err.Error()
		if errors.Is(err, flag.ErrHelp) {
			why = "help was requested"
		}
		return cmdError(action, why, usage)
	}
	if flags.NArg() != 0 {
		return cmdError(action, fmt.Sprintf("unexpected argument %q", flags.Arg(0)), usage)
	}
	return nil
}

// parseDate reads a date flag: YYYY-MM-DD, YYYY-MM-DD HH:MM, or an RFC 3339
// time. A time without a zone is UTC.
func parseDate(action, name, value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return t, nil
		}
	}
	return time.Time{}, cmdError(action, fmt.Sprintf("--%s %q is not a date", name, value),
		"use YYYY-MM-DD or an RFC 3339 time, such as --"+name+" 2026-11-03 or --"+name+" 2026-11-03T14:40:00Z")
}

func needPool(action string, pool *pgxpool.Pool) error {
	if pool == nil {
		return cmdError(action, "the database pool is nil", "pass the pool from db.Open to audit.Command, and set DATABASE_URL")
	}
	return nil
}

func listCommand(ctx context.Context, pool *pgxpool.Pool, args []string, out, errOut io.Writer) error {
	const action = "audit list"
	flags := newFlagSet("list")
	resource := flags.String("resource", "", "the table")
	id := flags.String("id", "", "the record ID")
	actor := flags.String("actor", "", "the actor email")
	act := flags.String("action", "", "the action")
	since := flags.String("since", "", "the first date")
	until := flags.String("until", "", "the end date (not included)")
	unattributed := flags.Bool("unattributed", false, "only events without an actor")
	limit := flags.Int("limit", cmdDefaultLimit, "the number of events")
	asJSON := flags.Bool("json", false, "one JSON object for each line")
	if err := parseFlags(flags, action, args, usageList); err != nil {
		return err
	}
	if *resource == "" {
		return cmdError(action, "--resource is required", "audit list --resource work_orders --id 42")
	}
	if *limit < 1 || *limit > cmdMaxLimit {
		return cmdError(action, fmt.Sprintf("--limit %d is not from 1 to %d", *limit, cmdMaxLimit),
			fmt.Sprintf("use --limit %d or less, or read every event with %s", cmdMaxLimit, usageExport))
	}
	f := Filter{Table: *resource, RecordID: *id, ActorEmail: *actor, Action: *act, Unattributed: *unattributed}
	var err error
	if *since != "" {
		if f.Since, err = parseDate(action, "since", *since); err != nil {
			return err
		}
	}
	if *until != "" {
		if f.Until, err = parseDate(action, "until", *until); err != nil {
			return err
		}
	}
	if err := needPool(action, pool); err != nil {
		return err
	}
	events, err := list(ctx, pool, f, *limit)
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	var buf bytes.Buffer
	for _, e := range events {
		if *asJSON {
			line, err := json.Marshal(e)
			if err != nil {
				return cmdError(action, "cannot encode an event: "+err.Error(), "report this error")
			}
			buf.Write(line)
			buf.WriteByte('\n')
			continue
		}
		buf.WriteString(formatEvent(e, *id == ""))
		buf.WriteByte('\n')
	}
	if _, err := out.Write(buf.Bytes()); err != nil {
		return cmdError(action, "cannot write the output: "+err.Error(), "check standard output and retry")
	}
	if len(events) == 0 {
		_, _ = fmt.Fprintln(errOut, "audit list: no events match")
	}
	return nil
}

// formatEvent writes one event as one line, like the History page:
//
//	2026-11-03 14:40  ben@example.com (staff)  update  status: assigned -> done
//
// With withRecord, the action is followed by #<record ID>.
func formatEvent(e Event, withRecord bool) string {
	who := e.Actor.Email
	if who == "" {
		who = e.Actor.Kind
		if e.Actor.Reason != "" && e.Actor.Kind != KindUnattributed {
			who += " (" + e.Actor.Reason + ")"
		}
	}
	if e.Actor.Role != "" {
		who += " (" + e.Actor.Role + ")"
	}
	what := e.Action
	if withRecord {
		what += " #" + e.RecordID
	}
	line := e.OccurredAt.UTC().Format("2006-01-02 15:04") + "  " + clean(who) + "  " + clean(what)
	if changes := formatChanges(e.Changes); changes != "" {
		line += "  " + changes
	}
	return line
}

// formatChanges writes the changes in key order as "field: old -> new" parts.
func formatChanges(raw json.RawMessage) string {
	var fields map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &fields) != nil {
		return clean(string(raw))
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, clean(k)+": "+formatChange(fields[k]))
	}
	return strings.Join(parts, ", ")
}

// formatChange writes one field change of the trigger format (decision 3.3).
// Any other value (such as the count of an audit.prune event) is written as is.
func formatChange(raw json.RawMessage) string {
	var c map[string]json.RawMessage
	if json.Unmarshal(raw, &c) != nil {
		return formatValue(raw)
	}
	if string(c["redacted"]) == "true" {
		return "changed (value hidden)"
	}
	old, hasOld := c["old"]
	newValue, hasNew := c["new"]
	switch {
	case hasOld && hasNew:
		return formatValue(old) + " -> " + formatValue(newValue)
	case hasNew:
		return formatValue(newValue)
	case hasOld:
		return formatValue(old) + " -> (deleted)"
	}
	return formatValue(raw)
}

// formatValue writes a JSON value. A string is written without quotes unless it
// is empty or holds a control character. A truncated value names its size.
func formatValue(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" || strings.IndexFunc(s, unicode.IsControl) >= 0 {
			return strconv.Quote(s)
		}
		return s
	}
	var t struct {
		Truncated bool  `json:"truncated"`
		Bytes     int64 `json:"bytes"`
	}
	if json.Unmarshal(raw, &t) == nil && t.Truncated {
		return fmt.Sprintf("(%d bytes, truncated)", t.Bytes)
	}
	return clean(string(raw))
}

// clean replaces control characters, so a value cannot break the line or send
// terminal escapes.
func clean(s string) string {
	if strings.IndexFunc(s, unicode.IsControl) < 0 {
		return s
	}
	q := strconv.Quote(s)
	return q[1 : len(q)-1]
}

func exportCommand(ctx context.Context, pool *pgxpool.Pool, args []string, out, errOut io.Writer) (err error) {
	const action = "audit export"
	flags := newFlagSet("export")
	since := flags.String("since", "", "the first date")
	until := flags.String("until", "", "the end date (not included)")
	format := flags.String("format", "", "jsonl or csv")
	outPath := flags.String("out", "", "the output file")
	if err := parseFlags(flags, action, args, usageExport); err != nil {
		return err
	}
	switch {
	case *since == "":
		return cmdError(action, "--since is required", usageExport)
	case *until == "":
		return cmdError(action, "--until is required", usageExport)
	case *format == "":
		return cmdError(action, "--format is required", usageExport)
	case *format != "jsonl" && *format != "csv":
		return cmdError(action, fmt.Sprintf("--format %q is not jsonl or csv", *format), usageExport)
	}
	from, err := parseDate(action, "since", *since)
	if err != nil {
		return err
	}
	to, err := parseDate(action, "until", *until)
	if err != nil {
		return err
	}
	if !to.After(from) {
		return cmdError(action, "--until is not after --since", "pass an --until date after the --since date")
	}
	if err := needPool(action, pool); err != nil {
		return err
	}

	dest, name, summary := out, "-", errOut
	if *outPath != "" {
		// The file holds personal data: only the owner reads it. An existing
		// file is never replaced.
		file, err := os.OpenFile(*outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return cmdError(action, "cannot create the output file: "+err.Error(), "pass a new file path to --out, or remove the old file")
		}
		defer func() {
			if cerr := file.Close(); cerr != nil && err == nil {
				err = cmdError(action, "cannot close the output file: "+cerr.Error(), "check the disk and run the export again")
			}
			if err != nil {
				_ = os.Remove(*outPath)
			}
		}()
		dest, name, summary = file, *outPath, out
	}
	sum := sha256.New()
	count, err := export(ctx, pool, from, to, *format, io.MultiWriter(dest, sum))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(summary, "exported %d events\n%s  %s\n", count, hex.EncodeToString(sum.Sum(nil)), name)
	if err != nil {
		return cmdError(action, "cannot write the output: "+err.Error(), "check standard output and retry")
	}
	return nil
}

// csvHeader is the first line of a CSV export.
var csvHeader = []string{"id", "occurred_at", "action", "table_name", "record_id", "owner_id", "actor_kind", "actor_id",
	"actor_email", "actor_role", "source", "reason", "request_id", "changes"}

// export writes the events from from (included) to to (not included) in id
// order. It reads pages of exportPage rows with a keyset cursor (id > last) and
// writes each page at once, so it never holds the full result in memory.
func export(ctx context.Context, q Querier, from, to time.Time, format string, w io.Writer) (int64, error) {
	const action = "audit export"
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	if format == "csv" {
		_ = cw.Write(csvHeader)
	}
	var last, count int64
	for {
		rows, err := q.Query(ctx, `SELECT `+eventColumns+` FROM audit_events
			WHERE occurred_at >= $1 AND occurred_at < $2 AND id > $3 ORDER BY id LIMIT $4`, from, to, last, exportPage)
		if err != nil {
			return count, fmt.Errorf("%s: %w", action, wrap("read the events", err, "check the database connection, then retry"))
		}
		page, err := pgx.CollectRows(rows, scanEvent)
		if err != nil {
			return count, fmt.Errorf("%s: %w", action, wrap("read the events", err, "check the database connection, then retry"))
		}
		for _, e := range page {
			if err := writeExport(&buf, cw, format, e); err != nil {
				return count, cmdError(action, "cannot encode the event "+strconv.FormatInt(e.ID, 10)+": "+err.Error(), "report this error")
			}
		}
		if format == "csv" {
			cw.Flush()
			if err := cw.Error(); err != nil {
				return count, cmdError(action, "cannot encode the events: "+err.Error(), "report this error")
			}
		}
		if _, err := w.Write(buf.Bytes()); err != nil {
			return count, cmdError(action, "cannot write the events: "+err.Error(), "check the disk or the output, then run the export again")
		}
		buf.Reset()
		count += int64(len(page))
		if len(page) < exportPage {
			return count, nil
		}
		last = page[len(page)-1].ID
	}
}

func writeExport(buf *bytes.Buffer, cw *csv.Writer, format string, e Event) error {
	if format == "jsonl" {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
		return nil
	}
	return cw.Write([]string{
		strconv.FormatInt(e.ID, 10), e.OccurredAt.UTC().Format(time.RFC3339Nano), e.Action, e.Table, e.RecordID,
		optInt(e.OwnerID), e.Actor.Kind, optInt(e.Actor.ID), e.Actor.Email, e.Actor.Role, e.Actor.Source,
		e.Actor.Reason, e.Actor.RequestID, string(e.Changes),
	})
}

func optInt(v int64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatInt(v, 10)
}

func pruneCommand(ctx context.Context, pool *pgxpool.Pool, args []string, out io.Writer) error {
	const action = "audit prune"
	flags := newFlagSet("prune")
	before := flags.String("before", "", "delete the events before this date")
	reason := flags.String("reason", "", "why the events are deleted")
	yes := flags.Bool("yes", false, "confirm the permanent delete")
	if err := parseFlags(flags, action, args, usagePrune); err != nil {
		return err
	}
	switch {
	case *before == "":
		return cmdError(action, "--before is required", usagePrune)
	case strings.TrimSpace(*reason) == "":
		return cmdError(action, "--reason is required", usagePrune)
	case !*yes:
		return cmdError(action, "--yes is required, because a prune deletes events permanently", usagePrune)
	}
	t, err := parseDate(action, "before", *before)
	if err != nil {
		return err
	}
	if err := needPool(action, pool); err != nil {
		return err
	}
	actor := Operator()
	actor.Reason = *reason
	n, err := Prune(WithActor(ctx, actor), pool, t, *reason)
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	if _, err := fmt.Fprintf(out, "pruned %d events before %s\n", n, t.UTC().Format(time.RFC3339)); err != nil {
		return cmdError(action, "cannot write the output: "+err.Error(), "check standard output")
	}
	return nil
}
