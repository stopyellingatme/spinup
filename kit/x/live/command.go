package live

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5"
)

// commandUsage is the usage line of the app command live (ADR 0033 section 10).
const commandUsage = "live feeds [--json] | live prune [--older-than 1h] | live reset (KEY | --all) --yes"

// The statements of the command live.
const (
	feedsSQL = `SELECT f.key, f.epoch, f.seq, (SELECT count(*) FROM live_outbox o WHERE o.feed = f.key), f.updated_at
FROM live_feeds f ORDER BY f.key`
	// resetSQL sets a new epoch with the clock expression of the migration.
	// The update runs first: it locks the feed rows, so a concurrent Publish
	// commits before the delete below takes its snapshot, or waits for the reset.
	resetSQL = `UPDATE live_feeds SET seq = 0,
    epoch = greatest(epoch + 1, (extract(epoch FROM clock_timestamp()) * 1000)::bigint),
    updated_at = now()`
	resetDeleteSQL = `DELETE FROM live_outbox WHERE feed = ANY($1)`
	resetNotifySQL = `SELECT pg_notify('` + notifyChannel + `', '0 ' || k) FROM unnest($1::text[]) AS k`
)

// Command returns the app command live: feeds, prune, and reset (ADR 0033
// section 10). It writes to standard output and never prompts. Register it
// with app.WithCommand("live", hub.Command()).
func (h *Hub) Command() func(ctx context.Context, args []string) error {
	return func(ctx context.Context, args []string) error {
		return h.runCommand(ctx, args, os.Stdout)
	}
}

// commandError returns an error of the form "live <action>: <why>.\nTry: <usage>".
func commandError(action, why string) error {
	if action != "" {
		action = " " + action
	}
	return fmt.Errorf("live%s: %s.\nTry: %s", action, why, commandUsage)
}

// commandArgs holds the parsed flags and arguments of one action.
type commandArgs struct {
	asJSON    bool
	yes       bool
	all       bool
	olderThan string
	keys      []string
}

// parseCommandArgs parses the flags of an action by hand. A flag has the form
// --name, -name, --name=value, or --name value.
func parseCommandArgs(action string, args []string) (commandArgs, error) {
	var out commandArgs
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if len(arg) < 2 || arg[0] != '-' {
			out.keys = append(out.keys, arg)
			continue
		}
		name := strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")
		name, value, hasValue := strings.Cut(name, "=")
		var flag *bool
		switch {
		case name == "json" && action == "feeds":
			flag = &out.asJSON
		case name == "yes" && action == "reset":
			flag = &out.yes
		case name == "all" && action == "reset":
			flag = &out.all
		case name == "older-than" && action == "prune":
			if !hasValue {
				if i+1 == len(args) {
					return out, commandError(action, "the flag --older-than requires a duration, such as 1h")
				}
				i++
				value = args[i]
			}
			out.olderThan = value
			continue
		default:
			return out, commandError(action, fmt.Sprintf("the flag %q is unknown", arg))
		}
		if hasValue {
			b, err := strconv.ParseBool(value)
			if err != nil {
				return out, commandError(action, fmt.Sprintf("the flag --%s takes no value", name))
			}
			*flag = b
			continue
		}
		*flag = true
	}
	return out, nil
}

func (h *Hub) runCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return commandError("", "no action was given")
	}
	action := args[0]
	switch action {
	case "feeds", "prune", "reset":
	default:
		return commandError("", fmt.Sprintf("the action %q is unknown", action))
	}
	parsed, err := parseCommandArgs(action, args[1:])
	if err != nil {
		return err
	}
	if action != "reset" && len(parsed.keys) > 0 {
		return commandError(action, fmt.Sprintf("the argument %q is not expected", parsed.keys[0]))
	}
	switch action {
	case "feeds":
		return h.commandFeeds(ctx, out, parsed.asJSON)
	case "prune":
		return h.commandPrune(ctx, out, parsed.olderThan)
	default:
		return h.commandReset(ctx, out, parsed)
	}
}

// feedInfo is one row of the action feeds.
type feedInfo struct {
	Key     string    `json:"key"`
	Epoch   int64     `json:"epoch"`
	Seq     int64     `json:"seq"`
	Patches int64     `json:"patches"`
	Updated time.Time `json:"updated"`
}

func (h *Hub) commandFeeds(ctx context.Context, out io.Writer, asJSON bool) error {
	rows, err := h.pool.Query(ctx, feedsSQL)
	if err != nil {
		return fmt.Errorf("live feeds: %w", dbError("list feeds", err))
	}
	feeds, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (feedInfo, error) {
		var f feedInfo
		err := row.Scan(&f.Key, &f.Epoch, &f.Seq, &f.Patches, &f.Updated)
		f.Updated = f.Updated.UTC()
		return f, err
	})
	if err != nil {
		return fmt.Errorf("live feeds: %w", dbError("list feeds", err))
	}
	if asJSON {
		if feeds == nil {
			feeds = []feedInfo{}
		}
		data, err := json.Marshal(feeds)
		if err != nil {
			return fmt.Errorf("live feeds: cannot encode the feeds: %w.\nTry: retry the command", err)
		}
		return writeOutput(out, "feeds", string(data)+"\n")
	}
	var text strings.Builder
	w := tabwriter.NewWriter(&text, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "KEY\tEPOCH\tSEQ\tPATCHES\tUPDATED")
	for _, f := range feeds {
		_, _ = fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%s\n", f.Key, f.Epoch, f.Seq, f.Patches, f.Updated.Format(time.RFC3339))
	}
	_ = w.Flush()
	return writeOutput(out, "feeds", text.String())
}

func (h *Hub) commandPrune(ctx context.Context, out io.Writer, olderThan string) error {
	age := h.opts.retention
	if olderThan != "" {
		d, err := time.ParseDuration(olderThan)
		if err != nil || d <= 0 {
			return commandError("prune", fmt.Sprintf("the age %q is not a positive duration", olderThan))
		}
		age = d
	}
	n, err := Prune(ctx, h.pool, PruneOptions{Patches: age})
	if err != nil {
		return fmt.Errorf("live prune: %w", err)
	}
	return writeOutput(out, "prune", fmt.Sprintf("pruned %d patches and %d feeds\n", n.Patches, n.Feeds))
}

func (h *Hub) commandReset(ctx context.Context, out io.Writer, a commandArgs) error {
	switch {
	case len(a.keys) > 1:
		return commandError("reset", fmt.Sprintf("the argument %q is not expected", a.keys[1]))
	case a.all && len(a.keys) == 1:
		return commandError("reset", "pass a KEY or --all, not both")
	case !a.all && len(a.keys) == 0:
		return commandError("reset", "a KEY or --all is required")
	case !a.yes:
		return commandError("reset", "--yes is required, because the command deletes the patches of the feed")
	}
	var key Key
	if !a.all {
		key = Key(a.keys[0])
		if err := key.Valid(); err != nil {
			return commandError("reset", fmt.Sprintf("the key %q is not valid", a.keys[0]))
		}
	}
	keys, err := h.reset(ctx, key, a.all)
	if err != nil {
		return fmt.Errorf("live reset: %w", err)
	}
	if !a.all && len(keys) == 0 {
		return fmt.Errorf("live reset: the feed %q has no row.\nTry: run live feeds to see the keys of the feeds", string(key))
	}
	var text strings.Builder
	for _, k := range keys {
		text.WriteString("reset " + k + "\n")
	}
	if len(keys) == 0 {
		text.WriteString("no feed to reset\n")
	}
	return writeOutput(out, "reset", text.String())
}

// reset runs the reset of section 10 in one transaction: a new epoch and seq 0
// for the feed (or all feeds), no outbox rows, and a notification for each
// feed, so each stream of the feed gets sync with the reason reset. It returns
// the keys of the feeds that it reset, sorted.
func (h *Hub) reset(ctx context.Context, key Key, all bool) ([]string, error) {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return nil, dbError("reset feeds", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	query, args := resetSQL+" RETURNING key", []any(nil)
	if !all {
		query, args = resetSQL+" WHERE key = $1 RETURNING key", []any{string(key)}
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, dbError("reset feeds", err)
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, dbError("reset feeds", err)
	}
	if len(keys) == 0 {
		return nil, nil
	}
	slices.Sort(keys)
	if _, err := tx.Exec(ctx, resetDeleteSQL, keys); err != nil {
		return nil, dbError("reset feeds", err)
	}
	if _, err := tx.Exec(ctx, resetNotifySQL, keys); err != nil {
		return nil, dbError("reset feeds", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, dbError("reset feeds", err)
	}
	return keys, nil
}

// writeOutput writes the output of an action.
func writeOutput(out io.Writer, action, text string) error {
	if _, err := io.WriteString(out, text); err != nil {
		return fmt.Errorf("live %s: cannot write the output: %w.\nTry: check standard output, then run the command again", action, err)
	}
	return nil
}
