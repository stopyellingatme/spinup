---
title: generate component — add UI components
parent: Command guides
---

# generate component — add UI components

`spinup g component` adds [shadcn-svelte](https://www.shadcn-svelte.com) components, such as a dialog or a dropdown menu, to the web app.
Use it when a page needs a component that the app does not have yet.
The command needs the network.

```console
$ spinup new todo --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup g component dialog dropdown-menu --dry-run
run pnpm dlx shadcn-svelte@1.7.0 add dialog dropdown-menu --yes --no-deps-install (in web)
```

Drop `--dry-run` to run the command.
The dry run shows the command and runs nothing.

<!-- doctest: skip -->
```console
$ spinup g component dialog dropdown-menu
...
Next: cd web && pnpm install
Next: cd web && pnpm check
```

## What it does

The command runs the shadcn-svelte CLI in `web/`.
The CLI copies each component to `web/src/lib/components/ui/<name>/`.
It adds the npm packages that the components need to `web/package.json`, but it does not install them.
After the command, run the two `Next:` commands to install the packages and type-check the app.

You own the copied files, like any other source file.
The generator writes no file itself, so `spinup.lock` does not list them.
`spinup upgrade --ui` compares your copies with a newer shadcn-svelte version when you want to update them.

## Pick the component names

Name the components as the shadcn-svelte site does.
Use lower-case letters, digits, and hyphens.
You can give more than one name.

```console
$ spinup g component Dialog
Error: generator "component" failed: component name "Dialog" is invalid: use lower-case letters, digits, and hyphens
  Try: spinup g component dialog dropdown-menu
[exit 1]
```

## Choose the registry version

The command uses the shadcn-svelte version in the `shadcn_registry` answer of `spinup.lock`.
Without the answer, it uses `1.7.0`.
The answer must be a version such as `1.7.0`.

## Use the JSON output

`--json` lists the external command in the `commands` field.
Scripts and editors can read it.

```console
$ spinup g component dialog --dry-run --json | grep -o '"commands".*'
"commands":[{"Dir":"web","Argv":["pnpm","dlx","shadcn-svelte@1.7.0","add","dialog","--yes","--no-deps-install"]}]}
```

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `the app has no web pack` | The app was made with `--api-only`. Components need the web app. |
| `expected at least one component name` | Name at least one component, such as `dialog`. |
| The command fails to download | It needs the network. Check the connection and run the command again. The error shows the command to run by hand. |
| `pnpm check` fails after the command | Run `cd web && pnpm install` first. The command does not install packages. |

## See also

- [generate page](generate-page.md): add a page that uses the component
- [The web app](../web.md)
- [upgrade](upgrade.md): `spinup upgrade --ui` reviews component changes
- Flag reference: [`generate component`](../site/commands/generate-component.md)
