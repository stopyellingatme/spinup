---
title: docs — compatibility and CLI reference
parent: Command guides
---

# docs — compatibility and CLI reference

`spinup docs` prints which versions work together, and it writes the CLI reference pages.
Use `docs compat` before an upgrade.
Use `docs reference` if you maintain documentation for spinup.

```console
$ spinup docs compat
# Compatibility
...
```

| Subcommand | What it does |
| --- | --- |
| `docs compat` | Prints the compatible CLI, kit, template, and tool versions. |
| `docs reference --out <dir>` | Writes one Markdown page for each command. |

## Show which versions work together

`spinup docs compat` lists the CLI version, the template versions, the PostgreSQL majors, the Go, Node, and SvelteKit versions, and the pinned images.
It reads the template store and the version manifest that this build carries.
The same data is on the [compatibility page](../site/compat.md).

By default the output is Markdown.
Add `--json` for JSON.
`--markdown` is the default and exists only to be explicit.
You cannot pass `--json` and `--markdown` together.

```console
$ spinup docs compat --json
{
...
}
```

## Write the CLI reference

`spinup docs reference --out <directory>` writes one Markdown page for each command, plus an index.
The output is the same each time, so Git shows only real changes.
The pages under `docs/site/` in the spinup repository come from this command.
Do not edit them by hand.

```console
$ spinup docs reference --out cli-reference
...
$ ls cli-reference
...
```

The examples on each page come from the `Example` field of the command.

## See also

- [Compatibility](../site/compat.md)
- [Kit compatibility](../policy/kit-compat.md)
- [Flag reference: docs](../site/commands/docs.md), [docs compat](../site/commands/docs-compat.md), [docs reference](../site/commands/docs-reference.md)
