---
title: Windows
parent: Reference
nav_order: 12
---

# Windows

spinup ships a Windows program (`spinup.exe`) for amd64 and arm64. A few commands run on Windows itself.
The commands that start processes, use Docker, or reach a server need WSL2, the Linux layer of Windows.

For daily work, use WSL2. Then every command works as this documentation shows.

```sh
wsl --install
```

Run that command once in PowerShell as an administrator, then restart the computer.
Open the Ubuntu terminal that it installs. Install spinup and your tools inside it, as on Linux. See [Install spinup](../get-started/install.md).

```sh
wsl spinup dev
```

You can also start a command from PowerShell with the `wsl` prefix.

## What works on Windows

| Command | Native Windows | WSL2 |
| --- | --- | --- |
| `spinup new` | yes | yes |
| `spinup generate` | yes | yes |
| `spinup build` | yes. You need Go for the API, and Docker for images. | yes |
| `spinup upgrade --plan` | yes | yes |
| `spinup dev` | no | yes |
| `spinup deploy` | no | yes |
| `spinup server` | no | yes |
| Any other command | not tested. Use WSL2. | yes |

`dev`, `deploy`, and `server` depend on Linux processes, file behavior, and tools such as `ssh` and `docker`.
Run them from a WSL2 terminal that has those tools.

{: .tip }
Keep your app in the Linux file system of WSL2, such as `~/code/todo`, not under `C:\`. Builds are much faster there, and file modes stay correct.

## Line endings

A generated app has a `.gitattributes` file. Its first line keeps every text file on Unix line endings:

```text
* text=auto eol=lf
```

Linux scripts and container builds need these line endings. Git on Windows would otherwise change them to CRLF.
`spinup.lock` hashes the files with Unix line endings, so a line ending change never shows as an edit.

If you see `^M` in a script, or a script fails with `bad interpreter`, the file has Windows line endings.
Run `git add --renormalize .` to fix the files that Git tracks.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `spinup dev` fails on Windows | The command needs Linux processes. | Run it in a WSL2 terminal. |
| Docker is not found in WSL2 | Docker Desktop does not share WSL2 with your distribution. | In Docker Desktop, turn on WSL integration for the distribution. |
| A shell script fails with `bad interpreter` | The file has CRLF line endings. | Run `git add --renormalize .`, or check that `.gitattributes` is in the app. |

## See also

- [Install spinup](../get-started/install.md): install the program
- [Support policy](support.md): the platforms that spinup supports
- [The dev command guide](../commands/dev.md): the development loop
