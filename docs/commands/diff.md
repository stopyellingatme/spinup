---
title: diff — compare files with the template
parent: Command guides
---

# diff — compare files with the template

`spinup diff` shows how your files differ from the template that made them.
Use it to see what you changed, or to find out why [`spinup check`](check.md) reports drift.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ echo "# my change" >> deploy/Dockerfile.api
$ spinup diff deploy/Dockerfile.api
diff a/deploy/Dockerfile.api b/deploy/Dockerfile.api
--- a/deploy/Dockerfile.api
+++ b/deploy/Dockerfile.api
...
+# my change
```

The output is a unified diff. `a/` is the template version, and `b/` is the file on your disk.
A line that starts with `+` is in your file only. A line that starts with `-` is in the template only.
Without a path, the command compares every tracked file. A file that matches the template prints nothing.
A binary file prints `binary files differ`. The command exits with 0 even when it finds differences.

## Show one file or folder

Pass one or more paths to limit the output:

```console
$ spinup diff deploy/
diff a/deploy/Dockerfile.api b/deploy/Dockerfile.api
...
+# my change
$ spinup diff deploy/Dockerfile.api deploy/Caddyfile
diff a/deploy/Dockerfile.api b/deploy/Dockerfile.api
...
+# my change
```

## Read the other states

A file that has no text diff gets one line instead, with a tab between the columns:

```text
missing	managed	deploy/Dockerfile.postgres
```

| State | Meaning |
| --- | --- |
| `missing` | The lock lists the file, and the file is not on disk. `spinup restore` brings it back. |
| `tombstone` | You deleted the file, an upgrade noted it, and spinup will not write it again. |
| `untracked` | The template makes this file, and the lock does not list it. |

The second column is the [file class](new.md#file-classes) of the file: `managed`, `scaffold`, or `seed`.
When you generate a resource, the generator changes some files too, so they also show up in the diff.

## Get JSON

`--json` prints an object with a `files` array. Each item has `path`, `class`, and `state`:

```console
$ spinup diff --json deploy/
{
  "files": [
    {
      "path": "deploy/Dockerfile.api",
      "class": "managed",
      "state": "modified"
    }
  ]
}
```

## Work offline

`--offline` never downloads a template. If the template of your lock is not in the cache, run `spinup cache warm` once.
`--dir` compares an app in another folder.

```console
$ spinup diff --dir . --offline deploy/Dockerfile.api
diff a/deploy/Dockerfile.api b/deploy/Dockerfile.api
...
+# my change
```

## Flags

| Flag | What it does | Example |
| --- | --- | --- |
| `[path...]` | Limit the output to these files or folders. | `spinup diff deploy/` |
| `--json` | Print a `files` array. | `spinup diff --json` |
| `--dir <directory>`, `-C` | Compare the app in this folder. | `spinup diff -C ~/code/todo` |
| `--offline` | Never download template versions. | `spinup diff --offline` |

## See also

- [`check`](check.md) turns drift into an exit code for CI.
- [`restore`](restore.md) puts a file back as the template made it.
- [`eject`](eject.md) keeps your edit and stops spinup from managing the file.
- Flag reference: [`diff`](../site/commands/diff.md)
