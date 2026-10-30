# Generate a service

Run `spinup g service <name> --lang rust|python` from the application root.
The command also accepts `spinup generate service`.
The `--lang` flag is required.

The generator creates `services/<name>/` and appends a `[[services]]` table to `spinup.toml`.
It preserves the existing configuration bytes.
It refuses an existing directory or a declared name, including with `--force`.
Names must match `^[a-z][a-z0-9]{0,19}$` and must not use a reserved name.

Use `--port` to select the container port.
The default is 8080.
Use `--dev-port` to select the host port.
The default is the first unused port from 8101.
Each service must have a distinct host port.

Both templates provide `GET /healthz` and `POST /v1/sum`.
The sum route accepts `{"values": [1, 2.5]}` and returns `{"sum": 3.5}`.
Each template includes a Dockerfile, locked dependencies, tests, and an OpenAPI file.
All generated files use the `scaffold` class.
The generator runs no external tools.

Use `--no-openapi` to omit the OpenAPI file and its declaration.
Use `--dry-run` or `--diff` to preview the changes.
Run `spinup g client` after a change to an OpenAPI file.
Run `spinup dev` to start the application and its services.

This example creates both service types without a compiler or a container runtime:

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup g service ocr --lang rust --dry-run
...
$ test ! -e services/ocr
$ spinup g service ocr --lang rust
...
$ test -s services/ocr/Cargo.lock
$ test -s services/ocr/src/main.rs
$ grep '^dev_port = 8101$' spinup.toml
dev_port = 8101
$ spinup g service classify --lang python --port 9000 --no-openapi
...
$ test -s services/classify/uv.lock
$ test -s services/classify/tests/test_main.py
$ test ! -e services/classify/openapi.yaml
$ grep '^dev_port = 8102$' spinup.toml
dev_port = 8102
```

Read the generated README for the commands that run the service tests.
Keep the package name `service` to preserve the lockfiles.
