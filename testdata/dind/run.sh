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
#   7. a sidecar service (ADR 0022): the service runs in the live color, the API reaches it through
#      SERVICE_ECHO_URL, it sees only its own secret, a second deploy switches colors, and rollback
#      restores the previous release with its service
#      The app also has the jobs module. The worker runs in the live color only, has SERVICE_ECHO_URL,
#      does not restart, and follows the color switch and the rollback.
#   8. deploy digital-ocean against the dofake binary, with the VM as the droplet: a provision run
#      with host key rotation, a second run that creates nothing and deploys a new release, and destroy
#
# Every container, volume, and image that it creates has a name that starts with
# spinup-dind-. A trap removes them, also when a step fails.
#
# Needs: docker (with network access to build the VM and to pull images in it), go, git,
# ssh, ssh-keygen, ssh-keyscan, sops, age-keygen, curl, and python3.
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
# The sidecar service of step 7. Before step 7, the app declares no service.
svc="echo"
svc_port=8090
svc_on=""
run_id="$$"
vm="spinup-dind-vm-$run_id"
vm_image="spinup-dind-vm:$run_id"
host_alias=spinup-dind-vm

for tool in docker "$GO" git ssh ssh-keygen ssh-keyscan sops age-keygen curl python3; do
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
  (cd "$work/$app/api" && CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" GOWORK=off GOFLAGS=-mod=mod \
    "$GO" build -trimpath -ldflags "-s -w -X main.version=$rel" -o "$work/img/worker" ./cmd/worker) \
    >>"$logs/build-$sha.log" 2>&1 || { show_log "build-$sha"; fail "go build of the worker of release $rel failed"; }
  cp "$here/fixtures/Dockerfile.api" "$work/img/Dockerfile"
  if [[ -f "$work/img/worker" ]]; then
    # The jobs module: the release image also holds the worker binary, as in deploy/Dockerfile.api.
    printf 'COPY worker /worker\n' >>"$work/img/Dockerfile"
  fi
  echo "$app-api:$rel" >>"$work/images"
  if [[ -n "$web" ]]; then
    echo "$app-web:$rel" >>"$work/images"
    docker tag "$app-web:dind-base" "$app-web:$rel"
  fi
  if [[ -n "$svc_on" ]]; then
    # The service source does not change between releases, so each release tags one image.
    echo "$app-$svc:$rel" >>"$work/images"
    docker tag "$app-$svc:dind-base" "$app-$svc:$rel"
  fi
  docker build -q --build-arg BASE="$base" -t "$app-api:$rel" "$work/img" >>"$logs/build-$sha.log" 2>&1 ||
    { show_log "build-$sha"; fail "docker build of release $rel failed"; }
  printf '%s\n' "$rel"
}

commit() { git -C "$work/$app" add -A && git -C "$work/$app" commit -q -m "$1"; }

# pin <name> prints the image ref image:tag@digest of a pin in stackversions.yaml.
pin() {
  local name=$1 image tag digest
  image="$(sed -nE "s/^[[:space:]]*$name:.*image:[[:space:]]*([^,[:space:]]+),.*/\1/p" "$versions")"
  tag="$(sed -nE "s/^[[:space:]]*$name:.*tag:[[:space:]]*\"([^\"]+)\".*/\1/p" "$versions")"
  digest="$(sed -nE "s/^[[:space:]]*$name:.*digest:[[:space:]]*\"([^\"]+)\".*/\1/p" "$versions")"
  if [[ -z "$image" || -z "$tag" || -z "$digest" ]]; then
    fail "cannot read the $name pin from $versions"
  fi
  printf '%s:%s@%s\n' "$image" "$tag" "$digest"
}

# vm_docker <arguments...> runs the docker CLI on the VM.
vm_docker() { docker exec "$vm" docker "$@"; }

# check_service <release> <greeting> checks the sidecar service of the live color, and sets
# live_color. Exactly one color runs api and the service. The service runs the image of the
# release, is healthy, and reports the greeting of spinup.toml. The API has SERVICE_ECHO_URL and
# reaches the service through it. The service sees only its own secret in /run/secrets.
check_service() {
  local rel=$1 greeting=$2 project svc_c api_c projects image health info url names
  project="$(live_projects)"
  if [[ "$(printf '%s\n' "$project" | grep -c .)" -ne 1 ]]; then
    fail "more than one color runs an api container" "api projects: ${project//$'\n'/ }"
  fi
  live_color="${project##*-}"
  projects="$(vm_docker ps --filter "label=com.docker.compose.service=$svc" \
    --format '{{.Label "com.docker.compose.project"}}' | sort -u)"
  [[ "$projects" == "$project" ]] ||
    fail "the $svc service does not run in the live color $live_color only" "$svc projects: ${projects//$'\n'/ }"
  svc_c="$(vm_docker ps --filter "label=com.docker.compose.project=$project" \
    --filter "label=com.docker.compose.service=$svc" --format '{{.Names}}')"
  [[ "$(printf '%s\n' "$svc_c" | grep -c .)" -eq 1 ]] ||
    fail "the live color $live_color does not run exactly one $svc container" "containers: ${svc_c//$'\n'/ }"
  image="$(vm_docker inspect -f '{{.Config.Image}}' "$svc_c")"
  [[ "$image" == "$app-$svc:$rel" ]] || fail "the $svc container runs the wrong image" "want: $app-$svc:$rel" "got:  $image"
  health="$(vm_docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$svc_c")"
  [[ "$health" == healthy ]] || fail "the $svc container is not healthy" "health: ${health:-none}"
  vm_docker exec "$svc_c" /service healthcheck ||
    fail "the healthcheck command of the image fails in $svc_c"
  info="$(vm_docker exec "$svc_c" /service get "http://127.0.0.1:$svc_port/info")" ||
    fail "the $svc service does not answer /info"
  [[ "$info" == *"\"greeting\":\"$greeting\""* ]] ||
    fail "the $svc service reports the wrong greeting" "want: $greeting" "got:  $info"
  echo "    $svc runs $rel in $live_color, healthy, greeting $greeting"

  api_c="$(vm_docker ps --filter "label=com.docker.compose.project=$project" \
    --filter label=com.docker.compose.service=api --format '{{.Names}}' | head -n 1)"
  url="$(vm_docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$api_c" | sed -n 's/^SERVICE_ECHO_URL=//p')"
  [[ "$url" == "http://$svc-$live_color:$svc_port" ]] ||
    fail "the api container has the wrong SERVICE_ECHO_URL" "want: http://$svc-$live_color:$svc_port" "got:  ${url:-unset}"
  # The API image is distroless and has no HTTP client. A container in the network namespace
  # of the API container calls the URL, so the request takes the network path of the API.
  info="$(vm_docker run --rm --network "container:$api_c" "$app-$svc:$rel" get "$url/healthz")" ||
    fail "the api container cannot reach $url"
  [[ "$info" == ok ]] || fail "$url/healthz answers the wrong body" "got: $info"
  echo "    $api_c has SERVICE_ECHO_URL=$url and reaches it"

  names="$(vm_docker exec "$svc_c" /service ls /run/secrets)" || fail "cannot list /run/secrets in $svc_c"
  [[ "$names" == ECHO_TOKEN ]] ||
    fail "the $svc service must see only its own secret in /run/secrets" "got: ${names//$'\n'/ }"
  [[ "$(vm_docker exec "$svc_c" /service cat /run/secrets/ECHO_TOKEN)" == "$echo_token" ]] ||
    fail "the $svc service cannot read the value of ECHO_TOKEN"
  echo "    $svc sees only /run/secrets/ECHO_TOKEN"
}

# check_worker <release> checks the jobs worker of the live color (check_service sets live_color
# first). Exactly one color runs the worker. The worker runs the image of the release and has
# SERVICE_ECHO_URL. It does not restart: RestartCount is 0, and StartedAt does not change.
check_worker() {
  local rel=$1 project projects worker_c image url state started1 started2 restarts
  project="$(live_projects)"
  projects="$(vm_docker ps --filter label=com.docker.compose.service=worker \
    --format '{{.Label "com.docker.compose.project"}}' | sort -u)"
  [[ "$projects" == "$project" ]] ||
    fail "the worker does not run in the live color $live_color only" "worker projects: ${projects//$'\n'/ }" "api projects: ${project//$'\n'/ }"
  worker_c="$(vm_docker ps --filter "label=com.docker.compose.project=$project" \
    --filter label=com.docker.compose.service=worker --format '{{.Names}}')"
  [[ "$(printf '%s\n' "$worker_c" | grep -c .)" -eq 1 ]] ||
    fail "the live color $live_color does not run exactly one worker container" "containers: ${worker_c//$'\n'/ }"
  image="$(vm_docker inspect -f '{{.Config.Image}}' "$worker_c")"
  [[ "$image" == "$app-api:$rel" ]] || fail "the worker container runs the wrong image" "want: $app-api:$rel" "got:  $image"
  url="$(vm_docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$worker_c" | sed -n 's/^SERVICE_ECHO_URL=//p')"
  [[ "$url" == "http://$svc-$live_color:$svc_port" ]] ||
    fail "the worker container has the wrong SERVICE_ECHO_URL" "want: http://$svc-$live_color:$svc_port" "got:  ${url:-unset}"
  started1="$(vm_docker inspect -f '{{.State.StartedAt}}' "$worker_c")"
  sleep 8
  state="$(vm_docker inspect -f '{{.State.Status}} {{.State.Restarting}}' "$worker_c")"
  restarts="$(vm_docker inspect -f '{{.RestartCount}}' "$worker_c")"
  started2="$(vm_docker inspect -f '{{.State.StartedAt}}' "$worker_c")"
  [[ "$state" == "running false" ]] || {
    vm_docker logs --tail 20 "$worker_c" >&2 || true
    fail "the worker container is not running" "state: $state"
  }
  [[ "$restarts" == 0 && "$started1" == "$started2" ]] || {
    vm_docker logs --tail 20 "$worker_c" >&2 || true
    fail "the worker container restarts" "RestartCount: $restarts" "StartedAt: $started1 then $started2"
  }
  echo "    $worker_c runs $rel in $live_color with SERVICE_ECHO_URL=$url and does not restart"
}

# ---------------------------------------------------------------------------------------------
step "build the CLI"
(cd "$repo" && "$GO" build -o "$work/spinup" ./cmd/spinup)

step "build the VM image"
ssh-keygen -q -t ed25519 -N '' -C spinup-dind -f "$work/id_ed25519"
versions="$repo/internal/stackversions/stackversions.yaml"
dind_ref="$(pin docker-dind)"
mkdir -p "$work/vm"
sed "s#\[\[ image \"docker-dind\" \]\]#$dind_ref#" "$here/vm/Dockerfile" >"$work/vm/Dockerfile"
cp "$here/vm/sshd_config" "$here/vm/entrypoint.sh" "$here/vm/cloud-init" "$here/vm/systemctl" "$work/vm/"
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
  --modules ci,deploy,secrets,jobs --no-git --non-interactive) >"$logs/new.log" 2>&1 ||
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
step "6. the deploy and secrets push write secrets with mode 0600"
# The upload step of deploy pushes the secrets; secrets push (below) does it without a deploy.
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

# secrets push writes changed values without a deploy.
token2="dind-token-2-$run_id"
printf '%s' "$token2" >"$work/value"
sp secrets-token2 secrets set --env staging DIND_TOKEN <"$work/value" || { show_log secrets-token2; fail "secrets set failed"; }
rm -f "$work/value"
sp secrets-push secrets push --env staging --yes || { show_log secrets-push; fail "secrets push failed"; }
if [[ "$(ssh -F "$work/ssh_config" -o BatchMode=yes "$host_alias" cat "/opt/$app/secrets/DIND_TOKEN")" != "$token2" ]]; then
  fail "secrets push did not write the new DIND_TOKEN value"
fi
[[ "$(docker exec "$vm" stat -c '%a' "/opt/$app/secrets/DIND_TOKEN")" == 600 ]] || fail "secrets push left DIND_TOKEN without mode 0600"
echo "    secrets push wrote the new value with mode 0600"

# ---------------------------------------------------------------------------------------------
step "7. a sidecar service: deploy, switch colors, and roll back"
# The service is a small Go server on the pinned golang and distroless-static images. Like the
# API, its image is built on the host, and deploy --skip-build finds it by the release ID.
svc_dir="$work/$app/services/$svc"
mkdir -p "$svc_dir"
cp "$here/fixtures/service/main.go" "$svc_dir/main.go"
golang_ref="$(pin golang)"
static_ref="$(pin distroless-static)"
sed -e "s#\[\[ image \"golang\" \]\]#$golang_ref#" -e "s#\[\[ image \"distroless-static\" \]\]#$static_ref#" \
  "$here/fixtures/service/Dockerfile" >"$svc_dir/Dockerfile"
cat >>"$work/$app/spinup.toml" <<EOF

[[services]]
name = "$svc"
port = $svc_port
dev_port = 8101
health = "/healthz"
healthcheck = ["/service", "healthcheck"]
secrets = ["ECHO_TOKEN"]
env = { GREETING = "one" }
EOF
echo_token="dind-echo-$run_id"
printf '%s' "$echo_token" >"$work/value"
sp secrets-echo secrets set --env staging ECHO_TOKEN <"$work/value" || { show_log secrets-echo; fail "secrets set failed"; }
rm -f "$work/value"
echo "$app-$svc:dind-base" >>"$work/images"
docker build -q -t "$app-$svc:dind-base" "$svc_dir" >"$logs/service-build.log" 2>&1 ||
  { show_log service-build; fail "the $svc service image did not build"; }
commit "dind: add the $svc service"
svc_on=1

rel4="$(new_release)"
sp deploy-svc-1 deploy --env staging --skip-build --tag "$rel4" --yes ||
  { show_log deploy-svc-1 60; fail "the first deploy with a service failed"; }
expect_version "$rel4"
check_service "$rel4" one
check_worker "$rel4"
svc_color1="$live_color"
release_services="$(docker exec "$vm" cat "/opt/$app/releases/$rel4/release.json")" ||
  fail "cannot read release.json of $rel4"
[[ "$release_services" == *"\"$app-$svc:$rel4\""* ]] ||
  fail "release.json of $rel4 does not record the $svc image"
docker exec "$vm" test -s "/opt/$app/releases/$rel4/compose.services.yml" ||
  fail "the release directory of $rel4 has no compose.services.yml"

# A second release changes the environment of the service and switches colors.
sed 's/^env = { GREETING = "one" }$/env = { GREETING = "two" }/' "$work/$app/spinup.toml" >"$work/spinup.toml.new"
mv "$work/spinup.toml.new" "$work/$app/spinup.toml"
grep -q 'GREETING = "two"' "$work/$app/spinup.toml" || fail "cannot change the greeting in spinup.toml"
commit "dind: change the $svc greeting"
rel5="$(new_release)"
sp deploy-svc-2 deploy --env staging --skip-build --tag "$rel5" --yes ||
  { show_log deploy-svc-2 60; fail "the second deploy with a service failed"; }
expect_version "$rel5"
check_service "$rel5" two
check_worker "$rel5"
[[ "$live_color" != "$svc_color1" ]] || fail "the second deploy did not switch colors" "color: $live_color"
echo "    the second deploy switched from $svc_color1 to $live_color"

# Rollback restores the previous release with the service and the environment of that release.
sp svc-rollback rollback --env staging --yes || { show_log svc-rollback 60; fail "rollback with a service failed"; }
expect_version "$rel4"
check_service "$rel4" one
check_worker "$rel4"
[[ "$live_color" == "$svc_color1" ]] || fail "rollback did not return to the color $svc_color1" "color: $live_color"
echo "    rollback restored $rel4 and its service in $live_color"

# ---------------------------------------------------------------------------------------------
step "8. spinup deploy digital-ocean on the VM as a droplet"
# The dofake binary serves the DigitalOcean API. On a droplet create it extracts the user data
# and runs on-create.sh, which installs the injected host key and the rotation script on the VM,
# as cloud-init does. The command must rotate the key, deploy, and do nothing on a second run.
mkdir -p "$work/bin" "$work/creds"
(cd "$repo" && "$GO" build -o "$work/bin/dofake" ./testdata/dind/dofake) >"$logs/dofake-build.log" 2>&1 ||
  { show_log dofake-build; fail "the dofake binary did not build"; }
do_token="dind-do-token-$run_id"
DIND_VM="$vm" DIND_WORK="$work" "$work/bin/dofake" -token "$do_token" -ipv4 127.0.0.1 \
  -on-create "$here/on-create.sh" >"$work/dofake.out" 2>"$logs/dofake.log" &
pids+=("$!")
do_url=""
for _ in $(seq 1 30); do
  do_url="$(head -n 1 "$work/dofake.out" 2>/dev/null || true)"
  if [[ -n "$do_url" ]]; then break; fi
  sleep 0.5
done
[[ -n "$do_url" ]] || { show_log dofake; fail "the dofake binary did not print its URL"; }
echo "    dofake on $do_url"

# The droplet address is 127.0.0.1 and the environment has no port, so ssh finds the port of
# the VM and the operator key in this block.
cat >>"$work/ssh_config" <<EOF

Host 127.0.0.1
  Port $ssh_port
  User deploy
  IdentityFile $work/id_ed25519
  IdentitiesOnly yes
EOF

droplet_count() {
  curl -fsS -m 10 "$do_url/debug/state" | python3 -c 'import json,sys; print(json.load(sys.stdin)["droplets"])'
}
# pinned_keys prints the ssh-ed25519 keys of [env.do] in spinup.toml, one for each line.
pinned_keys() {
  awk '/^\[env\.do\]/ {on=1; next} /^\[/ {on=0} on' "$work/$app/spinup.toml" |
    grep -oE 'ssh-ed25519 [A-Za-z0-9+/=]+' || true
}
if [[ "$(droplet_count)" != 0 ]]; then fail "the fake has a droplet before the run"; fi

# The environment "do" is a copy of staging. The postgres volume on the VM holds the staging
# password, so "do" gets the same database secrets. A clean tree is a precondition.
cp "$work/$app/deploy/environments/staging.toml" "$work/$app/deploy/environments/do.toml"
sp do-secrets-init secrets init --env "do" --no-recovery || { show_log do-secrets-init; fail "secrets init --env do failed"; }
printf '%s' "postgres://app:$db_password@postgres:5432/app?sslmode=disable" >"$work/value"
sp do-secrets-db secrets set --env "do" DATABASE_URL <"$work/value" || { show_log do-secrets-db; fail "secrets set failed"; }
printf '%s' "$db_password" >"$work/value"
sp do-secrets-pg secrets set --env "do" POSTGRES_PASSWORD <"$work/value" || { show_log do-secrets-pg; fail "secrets set failed"; }
printf '%s' "$echo_token" >"$work/value"
sp do-secrets-echo secrets set --env "do" ECHO_TOKEN <"$work/value" || { show_log do-secrets-echo; fail "secrets set failed"; }
rm -f "$work/value"
commit "dind: add the do environment"

export SPINUP_DO_API_BASE="$do_url" SPINUP_DO_OAUTH_BASE="$do_url"
export DIGITALOCEAN_ACCESS_TOKEN="$do_token" SPINUP_CREDENTIALS_DIR="$work/creds"
do_args=(deploy digital-ocean --env "do" --public-url "http://localhost:$http_port" --no-recovery --yes
  --ssh-key "$work/id_ed25519.pub")

# The first run provisions only. Its commit moves HEAD, and a release ID must end with the HEAD
# SHA, so the release images are built after it.
sp do-up "${do_args[@]}" --skip-deploy || { show_log do-up 60; fail "spinup deploy digital-ocean --skip-deploy failed"; }
if [[ "$(droplet_count)" != 1 ]]; then fail "the first run did not create exactly one droplet"; fi
[[ -z "$(git -C "$work/$app" status --porcelain)" ]] || fail "the first run left uncommitted changes"

# spinup.toml pins one host key, and it is the key that the VM serves now.
pinned="$(pinned_keys)"
if [[ "$(printf '%s\n' "$pinned" | grep -c .)" -ne 1 ]]; then
  fail "[env.do] must pin exactly one host key" "host_keys: ${pinned//$'\n'/ | }"
fi
vm_key="$(docker exec "$vm" cat /etc/ssh/ssh_host_ed25519_key.pub | awk '{print $1 " " $2}')"
injected="$(awk '{print $1 " " $2}' "$work/injected_host_key.pub")"
[[ "$pinned" == "$vm_key" ]] || fail "the pinned host key is not the key of the VM" "pinned: $pinned" "vm:     $vm_key"
[[ "$vm_key" != "$injected" ]] || fail "the host key was not rotated: the VM still serves the injected key"
served="$(ssh-keyscan -t ed25519 -p "$ssh_port" 127.0.0.1 2>/dev/null | grep -v '^#' | awk '{print $2 " " $3}')"
[[ "$served" == "$vm_key" ]] || fail "sshd does not serve the rotated host key" "served: $served" "file:   $vm_key"
echo "    host key rotated and pinned: ${vm_key:0:40}..."

# The second run finds the droplet by its tag, creates nothing, and deploys a new release.
rel6="$(new_release)"
sp do-up-2 "${do_args[@]}" --skip-build --tag "$rel6" || { show_log do-up-2 60; fail "the second spinup deploy digital-ocean failed"; }
if [[ "$(droplet_count)" != 1 ]]; then fail "the second run changed the number of droplets"; fi
expect_version "$rel6"
check_service "$rel6" two
check_worker "$rel6"
[[ "$(pinned_keys)" == "$vm_key" ]] || fail "the second run changed the pinned host key" "pinned: $(pinned_keys)"
echo "    the second run created nothing and deployed $rel6"

# destroy deletes the droplet from the fake. The VM stays.
CI=1 sp do-destroy deploy digital-ocean destroy --env "do" --i-know --yes ||
  { show_log do-destroy; fail "spinup deploy digital-ocean destroy failed"; }
if [[ "$(droplet_count)" != 0 ]]; then fail "destroy left the droplet in the fake"; fi
echo "    destroy removed the droplet"
unset SPINUP_DO_API_BASE SPINUP_DO_OAUTH_BASE DIGITALOCEAN_ACCESS_TOKEN SPINUP_CREDENTIALS_DIR

printf '\nrun.sh: PASS (releases %s, %s, %s, %s, %s, %s)\n' "$rel1" "$rel2" "$rel3" "$rel4" "$rel5" "$rel6"
