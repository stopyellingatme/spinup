# Vendored Scalar API reference

The main loop vendors this file during a pre-seed (CLAUDE.md, shared files). Units never edit it.
`kit/apidocs` embeds it and serves it with `Content-Encoding: gzip` (ADR 0015 §3).

| File | Source | Version | SHA-256 |
|---|---|---|---|
| scalar.js.gz | npm `@scalar/api-reference`, `dist/browser/standalone.js`, compressed with `gzip -9 -n` | 1.72.3 (2026-09-30, the newest release older than 14 days on 2026-10-16) | standalone.js: `f98a66f9d9613398fd6eb58be8f25594d7e8861e71ea11bca94d67ae9fa5567f`; scalar.js.gz: `556fdad6ec75c07549de334d21ac85b421f68ff27cb9c2450d6868e566da2bc5` |

The npm tarball is `scalar-api-reference-1.72.3.tgz`, with the integrity value
`sha512-RQjAUSrtwZ7ASG6KlKwRohEdvK0GfIvPmLyhX3xHD9cY3tmsilJcrcqBSJRKKG6FeiwEz23C1Q+pOdx05LT6pw==`.
The package license is MIT (Scalar, https://github.com/scalar/scalar).

The bundle has defaults that call external services: fonts from `fonts.scalar.com`, the request
proxy `proxy.scalar.com`, and telemetry. The boot script turns them off (`withDefaultFonts: false`, no
`proxyUrl`, `telemetry: false`). The page CSP (`connect-src 'self'`) blocks any other external call.

To update: pick the newest release older than 14 days, run `npm pack @scalar/api-reference@<v>`, extract
`package/dist/browser/standalone.js`, compress it with `gzip -9 -n`, and update this table.
