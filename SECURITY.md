# Security policy

## Report a vulnerability

Do not open a public issue.
Use the private advisory form: https://github.com/stopyellingatme/spinup/security/advisories/new

Only the maintainers of spinup can read your report.

## What to include

- The version line that `spinup version` prints.
- The kit version in `api/go.mod` of your app.
- The command, the kit package, or the generated file.
- The steps to repeat the problem.
- The effect of the problem.

Do not send real secrets or user data.

## What to expect

The maintainer answers within seven days.
spinup has one maintainer, so a fix has no fixed date.
The maintainer agrees a publication date with you. The default is 90 days after your report.

## Supported versions

No release exists yet. Until the first release, fixes go to `main` only.
Pre-release kit tags get no fixes. Use the newest tag.

After the first release, security fixes go to the last two minor versions of the CLI and the kit.
The [support policy](https://stopyellingatme.github.io/spinup/policy/support.html) has the details.

## In scope

- The CLI: its commands, `install.sh`, `spinup self-update`, and the release checks. The release checks cover the checksums, the cosign bundles, and the signature of the template archive.
- The kit, with these parts:
  - Sign-in and sessions, roles, password reset, and OIDC
  - The auth rate limit, CSP and response headers, and request decoding
  - API keys, the MCP endpoint, and the billing webhook
  - Live feeds, the audit trail, and the console
- The generated files: Compose, Caddy, the CI workflows, the web defaults, the sidecar templates, and the PDF URL fetcher.
- Deploy: SSH, host key pins, the deploy lock, the transport of secrets, and the DigitalOcean token store.

## Out of scope

- A plugin is code that you trust. A malicious plugin is not a spinup vulnerability.
- Code that the app owner wrote or changed.
- A dependency flaw with no spinup cause. Report it to the upstream project.
- The folders `spikes/` and `examples/`, and the test fakes.
- Scanner output with no shown effect.
