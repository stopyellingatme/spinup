#!/usr/bin/env bash
# Repeat the live test of `spinup deploy digital-ocean` on a real account (ADR 0018 decision 7).
#
# THIS SCRIPT COSTS MONEY. It creates a droplet and destroys it at the end.
# It is not part of make or CI. It runs only when SPINUP_DO_LIVE=1 is set.
#
# Usage:  SPINUP_DO_LIVE=1 scripts/do-live-test.sh <app-dir> <env> [provision flags...]
#   <app-dir>   a spinup project with the deploy pack and a clean git tree
#   <env>       the environment name to create, for example live-test
#   flags       extra flags for every provision run, for example: --ssh-key ~/.ssh/id_ed25519.pub
#
# Needs:  spinup (or SPINUP_BIN), jq, curl, openssl, a terminal (/dev/tty), and a stored sign-in
#         (run `spinup deploy digital-ocean login` first). The account should hold no other
#         droplets, firewalls, or tags, because the last step fails when anything else is left.
# Env:    SPINUP_DO_LIVE=1             required
#         SPINUP_BIN                   the spinup binary (default spinup)
#         SPINUP_CREDENTIALS_DIR       the directory of the stored sign-in
#         SPINUP_DO_DEPLOY_ARGS        extra flags for the deploy runs (word split)
#         SPINUP_DO_API                the API base (default https://api.digitalocean.com)
#         SPINUP_DO_WAIT_SECONDS       the wait for each long step (default 600)
# The script never prints a token value.
set -euo pipefail

die() {
  echo "error: $1" >&2
  echo "Try: $2" >&2
  exit 1
}

if [[ "${SPINUP_DO_LIVE:-}" != 1 ]]; then
  die "this script creates paid resources on a real DigitalOcean account" \
    "set SPINUP_DO_LIVE=1 to run it, and read docs/contributing/do-live-test.md first"
fi
if [[ $# -lt 2 ]]; then
  die "the app directory and the environment are missing" \
    "run SPINUP_DO_LIVE=1 scripts/do-live-test.sh <app-dir> <env> [provision flags...]"
fi

app_dir=$1
env_name=$2
shift 2
provision_flags=("$@")

spinup_bin="${SPINUP_BIN:-spinup}"
api="${SPINUP_DO_API:-https://api.digitalocean.com}"
api="${api%/}"
wait_seconds="${SPINUP_DO_WAIT_SECONDS:-600}"
deploy_args=()
if [[ -n "${SPINUP_DO_DEPLOY_ARGS:-}" ]]; then
  read -r -a deploy_args <<<"$SPINUP_DO_DEPLOY_ARGS"
fi
creds_dir="${SPINUP_CREDENTIALS_DIR:-$HOME/.config/spinup/credentials}"
token_file="$creds_dir/digitalocean.json"

for tool in jq curl openssl "$spinup_bin"; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required" "install $tool and run the script again"
done
[[ -d "$app_dir" ]] || die "the app directory $app_dir does not exist" "pass the directory of a spinup project"
[[ -f "$token_file" ]] || die "no stored sign-in at $token_file" "run: $spinup_bin deploy digital-ocean login"
{ : </dev/tty; } 2>/dev/null || die "there is no terminal for the confirmation" "run the script in a terminal"

work=$(mktemp -d)
created=0
destroyed=0
child=0

destroy_cmd=("$spinup_bin" -C "$app_dir" --env "$env_name" --confirm-env "$env_name" deploy digital-ocean destroy --i-know)

on_exit() {
  local status=$?
  if [[ "$child" -gt 0 ]]; then kill -9 "$child" 2>/dev/null || true; fi
  rm -rf "$work"
  if [[ "$status" -ne 0 && "$created" -eq 1 && "$destroyed" -eq 0 ]]; then
    echo >&2
    echo "The live test failed and resources can still exist on the account." >&2
    echo "Try: ${destroy_cmd[*]}" >&2
  fi
}
trap on_exit EXIT

step() { echo; echo "== $1"; }

# ask <question> reads "yes" from the terminal. Any other answer stops the script.
ask() {
  local answer
  printf '%s Type yes to continue: ' "$1" >/dev/tty
  read -r answer </dev/tty || answer=
  [[ "$answer" == yes ]] || die "stopped: the answer was not yes" "run the script again and type yes"
}

sp() { "$spinup_bin" -C "$app_dir" --env "$env_name" "$@"; }

# api_names <path> <jq filter> prints the names from one API list. The token stays in a header file.
api_names() {
  local hdr="$work/header"
  if [[ ! -f "$hdr" ]]; then
    (umask 077 && printf 'Authorization: Bearer %s\n' "$(jq -r .access_token "$token_file")" >"$hdr")
  fi
  curl -sS --fail --max-time 30 -H @"$hdr" "$api$1?per_page=200" | jq -r "$2"
}

# --- plan and first confirmation -----------------------------------------------------------------
step "Plan"
sp deploy digital-ocean --plan ${provision_flags[@]+"${provision_flags[@]}"}
ask "The next step creates a droplet and a firewall that cost money."

# --- 1. provision, kill -9 during the droplet wait -------------------------------------------------
step "1. Provision, then kill -9 during the droplet wait"
log1="$work/run1.log"
created=1
sp deploy digital-ocean --skip-deploy ${provision_flags[@]+"${provision_flags[@]}"} >"$log1" 2>&1 </dev/null &
child=$!
waited=0
until grep -q "Created the droplet" "$log1"; do
  if ! kill -0 "$child" 2>/dev/null; then
    cat "$log1" >&2
    die "the first run ended before it created the droplet" "read the output above and fix the cause"
  fi
  if [[ "$waited" -ge "$wait_seconds" ]]; then
    die "the first run did not create the droplet in $wait_seconds seconds" "read the output of the first run"
  fi
  sleep 1
  waited=$((waited + 1))
done
kill -9 "$child"
wait "$child" 2>/dev/null || true
child=0
echo "Killed the first run after: $(grep "Created the droplet" "$log1")"

# --- 2. run again: the droplet is reused -----------------------------------------------------------
step "2. Run again; the droplet must be reused"
sp deploy digital-ocean --skip-deploy ${provision_flags[@]+"${provision_flags[@]}"} 2>&1 </dev/null | tee "$work/run2.log"
grep -q "Using the droplet" "$work/run2.log" || die "the second run did not say 'Using the droplet'" \
  "read the output above, then run: ${destroy_cmd[*]}"

# --- 3. run again with the deploy: nothing is created ----------------------------------------------
step "3. Run again with the deploy; nothing may be created"
sp deploy digital-ocean ${provision_flags[@]+"${provision_flags[@]}"} ${deploy_args[@]+"${deploy_args[@]}"} 2>&1 </dev/null | tee "$work/run3.log"
if grep -qE "Created the |Added the SSH key|Deleting the droplet" "$work/run3.log"; then
  die "the third run created or deleted a resource" "read the output above, then run: ${destroy_cmd[*]}"
fi

# --- 4. force a token refresh ------------------------------------------------------------------------
step "4. Force a token refresh"
past="2000-01-01T00:00:00Z"
(umask 077 && jq --arg e "$past" '.expiry = $e' "$token_file" >"$work/token.json")
cat "$work/token.json" >"$token_file"
rm -f "$work/token.json" "$work/header"
sp deploy digital-ocean status >"$work/status.log" 2>&1 || {
  cat "$work/status.log" >&2
  die "status failed after the expiry moved into the past" "check that the stored sign-in has a refresh token"
}
new_expiry=$(jq -r .expiry "$token_file")
[[ "$new_expiry" > "$past" ]] || die "the token expiry did not change, so spinup did not refresh the token" \
  "run: $spinup_bin deploy digital-ocean login"
echo "The token refreshed. The new expiry is $new_expiry."

# --- 5. plain deploy with no DigitalOcean token ----------------------------------------------------
step "5. Plain deploy with no DigitalOcean token"
empty_creds="$work/empty-creds"
mkdir -p "$empty_creds"
env -u DIGITALOCEAN_ACCESS_TOKEN -u DIGITALOCEAN_TOKEN SPINUP_CREDENTIALS_DIR="$empty_creds" \
  "$spinup_bin" -C "$app_dir" --env "$env_name" deploy ${deploy_args[@]+"${deploy_args[@]}"} 2>&1 </dev/null ||
  die "the plain deploy failed" "read the output above, then run: ${destroy_cmd[*]}"

# --- 6. the public URL and its certificate -----------------------------------------------------------
step "6. Public URL and certificate"
public_url=$(awk -v tbl="[env.$env_name]" '
  /^\[/ { in_tbl = ($0 == tbl) }
  in_tbl && $1 == "public_url" { gsub(/"/, "", $3); print $3; exit }' "$app_dir/spinup.toml")
[[ "$public_url" == https://* ]] || die "the public URL '$public_url' is empty or not https" \
  "check public_url in the [env.$env_name] table of spinup.toml"
host=${public_url#https://}
host=${host%%/*}
waited=0
until curl -sS --max-time 20 -o /dev/null "$public_url/" 2>"$work/curl.log"; do
  if [[ "$waited" -ge "$wait_seconds" ]]; then
    cat "$work/curl.log" >&2
    die "curl could not reach $public_url with a valid certificate" "check the proxy logs, then run: ${destroy_cmd[*]}"
  fi
  sleep 5
  waited=$((waited + 5))
done
echo "curl verified the certificate of $host."
openssl s_client -connect "$host:443" -servername "$host" </dev/null 2>/dev/null |
  openssl x509 -noout -issuer -enddate

# --- 7. destroy --------------------------------------------------------------------------------------
step "7. Destroy"
ask "The next step deletes the droplet, the firewall, and the database of the environment $env_name."
"${destroy_cmd[@]}"
destroyed=1

# --- 8. what is left ---------------------------------------------------------------------------------
step "8. Resources left on the account (names only)"
rm -f "$work/header"
left=0
for item in "droplets|/v2/droplets|.droplets[].name" "firewalls|/v2/firewalls|.firewalls[].name" \
  "tags|/v2/tags|.tags[].name"; do
  IFS='|' read -r label path filter <<<"$item"
  names=$(api_names "$path" "$filter")
  echo "$label: ${names:-none}"
  if [[ -n "$names" ]]; then left=1; fi
done
echo "ssh keys: $(api_names /v2/account/keys '.ssh_keys[].name' | paste -sd, -)"
if [[ "$left" -ne 0 ]]; then
  die "destroy left resources on the account" "delete them in the DigitalOcean control panel"
fi
echo
echo "The live test passed. Only the SSH key is left."
