# Build an application

`spinup build` finds the project root and builds the API in `api/`.
The default binary is `api/bin/api`.
The command embeds the version from `--tag`, or `dev` when the flag is absent.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup build --dry-run
go build -trimpath -ldflags '-X main.version=dev' -o bin/api ./cmd/api
$ spinup build --target all --tag v0.1.0 --dry-run
Building available targets: api
go build -trimpath -ldflags '-X main.version=v0.1.0' -o bin/api ./cmd/api
```

| Flag | Purpose |
| --- | --- |
| `--target <name>` | Select `api`, `web`, or `all`. The default is `api`. |
| `--tag <version>` | Set the embedded version and image tag. The default is `dev`. |
| `--image` | Build `<app>-api:<tag>` with `deploy/Dockerfile.api`. |
| `--dir`, `-C <directory>` | Find the project root from this directory. |
| `--dry-run` | Print the command without execution. |
| `--env <name>` | Global flag. Local builds do not select an environment. |
| `--yes`, `-y` | Global flag. Builds do not require confirmation. |
| `--non-interactive` | Disable prompts. Builds do not prompt. |
| `--json` | Format errors as JSON. Dry runs print command text. |
| `--verbose` | Enable extra diagnostics. |
| `--no-color` | Disable color. |
| `--help`, `-h` | Print command help. |

The `web` target fails because M1 has no web pack.
The `all` target builds the available API and reports its choice.
A missing Go or Docker executable produces an error with installation guidance.
An image build fails when `api/go.mod` replaces the kit with a local directory.
Remove the replacement and use a published kit version before an image build.
If project discovery fails, pass `--dir` with the application directory.
