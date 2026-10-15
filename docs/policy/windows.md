# Windows support

The commands `new`, `generate`, `build`, and `upgrade --plan` run natively on Windows.
M1 provides `new` and `build` from this list.
Install Go for local API builds.
Install Docker when you build container images.

The commands `dev`, `deploy`, and `server` require WSL2.
These commands depend on Linux processes, filesystem behavior, and deployment tools.
Run these commands from a WSL2 terminal with the required tools available.

Generated applications contain `.gitattributes` with `* text=auto eol=lf`.
This rule keeps checked files on LF line endings across operating systems.
Linux scripts and container builds require these predictable line endings.
The lock hashes normalized LF content to prevent line ending differences from creating false changes.
