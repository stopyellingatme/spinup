#!/usr/bin/env bash
# Register the spinup OAuth client with DigitalOcean (ADR 0017 section 2).
#
# This script is for the maintainers. Run it once.
# It prints the client ID on stdout. Put the ID in digitalocean.DefaultClientID.
# It prints the registration access token on stderr only.
#
# Needs: curl, jq.
# Env:   SPINUP_DO_OAUTH_BASE (default https://cloud.digitalocean.com).
set -euo pipefail

for tool in curl jq; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "error: $tool is required" >&2
    echo "Try: install $tool and run the script again" >&2
    exit 1
  }
done

base="${SPINUP_DO_OAUTH_BASE:-https://cloud.digitalocean.com}"
base="${base%/}"

body=$(jq -n '{
  client_name: "spinup",
  client_uri: "https://github.com/stopyellingatme/spinup",
  redirect_uris: ["http://127.0.0.1/callback", "http://localhost/callback"],
  grant_types: ["authorization_code", "refresh_token"],
  response_types: ["code"],
  token_endpoint_auth_method: "none"
}')

if ! reply=$(curl -fsS -X POST "$base/v1/oauth/register" \
  -H 'Content-Type: application/json' -d "$body"); then
  echo "error: the registration request failed" >&2
  echo "Try: check the network and SPINUP_DO_OAUTH_BASE, then run the script again" >&2
  exit 1
fi

client_id=$(jq -er '.client_id' <<<"$reply") || {
  echo "error: the answer has no client_id" >&2
  echo "Try: check the service and run the script again" >&2
  exit 1
}
token=$(jq -er '.registration_access_token' <<<"$reply") || token=""

printf '%s\n' "$client_id"
if [ -n "$token" ]; then
  echo "registration_access_token: $token" >&2
fi
echo "Keep the registration access token in a password manager. Never commit it." >&2
