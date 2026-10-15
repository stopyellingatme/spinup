#!/usr/bin/env bash
# The Docker-in-Docker deploy suite (make dind). See docs/contributing/dind.md.
#
# It starts a privileged docker:dind container with sshd and a deploy user as the "VM",
# generates an API-only app with the local kit, and runs these steps against the VM:
#   1. server bootstrap (--plan and --print-cloud-init, see the note at step 1)
#   2. deploy, then /healthz and /version through the published port of the VM
#   3. a deploy whose /readyz fails: it exits non-zero, and the old release stays live
#   4. a good second release, then rollback to the first release
#   5. lock contention: a held lock makes a deploy exit 5, then deploy unlock --force
#   6. a deploy with a new secret: the secret file on the VM has mode 0600
#
# Every container, volume, and image that it creates has a name that starts with
# spinup-dind-. A trap removes them, also when a step fails.
#
# Needs: docker (with network access to build the VM and to pull images in it), go, git,
# ssh, ssh-keygen, sops, age-keygen, curl, and python3.
# Set DIND_WEB=1 to generate a full app (api and web). The web image build needs the network.
# Set DIND_KEEP=1 to keep the work directory and its logs. The trap still removes the VM.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../.." && pwd)"

export GOTOOLCHAIN="${GOTOOLCHAIN:-local}"
export SPINUP_NO_UPDATE_CHECK=1
GO="${GO:-go}"

app=spinup-dind-app
web="${DIND_WEB:-}"
run_id="$$"
vm="spinup-dind-vm-$run_id"
vm_image="spinup-dind-vm:$run_id"
host_alias=spinup-dind-vm

for tool in docker "$GO" git ssh ssh-keygen sops age-keygen curl python3; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "run.sh: $tool is not on PATH." >&2
    echo "  Try: install $tool, or run spinup tools install for sops and age" >&2
    exit 1
  fi
done
if ! docker info >/dev/null 2>&1; then
  echo "run.sh: the Docker daemon does not answer." >&2
  echo "  Try: start Docker, then run make dind again" >&2
  exit 1
fi

work="$(mktemp -d "${TMPDIR:-/tmp}/spinup-dind.XXXXXX")"
logs="$work/logs"
mkdir -p "$logs"
pids=()

cleanup() {
  local status=$?
  set +e
  for pid in ${pids[@]+"${pids[@]}"}; do
    kill "$pid" 2>/dev/null
    wait "$pid" 2>/dev/null
  done
  if [[ $status -ne 0 ]] && docker inspect "$vm" >/dev/null 2>&1; then
    echo "--- containers on the VM" >&2
    docker exec "$vm" docker ps -a --format '{{.Names}} {{.Status}}' >&2
    docker exec "$vm" sh -c 'cat /opt/'"$app"'/deploy.state 2>/dev/null' >"$logs/deploy.state" 2>&1
  fi
  docker rm -f -v "$vm" >/dev/null 2>&1
  # Remove only the images of this run: the VM image and the release images in $work/images.
  local image
  for image in "$vm_image" $(cat "$work/images" 2>/dev/null); do
    case "$image" in
      spinup-dind-*) docker image rm -f "$image" >/dev/null 2>&1 ;;
    esac
  done
  if [[ $status -ne 0 ]]; then
    echo "run.sh: FAILED. Logs are in $logs" >&2
  elif [[ -z "${DIND_KEEP:-}" ]]; then
    rm -rf "$work"
  else
    echo "run.sh: kept $work"
  fi
}
trap cleanup EXIT

step() { printf '\n==> %s\n' "$*"; }
fail() {
  echo "run.sh: $1" >&2
  shift
  for line in "$@"; do echo "  $line" >&2; done
  exit 1
}

# sp <log name> <spinup arguments...> runs spinup in the app and writes its output to a log.
# It returns the exit code of spinup and never stops the script by itself.
sp() {
  local name=$1
  shift
  local status=0
  (cd "$work/$app" && "$work/spinup" "$@") >"$logs/$name.log" 2>&1 || status=$?
  echo "    spinup $* => exit $status (log: $name.log)"
  return "$status"
}

show_log() { tail -n "${2:-40}" "$logs/$1.log" | sed 's/^/    | /' >&2; }

vm_sh() { docker exec -i "$vm" sh -s; }

# http_get <path> prints the body of a request through the public port of the VM.
http_get() { curl -fsS -m 10 "http://localhost:$http_port$1"; }

# api_get <path> prints the body of a request to the API. An API-only app serves the API on
# public_url. In a full app, Caddy sends / to web, so the request goes from the Caddy container
# to the API alias of the live color.
api_get() {
  if [[ -z "$web" ]]; then
    http_get "$1"
    return
  fi
  local project
  project="$(live_projects | head -n 1)"
  docker exec "$vm" docker exec "$app-caddy-1" wget -q -T 10 -O - "http://api-${project##*-}:8080$1"
}

expect_version() {
  local want=$1 body
  body="$(api_get /version)" || fail "/version does not answer" "Try: read $logs and the containers on the VM"
  if [[ "$body" != *"\"$want\""* ]]; then
    fail "/version reports the wrong release" "want: $want" "got:  $body"
  fi
  echo "    /version reports $want"
}

# The color projects that run an api container on the VM.
live_projects() {
  docker exec "$vm" docker ps --filter label=com.docker.compose.service=api \
    --format '{{.Label "com.docker.compose.project"}}' | sort -u
}

# new_release builds the API image of HEAD and prints the release ID.
# The API is compiled on the host and copied into the distroless base of deploy/Dockerfile.api.
new_release() {
  local sha rel base
  sha="$(git -C "$work/$app" rev-parse --short=12 HEAD)"
  rel="$(date -u +%Y%m%dT%H%M%SZ)-$sha"
  base="$(grep -E '^FROM ' "$work/$app/deploy/Dockerfile.api" | tail -n 1 | awk '{print $2}')"
  rm -rf "$work/img"
  mkdir -p "$work/img"
  (cd "$work/$app/api" && CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" GOWORK=off GOFLAGS=-mod=mod \
    "$GO" build -trimpath -ldflags "-s -w -X main.version=$rel" -o "$work/img/api" ./cmd/api) \
    >"$logs/build-$sha.log" 2>&1 || { show_log "build-$sha"; fail "go build of release $rel failed"; }
  cp "$here/fixtures/Dockerfile.api" "$work/img/Dockerfile"
  echo "$app-api:$rel" >>"$work/images"
  if [[ -n "$web" ]]; then
    echo "$app-web:$rel" >>"$work/images"
    docker tag "$app-web:dind-base" "$app-web:$rel"
  fi
  docker build -q --build-arg BASE="$base" -t "$app-api:$rel" "$work/img" >>"$logs/build-$sha.log" 2>&1 ||
    { show_log "build-$sha"; fail "docker build of release $rel failed"; }
  printf '%s\n' "$rel"
}

commit() { git -C "$work/$app" add -A && git -C "$work/$app" commit -q -m "$1"; }

# ---------------------------------------------------------------------------------------------
step "build the CLI"
(cd "$repo" && "$GO" build -o "$work/spinup" ./cmd/spinup)

step "build the VM image"
ssh-keygen -q -t ed25519 -N '' -C spinup-dind -f "$work/id_ed25519"
versions="$repo/internal/stackversions/stackversions.yaml"
dind_image="$(sed -nE 's/^[[:space:]]*docker-dind:.*image:[[:space:]]*([^,[:space:]]+),.*/\1/p' "$versions")"
dind_tag="$(sed -nE 's/^[[:space:]]*docker-dind:.*tag:[[:space:]]*"([^"]+)".*/\1/p' "$versions")"
dind_digest="$(sed -nE 's/^[[:space:]]*docker-dind:.*digest:[[:space:]]*"([^"]+)".*/\1/p' "$versions")"
if [[ -z "$dind_image" || -z "$dind_tag" || -z "$dind_digest" ]]; then
  fail "cannot read the docker-dind pin from $versions"
fi
dind_ref="$dind_image:$dind_tag@$dind_digest"
mkdir -p "$work/vm"
sed "s#\[\[ image \"docker-dind\" \]\]#$dind_ref#" "$here/vm/Dockerfile" >"$work/vm/Dockerfile"
cp "$here/vm/sshd_config" "$here/vm/entrypoint.sh" "$work/vm/"
chmod 0755 "$work/vm/entrypoint.sh"
echo "    FROM $dind_ref"
docker build -t "$vm_image" --build-arg DEPLOY_PUBKEY="$(cat "$work/id_ed25519.pub")" "$work/vm" \
  >"$logs/vm-build.log" 2>&1 || { show_log vm-build; fail "the VM image did not build"; }

step "start the VM"
docker run -d --privileged --name "$vm" -p 127.0.0.1::22 -p 127.0.0.1::80 "$vm_image" >/dev/null
ssh_port="$(docker port "$vm" 22/tcp | head -n 1 | sed 's/.*://')"
http_port="$(docker port "$vm" 80/tcp | head -n 1 | sed 's/.*://')"
echo "    ssh on 127.0.0.1:$ssh_port, http on 127.0.0.1:$http_port"
for _ in $(seq 1 60); do
  if docker exec -u deploy "$vm" docker info >/dev/null 2>&1; then break; fi
  sleep 1
done
docker exec -u deploy "$vm" docker info >/dev/null 2>&1 ||
  fail "dockerd in the VM does not answer the deploy user" "Try: docker logs $vm"
goarch="$(docker exec "$vm" uname -m)"
case "$goarch" in
  x86_64) goarch=amd64 ;;
  aarch64 | arm64) goarch=arm64 ;;
  *) fail "unsupported VM architecture $goarch" ;;
esac

cat >"$work/ssh_config" <<EOF
Host $host_alias
  HostName 127.0.0.1
  Port $ssh_port
  User deploy
  IdentityFile $work/id_ed25519
  IdentitiesOnly yes
  UserKnownHostsFile $work/known_hosts
  StrictHostKeyChecking accept-new
EOF
export SPINUP_SSH_CONFIG="$work/ssh_config"
for _ in $(seq 1 30); do
  if ssh -F "$work/ssh_config" -o BatchMode=yes -o ConnectTimeout=5 "$host_alias" true 2>/dev/null; then break; fi
  sleep 1
done
ssh -F "$work/ssh_config" -o BatchMode=yes "$host_alias" 'echo "    $(id -un) on $(cat /etc/alpine-release 2>/dev/null | sed "s/^/Alpine /"), Docker $(docker version --format "{{.Server.Version}}")"' ||
  fail "SSH to the VM failed" "Try: docker logs $vm"

step "generate the app"
export SPINUP_KEYS_DIR="$work/keys"
shape=(--api-only)
if [[ -n "$web" ]]; then shape=(); fi
(cd "$work" && SPINUP_KIT_REPLACE="$repo/kit" ./spinup new "$app" ${shape[@]+"${shape[@]}"} --module example.com/dindapp \
  --modules ci,deploy,secrets --no-git --non-interactive) >"$logs/new.log" 2>&1 ||
  { show_log new; fail "spinup new failed"; }
cat >>"$work/$app/spinup.toml" <<EOF

[env.staging]
host = "$host_alias"
user = "deploy"
port = $ssh_port
public_url = "http://localhost:$http_port"
transport = "ssh"
EOF
# A first build resolves the kit replace, so go.mod and go.sum do not change after the commit.
(cd "$work/$app/api" && GOWORK=off GOFLAGS=-mod=mod "$GO" build ./...) >"$logs/warm-build.log" 2>&1 ||
  { show_log warm-build; fail "the generated API does not build"; }
if [[ -n "$web" ]]; then
  # The web image does not depend on the kit or the release, so one build serves every release.
  echo "$app-web:dind-base" >>"$work/images"
  docker build -q -f "$work/$app/deploy/Dockerfile.web" --build-arg PUBLIC_ORIGIN="http://localhost:$http_port" \
    -t "$app-web:dind-base" "$work/$app/web" >"$logs/web-build.log" 2>&1 ||
    { show_log web-build; fail "the web image did not build"; }
fi
git -C "$work/$app" init -q
git -C "$work/$app" config user.email dind@example.com
git -C "$work/$app" config user.name "spinup dind"
sp secrets-init secrets init --env staging --no-recovery || { show_log secrets-init; fail "secrets init failed"; }
db_password="dind-pw-$run_id"
printf '%s' "postgres://app:$db_password@postgres:5432/app?sslmode=disable" >"$work/value"
sp secrets-db secrets set --env staging DATABASE_URL <"$work/value" || { show_log secrets-db; fail "secrets set failed"; }
printf '%s' "$db_password" >"$work/value"
sp secrets-pg secrets set --env staging POSTGRES_PASSWORD <"$work/value" || { show_log secrets-pg; fail "secrets set failed"; }
rm -f "$work/value"
commit "initial app"

# ---------------------------------------------------------------------------------------------
step "1. server bootstrap"
# server bootstrap supports only Debian and Ubuntu (systemd, apt, UFW). The VM is the Alpine-based
# docker:dind image, so the suite checks that bootstrap connects and refuses the OS, and that
# --print-cloud-init renders the key. Then it prepares the VM the way bootstrap would.
if sp bootstrap-plan server bootstrap --env staging --ssh-key "$work/id_ed25519.pub" --plan; then
  fail "server bootstrap --plan accepted an Alpine VM"
fi
grep -q 'unsupported OS "alpine"' "$logs/bootstrap-plan.log" ||
  { show_log bootstrap-plan; fail "server bootstrap --plan did not reach the OS check of the VM"; }
sp cloud-init server bootstrap --env staging --ssh-key "$work/id_ed25519.pub" --print-cloud-init ||
  { show_log cloud-init; fail "server bootstrap --print-cloud-init failed"; }
grep -qF "$(awk '{print $2}' "$work/id_ed25519.pub")" "$logs/cloud-init.log" ||
  fail "the cloud-init output does not contain the deploy key"
vm_sh <<EOF
set -eu
for d in releases secrets backups data/caddy/data data/caddy/config caddy postgres; do
  install -d -o deploy -g deploy -m 0755 /opt/$app/\$d
done
chown deploy:deploy /opt/$app
chmod 0700 /opt/$app/secrets
EOF
echo "    prepared /opt/$app on the VM (the suite's own setup)"

# ---------------------------------------------------------------------------------------------
step "2. deploy the first release"
rel1="$(new_release)"
sp deploy-1 deploy --env staging --skip-build --tag "$rel1" --yes ||
  { show_log deploy-1 60; fail "the first deploy failed"; }
http_get /healthz >/dev/null || fail "/healthz does not answer after the first deploy"
echo "    /healthz answers"
if [[ -n "$web" ]]; then
  http_get / >/dev/null || fail "the web app does not answer / after the first deploy"
  echo "    the web app answers /"
fi
expect_version "$rel1"

# ---------------------------------------------------------------------------------------------
step "3. a release whose /readyz fails must not go live"
git -C "$work/$app" apply "$here/fixtures/broken-readyz.patch"
commit "dind: break readiness"
broken="$(new_release)"
if sp deploy-broken deploy --env staging --skip-build --tag "$broken" --yes --health-timeout 20s; then
  fail "the deploy of a release with a failing /readyz succeeded"
fi
grep -q "did not become healthy" "$logs/deploy-broken.log" ||
  { show_log deploy-broken; fail "the broken deploy failed for another reason"; }
http_get /healthz >/dev/null || fail "/healthz does not answer after the broken deploy"
expect_version "$rel1"
projects="$(live_projects)"
if [[ "$(printf '%s\n' "$projects" | grep -c .)" -ne 1 ]]; then
  fail "the unhealthy color still runs" "api projects: ${projects//$'\n'/ }"
fi
echo "    only $projects runs an api container"

# ---------------------------------------------------------------------------------------------
step "4. deploy a good second release, then roll back to the first"
git -C "$work/$app" apply -R "$here/fixtures/broken-readyz.patch"
commit "dind: repair readiness"
rel2="$(new_release)"
sp deploy-2 deploy --env staging --skip-build --tag "$rel2" --yes ||
  { show_log deploy-2 60; fail "the second deploy failed"; }
expect_version "$rel2"
sp rollback rollback --env staging --yes || { show_log rollback 60; fail "rollback failed"; }
expect_version "$rel1"
sp releases releases list --env staging || { show_log releases; fail "releases list failed"; }
grep -qE "^\*[[:space:]]+$rel1" "$logs/releases.log" ||
  { show_log releases; fail "releases list does not mark $rel1 as current"; }
echo "    releases list marks $rel1 as current"

# ---------------------------------------------------------------------------------------------
step "5. lock contention"
# The holder process owns the lock with a fresh owner record for as long as it runs.
# $app expands locally on purpose. The escaped names expand on the VM.
# shellcheck disable=SC2087
ssh -F "$work/ssh_config" -o BatchMode=yes "$host_alias" sh -s >"$logs/lock-holder.log" 2>&1 <<EOF &
set -eu
d=/opt/$app/deploy.lock
mkdir "\$d"
printf '{"owner":"dind-holder","pid":%d,"host":"dind","acquired":"%s","ttl_seconds":1800,"release":"dind-lock-test"}' \$\$ "\$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"\$d/owner.json"
chmod 0600 "\$d/owner.json"
echo held
exec sleep 600
EOF
pids+=("$!")
for _ in $(seq 1 30); do
  if grep -q held "$logs/lock-holder.log" 2>/dev/null; then break; fi
  sleep 0.5
done
grep -q held "$logs/lock-holder.log" || { show_log lock-holder; fail "the lock holder did not take the lock"; }
status=0
sp deploy-locked deploy --env staging --skip-build --tag "$rel2" --yes || status=$?
if [[ $status -ne 5 ]]; then
  show_log deploy-locked
  fail "a deploy against a held lock exited $status, want 5"
fi
grep -q "dind-holder" "$logs/deploy-locked.log" || { show_log deploy-locked; fail "the lock error does not name the owner"; }
expect_version "$rel1"
kill "${pids[0]}" 2>/dev/null || true
wait "${pids[0]}" 2>/dev/null || true
pids=()
sp unlock deploy unlock --force --env staging || { show_log unlock; fail "deploy unlock --force failed"; }
if docker exec "$vm" test -e "/opt/$app/deploy.lock"; then
  fail "deploy unlock --force left the lock directory"
fi
echo "    deploy unlock --force removed the lock"

# ---------------------------------------------------------------------------------------------
step "6. the deploy pushes secrets with mode 0600"
# spinup has no separate secrets push command. The upload step of deploy pushes the secrets.
token="dind-token-$run_id"
printf '%s' "$token" >"$work/value"
sp secrets-token secrets set --env staging DIND_TOKEN <"$work/value" || { show_log secrets-token; fail "secrets set failed"; }
rm -f "$work/value"
commit "dind: add DIND_TOKEN"
rel3="$(new_release)"
sp deploy-3 deploy --env staging --skip-build --tag "$rel3" --yes ||
  { show_log deploy-3 60; fail "the deploy with a new secret failed"; }
expect_version "$rel3"
modes="$(docker exec "$vm" sh -c "cd /opt/$app/secrets && stat -c '%n %a %U' *")"
while IFS= read -r line; do echo "    $line"; done <<<"$modes"
for name in DATABASE_URL DIND_TOKEN POSTGRES_PASSWORD; do
  echo "$modes" | grep -qx "$name 600 deploy" || fail "the secret file $name does not have mode 0600 and owner deploy"
done
if [[ "$(docker exec "$vm" stat -c '%a' "/opt/$app/secrets")" != 700 ]]; then
  fail "the secrets directory does not have mode 0700"
fi
if [[ "$(ssh -F "$work/ssh_config" -o BatchMode=yes "$host_alias" cat "/opt/$app/secrets/DIND_TOKEN")" != "$token" ]]; then
  fail "the secret file DIND_TOKEN does not hold the value"
fi
leaks="$(docker exec "$vm" sh -c "grep -rlF '$token' /opt/$app/releases /opt/$app/deploy.state /opt/$app/releases.json 2>/dev/null || true")"
[[ -z "$leaks" ]] || fail "the secret value is in files outside /opt/$app/secrets" "$leaks"
echo "    the value is only in /opt/$app/secrets"

printf '\nrun.sh: PASS (releases %s, %s, %s)\n' "$rel1" "$rel2" "$rel3"
