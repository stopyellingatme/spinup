---
title: Web bundle budget
parent: Build your app
nav_order: 15
---

# Web bundle budget

`spinup doctor` measures JavaScript and CSS in a completed web build.
It measures raw bytes and Brotli bytes.
The check skips projects with no web app.
The check also skips a web app before its first build.

<!-- The command needs a completed web build. -->
<!-- doctest: skip -->
```console
$ spinup doctor
ok	web-bundle	client bundle: 325000 raw, 82000 brotli
```

The built-in limits are 1,500,000 raw bytes and 400,000 Brotli bytes.
The check reports a warning above either limit.
It lists the three largest client files and gives a `Try:` line.

## Change the limits

Create `web/bundle-budget.json` when your app needs different limits.
Use integer byte counts.

```json
{
  "client_raw_bytes": 1500000,
  "client_br_bytes": 400000
}
```

The file can contain either key.
The built-in value applies when a key is absent.
Correct malformed JSON before you run `spinup doctor` again.
