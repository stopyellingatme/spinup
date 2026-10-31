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
#         SPINUP_DO_VERIFY_CMD         an optional command (run with bash -c) after the service checks.
#                                      The script exports SPINUP_DO_HOST (the droplet IP) and
#                                      SPINUP_DO_SSH_KEY (the private key file). A failure stops the script.
#         SPINUP_DO_PUBLIC_COOKIE      the session cookie for authenticated public route checks.
#                                      The script never prints this value.
#         SPINUP_DO_LIVE_YES=1         answer yes to the create and destroy questions (no terminal
#                                      needed). Set it only after a person approved the cost.
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
if [[ "${SPINUP_DO_LIVE_YES:-}" != 1 ]]; then
  { : </dev/tty; } 2>/dev/null || die "there is no terminal for the confirmation" \
    "run the script in a terminal, or set SPINUP_DO_LIVE_YES=1 after a person approved the cost"
fi

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
# SPINUP_DO_LIVE_YES=1 answers yes.
ask() {
  local answer
  if [[ "${SPINUP_DO_LIVE_YES:-}" == 1 ]]; then
    echo "$1 yes (SPINUP_DO_LIVE_YES=1)"
    return
  fi
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
# Start the binary itself, not the sp function: $! of a function is a subshell, and kill -9 of
# the subshell leaves spinup running, so two runs would race on the droplet.
"$spinup_bin" -C "$app_dir" --env "$env_name" deploy digital-ocean --skip-deploy \
  ${provision_flags[@]+"${provision_flags[@]}"} >"$log1" 2>&1 </dev/null &
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
  in_tbl && $1 == "public_url" { gsub(/["\047]/, "", $3); print $3; exit }' "$app_dir/spinup.toml")
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

# --- 7. services on the droplet and the optional hook ----------------------------------------------
step "7. Services on the droplet"
# env_value <key> prints the value of a string key in the [env.<name>] table of spinup.toml.
env_value() {
  awk -v tbl="[env.$env_name]" -v key="$1" '
    /^\[/ { in_tbl = ($0 == tbl) }
    in_tbl && $1 == key { gsub(/["\047]/, "", $3); print $3; exit }' "$app_dir/spinup.toml"
}
droplet_ip=$(env_value host)
droplet_user=$(env_value user)
droplet_user=${droplet_user:-deploy}
[[ -n "$droplet_ip" ]] || die "spinup.toml has no host in the [env.$env_name] table" \
  "read the output above, then run: ${destroy_cmd[*]}"

# The private key is the public key of --ssh-key without the .pub suffix (default ~/.ssh/id_ed25519).
ssh_pub="$HOME/.ssh/id_ed25519.pub"
prev=
for flag in ${provision_flags[@]+"${provision_flags[@]}"}; do
  case "$prev" in --ssh-key) ssh_pub=$flag ;; esac
  case "$flag" in --ssh-key=*) ssh_pub=${flag#--ssh-key=} ;; esac
  prev=$flag
done
ssh_pub=${ssh_pub/#\~/$HOME}
ssh_key=${ssh_pub%.pub}

# Services are declared as [[services]] tables. Each line holds: name|port|health|public|auth.
service_lines=$(awk '
  /^\[\[services\]\]/ { if (n != "") print n "|" p "|" h "|" u "|" a; n = p = h = u = ""; a = "none"; ins = 1; next }
  /^\[/ { if (ins && n != "") print n "|" p "|" h "|" u "|" a; n = p = h = u = ""; a = "none"; ins = 0 }
  ins && $1 == "name" { gsub(/["\047]/, "", $3); n = $3 }
  ins && $1 == "port" { p = $3 }
  ins && $1 == "health" { gsub(/["\047]/, "", $3); h = $3 }
  ins && $1 == "public" { gsub(/["\047]/, "", $3); u = $3 }
  ins && $1 == "auth" { gsub(/["\047]/, "", $3); a = $3 }
  END { if (ins && n != "") print n "|" p "|" h "|" u "|" a }' "$app_dir/spinup.toml")
has_jobs=0
if [[ -f "$app_dir/api/cmd/worker/main.go" ]]; then has_jobs=1; fi
public_specs=()

# Pin the host key of spinup.toml in a known_hosts file, as spinup does. Without a pin, accept the
# key of the first connection.
known_hosts="$work/known_hosts"
: >"$known_hosts"
awk -v tbl="[env.$env_name]" '/^\[/ { in_tbl = ($0 == tbl) } in_tbl { print }' "$app_dir/spinup.toml" |
  grep -oE 'ssh-ed25519 [A-Za-z0-9+/=]+' | sed 's/^/* /' >"$known_hosts" || true
host_check=yes
if [[ ! -s "$known_hosts" ]]; then host_check=accept-new; fi
droplet_ssh() {
  ssh -i "$ssh_key" -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=20 \
    -o UserKnownHostsFile="$known_hosts" -o GlobalKnownHostsFile=/dev/null -o CheckHostIP=no \
    -o StrictHostKeyChecking="$host_check" "$droplet_user@$droplet_ip" "$@"
}

if [[ -n "$service_lines" || "$has_jobs" -eq 1 ]]; then
  [[ -f "$ssh_key" ]] || die "the private key $ssh_key does not exist" \
    "pass --ssh-key <public key> in the provision flags, then run: ${destroy_cmd[*]}"
  specs=()
  public_specs=()
  while IFS='|' read -r svc_name svc_port svc_health svc_public svc_auth; do
    [[ -n "$svc_name" ]] || continue
    specs+=("$svc_name:${svc_port:-8080}:${svc_health:-/healthz}")
    if [[ -n "$svc_public" ]]; then
      public_specs+=("$svc_name:${svc_public}:${svc_health:-/healthz}:${svc_auth:-none}")
    fi
  done <<<"$service_lines"
  echo "Services: ${specs[*]:-none}. Jobs worker: $has_jobs."
  # The remote script runs on the droplet. The services are not public, so Caddy calls them.
  droplet_ssh bash -s -- "$has_jobs" ${specs[@]+"${specs[@]}"} <<'REMOTE' || die "the service checks on the droplet failed" \
    "read the output above, then run: ${destroy_cmd[*]}"
set -eu
has_jobs=$1
shift
bad() { echo "service check failed: $*" >&2; exit 1; }
project=$(docker ps --filter label=com.docker.compose.service=api --format '{{.Label "com.docker.compose.project"}}' | sort -u)
[ "$(printf '%s\n' "$project" | grep -c .)" -eq 1 ] || bad "not exactly one color runs an api container: $project"
color=${project##*-}
caddy=$(docker ps --filter label=com.docker.compose.service=caddy --format '{{.Names}}' | head -n 1)
[ -n "$caddy" ] || bad "no caddy container runs"
api_c=$(docker ps --filter "label=com.docker.compose.project=$project" --filter label=com.docker.compose.service=api --format '{{.Names}}' | head -n 1)
[ -n "$api_c" ] || bad "no api container runs in $project"
env_of() { docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$1"; }
# running <container> fails when the container is not running, restarts, or is not healthy.
running() {
  state=$(docker inspect -f '{{.State.Status}} {{.State.Restarting}} {{.RestartCount}}' "$1")
  [ "$state" = "running false 0" ] || bad "$1 state is '$state'"
  hstat=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$1")
  [ -z "$hstat" ] || [ "$hstat" = healthy ] || bad "$1 is $hstat"
}
for spec in "$@"; do
  name=${spec%%:*}
  rest=${spec#*:}
  port=${rest%%:*}
  health=${rest#*:}
  upper=$(printf '%s' "$name" | tr '[:lower:]' '[:upper:]')
  projects=$(docker ps --filter "label=com.docker.compose.service=$name" --format '{{.Label "com.docker.compose.project"}}' | sort -u)
  [ "$projects" = "$project" ] || bad "$name runs in '$projects', not only in the live color $project"
  containers=$(docker ps --filter "label=com.docker.compose.project=$project" --filter "label=com.docker.compose.service=$name" --format '{{.Names}}')
  [ -n "$containers" ] || bad "no $name container runs in $project"
  for c in $containers; do
    running "$c"
    echo "$c runs and is healthy"
  done
  body=$(docker exec "$caddy" wget -q -T 10 -O - "http://$name-$color:$port$health") || bad "GET $health on $name-$color:$port through $caddy failed"
  echo "GET $health through $caddy answers: $(printf '%s' "$body" | head -c 80)"
  url=$(env_of "$api_c" | sed -n "s/^SERVICE_${upper}_URL=//p")
  [ "$url" = "http://$name-$color:$port" ] || bad "$api_c has SERVICE_${upper}_URL='${url:-unset}', want http://$name-$color:$port"
  echo "$api_c has SERVICE_${upper}_URL=$url"
  if [ "$has_jobs" = 1 ]; then
    worker_c=$(docker ps --filter "label=com.docker.compose.project=$project" --filter label=com.docker.compose.service=worker --format '{{.Names}}' | head -n 1)
    wurl=$(env_of "$worker_c" | sed -n "s/^SERVICE_${upper}_URL=//p")
    [ "$wurl" = "$url" ] || bad "$worker_c has SERVICE_${upper}_URL='${wurl:-unset}', want $url"
  fi
done
if [ "$has_jobs" = 1 ]; then
  workers=$(docker ps --filter label=com.docker.compose.service=worker --format '{{.Label "com.docker.compose.project"}}' | sort -u)
  [ "$workers" = "$project" ] || bad "the worker runs in '$workers', not only in the live color $project"
  worker_c=$(docker ps --filter "label=com.docker.compose.project=$project" --filter label=com.docker.compose.service=worker --format '{{.Names}}' | head -n 1)
  [ -n "$worker_c" ] || bad "no worker container runs in $project"
  running "$worker_c"
  started=$(docker inspect -f '{{.State.StartedAt}}' "$worker_c")
  sleep 10
  running "$worker_c"
  [ "$started" = "$(docker inspect -f '{{.State.StartedAt}}' "$worker_c")" ] || bad "$worker_c restarted during the wait"
  echo "$worker_c runs and does not restart"
fi
REMOTE
else
  echo "The app declares no [[services]] and has no jobs module. Skipped the service checks."
fi

if [[ ${#public_specs[@]} -eq 0 ]]; then
  echo "The app declares no public services. Skipped the public route checks."
else
  echo "Checking public routes: ${#public_specs[@]}."
  for public_spec in "${public_specs[@]}"; do
    svc_name=${public_spec%%:*}
    public_rest=${public_spec#*:}
    svc_public=${public_rest%%:*}
    public_rest=${public_rest#*:}
    svc_health=${public_rest%%:*}
    svc_auth=${public_rest#*:}
    public_route="https://$host$svc_public$svc_health"
    if [[ "$svc_auth" == session ]]; then
      anonymous_status=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 20 "$public_route") ||
        die "the anonymous public route check for $svc_name failed" "check the proxy logs, then run: ${destroy_cmd[*]}"
      [[ "$anonymous_status" == 401 ]] || die "the anonymous public route $public_route answered HTTP $anonymous_status, not HTTP 401" \
        "check the public route authentication, then run: ${destroy_cmd[*]}"
      if [[ -n "${SPINUP_DO_PUBLIC_COOKIE:-}" ]]; then
        cookie_status=$(curl -fsS -o /dev/null -w '%{http_code}' --max-time 20 \
          -H "Cookie: $SPINUP_DO_PUBLIC_COOKIE" "$public_route") ||
          die "the authenticated public route check for $svc_name failed" "check the proxy logs, then run: ${destroy_cmd[*]}"
        [[ "$cookie_status" == 200 ]] || die "the authenticated public route $public_route answered HTTP $cookie_status, not HTTP 200" \
          "check the public route authentication, then run: ${destroy_cmd[*]}"
        echo "GET $svc_public$svc_health on $svc_name answers HTTP 200 with the configured cookie."
      else
        echo "GET $svc_public$svc_health on $svc_name answers HTTP 401 without a cookie."
      fi
    else
      public_status=$(curl -fsS -o /dev/null -w '%{http_code}' --max-time 20 "$public_route") ||
        die "the public route check for $svc_name failed" "check the proxy logs, then run: ${destroy_cmd[*]}"
      [[ "$public_status" == 200 ]] || die "the public route $public_route answered HTTP $public_status, not HTTP 200" \
        "check the public route, then run: ${destroy_cmd[*]}"
      echo "GET $svc_public$svc_health on $svc_name answers HTTP 200."
    fi
  done
fi

if [[ -n "${SPINUP_DO_VERIFY_CMD:-}" ]]; then
  echo "Running SPINUP_DO_VERIFY_CMD with SPINUP_DO_HOST=$droplet_ip"
  SPINUP_DO_HOST="$droplet_ip" SPINUP_DO_SSH_KEY="$ssh_key" bash -c "$SPINUP_DO_VERIFY_CMD" ||
    die "SPINUP_DO_VERIFY_CMD failed" "read the output above, then run: ${destroy_cmd[*]}"
fi

# --- 8. destroy --------------------------------------------------------------------------------------
step "8. Destroy"
ask "The next step deletes the droplet, the firewall, and the database of the environment $env_name."
"${destroy_cmd[@]}"
destroyed=1

# --- 9. what is left ---------------------------------------------------------------------------------
step "9. Resources left on the account (names only)"
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
