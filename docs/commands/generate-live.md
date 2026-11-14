---
title: generate live — add a live feed
parent: Command guides
---

# generate live — add a live feed

`spinup g live <resource>` adds a live feed to an existing resource.
The app needs the `auth` and `live` modules.

```console
$ spinup new liveapp --module example.com/liveapp --modules ci,auth,live --no-git --non-interactive
...
$ cd liveapp
$ spinup g resource note body:text
...
$ spinup g live note
...
$ grep -c "spinup:live-feed notes registerNotesFeed" api/internal/live/notes.go
1
```

The command writes `api/internal/live/<plural>.go` with a snapshot loader and a stream route.
It updates `api/internal/live/feeds_gen.go` from the markers in the feed files.
The feed uses the owner scope and the roles of the resource.

The command preserves the existing handlers and pages.
Follow the printed `Next:` instructions to publish each write in its transaction.
Call `liveRefresh('<plural>')` during initialization of the list component.
If the live module is new, run `spinup db migrate up`.

## Flags

| Flag | What it does |
| --- | --- |
| `--force` | Replace a feed file that exists. |

## Generate a resource with live data

`spinup g resource <Name> <field:type>... --live` also generates transactional publishing and refreshes the list page.
See [Resources](generate-resource.md#add-live-data).
