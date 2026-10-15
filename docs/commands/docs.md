# Docs

`spinup docs compat` prints the versions that work together.
It uses the local template store and the pinned stack manifest.

Use `--json` for machine output.
Use `--markdown` for Markdown output.
The default output is Markdown.

```console
$ spinup docs compat --markdown
...
```

`spinup docs reference --out <directory>` writes a deterministic Markdown page for each CLI command.
It also writes a command index.
Use it to update external command references.
