---
title: build — build the API and images
parent: Command guides
---

# build — build the API and images

`spinup build` compiles the Go API into one binary. With `--image`, it builds the container images that you deploy.
Run it to check that the app compiles, or to make the images for a release.

```console
$ spinup new todo --api-only --module github.com/acme/todo --no-git --non-interactive
...
$ cd todo
$ spinup build --dry-run
go build -trimpath -ldflags '-X main.version=dev' -o bin/api ./cmd/api
```

Without `--dry-run`, the command runs that `go build` and writes `api/bin/api`.
`--dry-run` prints the command and does not run it. This page uses it so you can see what happens.

## Build the API

Run `spinup build` anywhere inside the app. The command finds the project root by itself.
To build from another place, pass `--dir <app directory>`.

Set the version that the binary reports with `--tag`. The default is `dev`.

```console
$ spinup build --tag v0.1.0 --dry-run
go build -trimpath -ldflags '-X main.version=v0.1.0' -o bin/api ./cmd/api
```

`--target` picks what to build: `api` (default), `web`, or `all`.
`spinup build` does not build the `node` web app yet. For that, run `pnpm build` in `web/`.
`--target all` builds what spinup can build, and says so:

```console
$ spinup build --target all --dry-run
Building available targets: api
go build -trimpath -ldflags '-X main.version=dev' -o bin/api ./cmd/api
$ spinup build --target web --dry-run
Error: Cannot build the web frontend
  the web pack is not available in this version
  Try: pass --target api
[exit 1]
```

If the app uses `--frontend-mode embed`, the command first builds the web app and copies it into the binary.
Then one binary holds both the API and the web app. See [Embed mode](../embed.md).

## Build container images

`--image` builds the images with Docker buildx: the API, the web app, and one for each sidecar service.
It uses the Dockerfiles in `deploy/` and `services/<name>/`. The order is API, web, then the services in the order of `spinup.toml`.

```console
$ spinup build --image --tag v0.1.0 --dry-run | grep -o -- '-t [^ ]*'
-t todo-api:v0.1.0
```

This app has no web app, so the command builds one image.
Without `--push`, the images stay on your machine under names like `todo-api:v0.1.0`, and buildx adds no provenance or SBOM.

Build only some images. Repeat `--target` or separate the names with commas:

```console
$ spinup build --image --target api --dry-run | grep -o -- '--platform [^ ]* -f [^ ]*'
--platform linux/amd64 -f deploy/Dockerfile.api
```

Build for another CPU with `--platform`. The default is `linux/amd64`, which most servers use:

```console
$ spinup build --image --platform linux/arm64 --dry-run | grep -o -- '--platform [^ ]*'
--platform linux/arm64
```

## Push to a registry

Add `--push` to send the images to a registry. The default registry is `ghcr.io`.
The GitHub owner comes from your Go module path, so use a module like `github.com/acme/todo`.
For a pushed image, buildx also records its provenance and a software bill of materials (SBOM).
The command prints the digest of each image.

```console
$ spinup build --image --push --tag v0.1.0 --dry-run | grep -o -- '-t [^ ]*'
-t ghcr.io/acme/todo-api:v0.1.0
```

To push to another registry, add `--registry`:

```console
$ spinup build --image --push --registry registry.example.com --tag v0.1.0 --dry-run | grep -o -- '-t [^ ]*'
-t registry.example.com/acme/todo-api:v0.1.0
```

Sign in first with `docker login <registry>`.
[`spinup deploy`](deploy.md) builds and pushes the images for you, so you rarely run this by hand.

## Flags

| Flag | What it does | Example |
| --- | --- | --- |
| `--tag <version>` | Set the binary version and the image tag. Default: `dev`. | `--tag v0.1.0` |
| `--image` | Build container images. | `--image` |
| `--target <name>` | Pick `api` or `all` for the binary. With `--image`, pick image names. | `--image --target api,web` |
| `--push` | Push the images, with provenance and an SBOM. | `--image --push` |
| `--platform <platform>` | Pick the target platform. Default: `linux/amd64`. | `--platform linux/arm64` |
| `--registry <registry>` | Replace the default `ghcr.io`. | `--registry registry.example.com` |
| `--dir <directory>`, `-C` | Find the app from this directory. | `-C ~/code/todo` |
| `--dry-run` | Print the commands and do not run them. | `--dry-run` |

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| An error about a missing `go` or `docker`. | The tool is not on your `PATH`. | Install it. The error says how. |
| `the API module does not identify a GitHub owner` | `--push` needs an owner, and your module is not `github.com/<owner>/...`. | Change the module path, or pass `--registry`. |
| `could not find spinup.toml` | You are outside an app. | Run the command in the app, or pass `--dir`. |
| The build fails on a `replace` of the kit. | `api/go.mod` points to a kit directory that does not exist. | Fix or remove the `replace` line. Relative paths start in `api/`. An image build gives a local kit directory to Docker as the build context `kit`. |

## See also

- [`deploy`](deploy.md) builds the images and ships them to a server.
- [Embed mode](../embed.md) builds the web app into the binary.
- [Generated CI](../ci.md) builds and pushes images for a release.
- Flag reference: [`build`](../site/commands/build.md)
