---
title: Live data
parent: Build your app
nav_order: 21
---

# Live data

The `live` module sends each committed change of a table to the open pages, with no reload.
A page loads a snapshot, then gets numbered patches over server-sent events (SSE).
PostgreSQL is the only broker. The module needs the `auth` module.

```console
$ spinup new todo --module example.com/todo --modules auth,live --no-git --non-interactive
...
$ cd todo
$ spinup g resource Note body:text --live
...
$ grep "spinup:live-feed" api/internal/live/notes.go
// spinup:live-feed notes registerNotesFeed
$ spinup doctor --quick 2>/dev/null | grep live-wired
ok	live-wired	live wiring is present
```

Run `spinup db migrate up` after you enable the module. The migration adds the tables `live_feeds` and `live_outbox`.
Open `/notes` in two browsers with the same account. A note that you add in one browser shows in the other.

## Enable the module

Add the module to a new app with `--modules auth,live`, as the first example shows.
To add it to an existing app that has `auth`, run `spinup module add`. The command makes a branch for you to review.

<!-- doctest: skip -->
```console
$ spinup module add live --yes --non-interactive
...
Next: git merge spinup/module-live
```

The module adds these files:

| Path | Class | What it holds |
| --- | --- | --- |
| `api/db/kit/00011_live.sql` | seed | The tables `live_feeds` and `live_outbox`, and the trigger that sends a notification. |
| `api/internal/live/live.go` | scaffold | `Deps` and `Routes`, which register every feed. |
| `api/internal/live/feeds_gen.go` | managed | `registerGenerated`, which calls the feeds that have a marker. |
| `web/src/lib/live.core.ts` | managed | The stream client. It has no Svelte import. |
| `web/src/lib/live.svelte.ts` | managed | `liveRefresh` and `liveList` for pages. |
| `web/src/lib/live.core.test.ts` | managed | The vitest tests of the stream client. |

The module also adds these lines to `api/cmd/api/main.go`. The three-way merge adds them to an app that you changed.

```go
var liveEnv struct {
	MaxStreams        int `env:"LIVE_MAX_STREAMS" default:"2000"`
	MaxStreamsPerUser int `env:"LIVE_MAX_STREAMS_PER_USER" default:"8"`
}
if err := config.Load(&liveEnv); err != nil {
	return err
}
liveHub, err := kitlive.New(pool,
	kitlive.WithMaxStreams(liveEnv.MaxStreams),
	kitlive.WithMaxStreamsPerUser(liveEnv.MaxStreamsPerUser),
)
if err != nil {
	return err
}
if err := liveHub.Start(ctx); err != nil {
	return err
}
// ...in app.New:
app.WithRequestTimeoutExempt(kitlive.IsStream),
app.WithRoutes(applive.Routes(liveHub, applive.Deps{DB: pool})),
app.WithCommand("live", liveHub.Command()),
app.WithReadiness("live", liveHub.Ready),
app.WithDrain(liveHub.Drain),
app.WithShutdown(liveHub.Close),
```

The doctor check `live-wired` finds a missing line. It names the line and shows it in the `Try:` line.
It also warns about a feed that `feeds_gen.go` does not register, and about a feed with no `Publish` call.

## How it works

A feed is a named list of rows, such as `notes`.
A feed of an owned resource has one key for each user, such as `notes:42`. A shared resource has one key, `notes`.
Each feed has two routes. Both are under `/api/`, so Caddy and the Vite proxy send them to the Go API.

| Route | What it answers |
| --- | --- |
| `GET /api/live/<feed>` | The snapshot: `{"epoch":E,"seq":N,"rows":[...]}`. The member `"more":true` shows that the list was cut. |
| `GET /api/live/<feed>/live` | The stream of events, with the content type `text/event-stream`. |

The snapshot cursor is `<epoch>-<seq>`, for example `1731500000000-42`.
The epoch is the time when the feed row was made, in milliseconds. The sequence counts the patches of the feed.
A feed with no patch yet has the cursor `0-0`.

A client loads the snapshot. Then it opens the stream with `?since=<cursor>` and applies each patch to its rows.
It keeps the cursor of the last patch that it got. After a lost connection, it connects again with that cursor and gets the missed patches.
A request with no `since` starts at the head of the feed.

The stream sends these events:

| Event | Data | What the client does |
| --- | --- | --- |
| `ready` | `{"epoch":E,"seq":N,"resumed":true}` | The stream is open. The data holds the head cursor of the feed, also on a resumed stream. |
| `patch` | `{"epoch":E,"seq":N,"changes":[...]}` | Apply each change: `upsert` replaces or adds the row with the ID, `delete` removes it, `touch` loads it again. |
| `sync` | `{"epoch":E,"seq":N,"reason":"gap"}` | The history from the cursor is gone. Load the snapshot again, then connect again. The stream ends. |
| `bye` | `{"reason":"age"}` | The server ends the stream, and the cursor is still good. Connect again with the cursor. |

A `patch` event has an `id` line with the cursor after the patch.
All changes of one transaction come in one `patch` event. A repeated patch is safe, because each change is idempotent.

```text
event: patch
id: 1731500000000-43
data: {"epoch":1731500000000,"seq":43,"changes":[{"op":"upsert","id":"7","row":{"id":7,"body":"x"}}]}
```

The `sync` reasons are:

| Reason | Cause |
| --- | --- |
| `epoch` | The cursor belongs to an older feed. The feed was reset, or deleted and made again. |
| `ahead` | The cursor is after the head of the feed. A database restore can cause this. |
| `gap` | The server no longer holds the patches after the cursor. |
| `slow` | The client read too slowly, and the patches that it needed left the memory of the server. |
| `reset` | An operator ran `live reset`. |

A `sync` event comes after a 200 answer with no `ready` event before it.
The server also sends a `: ping` comment every 15 seconds when the stream has no patch.

## What --live generates

`spinup g resource <Name> --live` writes the feed file `api/internal/live/<plural>.go` and updates `feeds_gen.go`.
It also changes three generated files of the resource:

- The handlers `Create`, `Update`, and `Delete` run their query in a transaction and call `live.Publish` as the last step.
- The handler test counts the rows in `live_outbox` for two users.
- The list page `web/src/routes/<plural>/+page.svelte` calls `liveRefresh('<plural>')`.

The generated `Create` handler looks like this:

```go
err = db.InTx(ctx, s.DB, func(tx pgx.Tx) error {
	var err error
	row, err = s.Queries.WithTx(tx).CreateNote(ctx, params)
	if err != nil {
		return err
	}
	item = noteFromStore(row)
	return live.Publish(ctx, tx, live.OwnedKey("notes", user.ID), live.Upsert(strconv.FormatInt(row.ID, 10), item))
})
```

A resource with an audit trail uses `audit.InTx` in place of `db.InTx`.
`Publish` writes the patch in the transaction of the write. If the transaction rolls back, no client sees the patch.
`Delete` publishes only when the query deleted a row.

Call `Publish` last in the transaction.
`Publish` locks the row of the feed in `live_feeds` until the transaction ends. So two writes to one feed commit one after the other, and the sequence has no gap.
An owned resource has one feed for each user, so the wait is rare. A shared feed on a table with many writes is a bottleneck.

## Refresh a page

`liveRefresh` refreshes the data of the page after each patch. Call it during component initialization.
The generated list pages use it, because their `load` functions already read the page with the correct filters.

```ts
import { liveRefresh } from '#lib/live.svelte.js';

const live = liveRefresh('notes', { debounceMs: 200, maxWaitMs: 2000 });
// live.status is 'connecting', 'live', 'reconnecting', 'paused', or 'unauthorized'.
```

The helper waits 200 ms after the last patch, and at most 2 s after the first patch. Then it runs `invalidateAll()`.
Set `invalidate` to a dependency string or to a function to refresh less.

`liveList` keeps the rows of the feed in a rune. Use it in a page that you write by hand.
It loads the snapshot, then applies each `upsert` and `delete`. A `touch` or a `sync` loads the snapshot again.

```ts
import { liveList } from '#lib/live.svelte.js';

type Note = { id: number; body: string };

const notes = liveList<Note>('notes', { sort: (a, b) => b.id - a.id, limit: 200 });
// notes.rows, notes.status, notes.more, and notes.refresh()
```

Both helpers use one stream for each feed in a tab. Two components on the feed `notes` share one connection.
If the tab is hidden for 60 seconds, or the browser goes offline, the helper closes the stream and keeps the cursor. It connects again when the tab is visible.
After a 401 answer, the helper runs `invalidateAll()` once, so the app sends the user to the sign-in page.

## Add a feed to an existing resource

Run `spinup g live <resource>` for a resource that you made without `--live`.
The command writes the feed file and updates `feeds_gen.go`. It does not change your handlers or pages.

```console
$ spinup g resource Task title:string
...
$ spinup g live task
...
$ grep -c registerTasksFeed api/internal/live/feeds_gen.go
1
```

The command prints the `Publish` code for `Create`, `Update`, and `Delete`, and the `liveRefresh` line for the list page.
Add them by hand. Until you do, `spinup doctor` warns that the feed `tasks` has no `Publish` call.
See [generate live](../commands/generate-live.md).

## Write a feed by hand

A feed is a Go file in `api/internal/live` with a marker comment.
It registers two routes: `Snapshot` with a loader, and `Stream`. Both use the same `KeyFunc`.
The `KeyFunc` runs on each request. It reads the user, checks the role, and returns the key of the feed that the user may read.

```go
package live

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/stopyellingatme/spinup/kit/auth"
	kitlive "github.com/stopyellingatme/spinup/kit/x/live"

	"example.com/todo/api/internal/roles"
)

type Order struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
}

// spinup:live-feed orders registerOrdersFeed
func registerOrdersFeed(mux *http.ServeMux, h *kitlive.Hub, d Deps) {
	key := func(r *http.Request) (kitlive.Key, error) {
		user, ok := auth.UserFrom(r.Context())
		if !ok {
			return "", kitlive.ErrUnauthorized
		}
		if !user.HasRole(roles.Staff) {
			return "", kitlive.ErrForbidden
		}
		return kitlive.SharedKey("orders"), nil
	}
	mux.Handle("GET /api/live/orders", kitlive.Snapshot(h, key, func(ctx context.Context, _ kitlive.Key, limit int) ([]Order, error) {
		rows, err := d.DB.Query(ctx, "SELECT id, status FROM orders ORDER BY id DESC LIMIT $1", limit)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByPos[Order])
	}))
	mux.Handle("GET /api/live/orders/live", h.Stream(key))
}
```

The kit asks the loader for one row more than the limit, and it sets `"more":true` when the loader returns that row.
A loader that has its own smaller limit calls `kitlive.MarkMore(ctx)` when it leaves rows out. The generated feeds do this.

Keep the marker `// spinup:live-feed <feed> register<Name>Feed` on the line before the function.
The generators read it to write `feeds_gen.go`. After you add a feed file, run `spinup g live <resource> --force` for any resource, or add the call to `feeds_gen.go`.

Then publish each write in its transaction, with the same key:

```go
err := db.InTx(ctx, pool, func(tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, "UPDATE orders SET status = $1 WHERE id = $2", status, id); err != nil {
		return err
	}
	return live.Publish(ctx, tx, live.SharedKey("orders"), live.Upsert(strconv.FormatInt(id, 10), Order{ID: id, Status: status}))
})
```

`Publish` takes 1 to 100 changes. An ID has 1 to 64 characters from `A-Z a-z 0-9 _ . : -`.
A row is at most 16 KiB as JSON, and a patch is at most 64 KiB. For a larger row, publish `live.Touch(id)`, and the client loads the row again.
A job may call `Publish` in its own transaction.

## Authorization and the 15-minute limit

The route decides who may read a feed. A cookie session and an API key with the `read` scope can both open a stream.
Each connect counts as one request of the API key plan.

The check runs when the stream opens. A role change, a revoked key, or a sign-out takes effect at the next connect.
A stream ends with `bye` (reason `age`) after 12 to 15 minutes, so a revoked session reads for 15 minutes at most.
The server does not check each patch again. A patch goes only to the streams of its key, and the key of an owned feed holds the user ID.

The stream answers these errors before it starts. Each body is `application/problem+json`, with a `Try:` line.

| Status | Cause |
| --- | --- |
| 400 | The `since` cursor is not valid. |
| 401 | No signed-in user. |
| 403 | The `KeyFunc` returned `ErrForbidden`. |
| 429 | The user has the most open streams (`Retry-After: 5`). |
| 503 | The server drains (`Retry-After: 1`), has the most streams (`Retry-After: 5`), loads other feeds (`Retry-After: 2`), or cannot read the database. |

A snapshot that cannot read its rows answers 503 with "The snapshot could not load. Try: retry the request later."

## Limits

| Setting | Default | What it limits |
| --- | --- | --- |
| `LIVE_MAX_STREAMS` | 2000 | Open streams in one API process. |
| `LIVE_MAX_STREAMS_PER_USER` | 8 | Open streams of one user in one API process. |
| Stream age | 15 min | The age of one stream, with a random end from 80 percent of the age. |
| Heartbeat | 15 s | The time between pings on an idle stream. |
| Snapshot rows | 500 | The rows of a snapshot. A generated feed reads at most 200, the page size of the list. |
| Feed loads | 4 at a time | The snapshot reads and the feed loads in one process. A request waits 2 s for a slot. |

Set the two variables in the `.env` file of the environment. The other limits are options of `kitlive.New`, such as `kitlive.WithMaxStreamAge`.
A stream is outside `REQUEST_TIMEOUT`. It has its own write timeout of 10 s for each write.

## Deploy behavior

The production Caddyfile sends `/api/live/*` to the API with `flush_interval -1`, so Caddy sends each event at once:

```text
handle /api/live/* {
	import upstream.caddy api 8080 { flush_interval -1 }
}
```

The stream sends `Cache-Control: no-cache, no-transform`. The value `no-transform` stops the Caddy `encode` directive from compressing the stream.
In development, the Vite proxy sends the stream to the API with no buffering.

During a blue/green deploy, the deployer drains the old color for 10 s. The open streams keep the old color busy for the full drain.
Then the old API gets SIGTERM. Each stream gets `bye` with the reason `drain`, and the API stops in less than 2 s.
The clients connect again to the new color after a random wait of 0.5 to 3 s, with their cursors.
A stream that ends with the reason `age` connects again after 0 to 1 s.

Each API process holds one extra database connection for `LISTEN`. It is not part of the pool.
The process also checks the feeds every 5 s, so a lost notification adds at most 5 s of delay.

## The live app command

The API binary has the command `live`. It needs the database, like the other app commands.

<!-- doctest: skip -->
```console
$ cd api
$ go run ./cmd/api live feeds
KEY       EPOCH          SEQ  PATCHES  UPDATED
notes:1   1731500000000  3    3        2026-11-13T10:00:00Z
$ go run ./cmd/api live reset notes:1 --yes
reset notes:1
```

| Action | What it does |
| --- | --- |
| `live feeds [--json]` | Prints each feed with its epoch, sequence, outbox rows, and update time. |
| `live prune [--older-than 1h]` | Deletes old patches and idle feeds. The server also prunes every 5 minutes. |
| `live reset (KEY \| --all) --yes` | Gives the feed a new epoch and the sequence 0, and deletes its patches. Each open stream of the feed gets `sync` with the reason `reset`. |

`live reset` updates the feed rows first and then deletes the outbox rows, in one transaction.
A reset of a key that has no feed row fails, and the error names `live feeds`.

## After a database restore

A restore can move the sequence of a feed back. A client with an old cursor can then get wrong patches.
After a restore, run this command once:

<!-- doctest: skip -->
```console
$ go run ./cmd/api live reset --all --yes
```

Each open page gets `sync`, loads the snapshot again, and continues.
The backup guides name this step: [Backups with pg_dump](../backups-pgdump.md) and [Backups with WAL-G](../backups-walg.md).

## Costs

- Each write of a live resource runs one more statement, and it locks the feed row until the commit.
- The outbox holds a copy of each changed row for one hour. The copy has the same access rule as the row.
- Each API process holds one `LISTEN` connection, and one stream for each open tab and feed.
- A deploy ends every stream, and all clients connect again at one time. The jitter and the limit of 4 feed loads spread the load.

## Not done

- A write outside the handlers publishes nothing. This covers `psql`, a SQL migration, and a job that does not call `Publish`.
- The server does not check access again for each patch. A role change takes effect at the next connect.
- Embed mode is not supported, because the module needs `auth`.
- The module has no WebSocket and no presence. SSE sends from the server to the client only.
- The routes are not in the OpenAPI document, and the generated client has no operation for them.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `live-wired` warns that a feed has no `Publish` call | The handlers do not publish. | Add `live.Publish` to the transaction of each write. |
| A page never updates | The stream goes through a proxy that buffers it. | Add `flush_interval -1` for `/api/live/*`, and keep `no-transform`. |
| The stream answers 429 | The user has 8 open streams. | Close tabs, or raise `LIVE_MAX_STREAMS_PER_USER`. |
| The snapshot answers 503 | The database failed, or 4 loads run already. | Check the API log, then retry. |

## See also

- [generate live](../commands/generate-live.md)
- [generate resource](../commands/generate-resource.md#add-live-data)
- [Background jobs](jobs.md)
- [ADR 0033: Live data module](../adr/0033-live.md)
