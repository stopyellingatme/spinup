# Build an application

`spinup build` finds the project root and builds the API in `api/`.
The default binary is `api/bin/api`.
The command embeds the version from `--tag`, or `dev` when the flag is absent.

```console
$ spinup new todo --api-only --module github.com/acme/todo --no-git --non-interactive
...
$ cd todo
$ cd api
$ go mod edit -dropreplace github.com/stopyellingatme/spinup/kit
$ cd ..
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
If `api/go.mod` replaces the kit with a local directory, an image build passes that directory to Docker buildx as `--build-context kit=<absolute directory>`.
A relative replace path is relative to `api/`.
The deploy build does the same.
If the directory does not exist, the build fails with a `Try:` line.
Without a local kit replace, the build uses the published kit version.
If project discovery fails, pass `--dir` with the application directory.

## Images and push

`--image` builds both deploy images with Docker buildx.
The default platform is `linux/amd64`.
Without `--push`, buildx loads local images and does not create provenance or an SBOM.

```console
$ spinup build --image --tag v0.1.0 --platform linux/amd64 --dry-run
...
```

Use `--push` to send images to GHCR or another registry.
The module path supplies the GitHub owner for the default `ghcr.io` registry.
Buildx creates provenance and an SBOM for pushed images.
The command prints each image digest for the deployer.

```console
$ spinup build --image --push --tag v0.1.0 --dry-run
...
```

| Flag | Purpose |
| --- | --- |
| `--push` | Push images and create provenance and an SBOM. |
| `--platform <platform>` | Select the Docker target platform. |
| `--registry <registry>` | Replace the default `ghcr.io` registry. |
