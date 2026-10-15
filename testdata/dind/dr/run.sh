#!/usr/bin/env bash
# The disaster-recovery drill (ADR 0011 §8). See docs/contributing/dr-drill.md.
#
# It proves that a destroyed VM can be replaced and that its database comes back from object
# storage inside a time budget (the RTO). It reuses the test VM of the DinD suite
# (testdata/dind/vm/, ADR 0009 §7) and stores the pgBackRest repository in SeaweedFS.
#
#   0. build the CLI and the VM image, and generate an API-only app with the backups module
#   1. start SeaweedFS (S3 over TLS), create the bucket, and write the PGBACKREST_* secrets
#   2. start VM 1, deploy, insert known rows, and run spinup backup now --type full
#   3. destroy VM 1 (the container and its volumes)                      <- the RTO clock starts
#   4. start VM 2 with a new host key, update the SSH config, push the secrets, deploy the
#      same release, and restore the database in place from the repository
#   5. check the rows and /readyz, and compare the time since step 3 with DR_RTO_BUDGET
#
# Every container, network, volume, and image that it creates has a name that starts with
# spinup-dr-. A trap removes them, also when a step fails.
#
# Needs: docker (with network access in the VM to pull images and build the Postgres image),
# go, git, ssh, ssh-keygen, sops, age-keygen, curl, and python3.
# DR_RTO_BUDGET is the RTO budget in seconds (default 900, 15 minutes).
# Set DR_KEEP=1 to keep the work directory and its logs. The trap still removes the containers.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
dind="$(cd "$here/.." && pwd)"
repo="$(cd "$here/../../.." && pwd)"

export GOTOOLCHAIN="${GOTOOLCHAIN:-local}"
export SPINUP_NO_UPDATE_CHECK=1
GO="${GO:-go}"

budget="${DR_RTO_BUDGET:-900}"
if ! [[ "$budget" =~ ^[1-9][0-9]*$ ]]; then
  echo "run.sh: DR_RTO_BUDGET must be a number of seconds, not \"$budget\"." >&2
  echo "  Try: DR_RTO_BUDGET=900 bash testdata/dind/dr/run.sh" >&2
  exit 1
fi

app=spinup-dr-app
run_id="$$"
net="spinup-dr-net-$run_id"
s3="spinup-dr-s3-$run_id"
# The S3 host name has a dot: the Docker DNS of the VM forwards only names with a dot to the
# Docker DNS of the host, so a container in the VM cannot resolve a single-label alias.
s3_host=s3.spinup-dr.test
bucket=spinup-dr-repo
vm1="spinup-dr-vm1-$run_id"
vm2="spinup-dr-vm2-$run_id"
vm_image="spinup-dr-vm:$run_id"
host_alias=spinup-dr-vm
vm=""

for tool in docker "$GO" git ssh ssh-keygen sops age-keygen curl python3; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "run.sh: $tool is not on PATH." >&2
    echo "  Try: install $tool, or run spinup tools install for sops and age" >&2
    exit 1
  fi
done
if ! docker info >/dev/null 2>&1; then
  echo "run.sh: the Docker daemon does not answer." >&2
  echo "  Try: start Docker, then run the drill again" >&2
  exit 1
fi

work="$(mktemp -d "${TMPDIR:-/tmp}/spinup-dr.XXXXXX")"
logs="$work/logs"
mkdir -p "$logs"

cleanup() {
  local status=$?
  set +e
  if [[ $status -ne 0 && -n "$vm" ]] && docker inspect "$vm" >/dev/null 2>&1; then
    echo "--- containers on $vm" >&2
    docker exec "$vm" docker ps -a --format '{{.Names}} {{.Status}}' >&2
    docker exec "$vm" docker logs "$app-postgres-1" >"$logs/postgres.log" 2>&1
  fi
  if [[ $status -ne 0 ]] && docker inspect "$s3" >/dev/null 2>&1; then
    docker logs "$s3" >"$logs/seaweedfs.log" 2>&1
  fi
  docker rm -f -v "$vm1" "$vm2" "$s3" >/dev/null 2>&1
  docker network rm "$net" >/dev/null 2>&1
  # Remove only the images of this run: the VM image and the release images in $work/images.
  local image
  for image in "$vm_image" $(cat "$work/images" 2>/dev/null); do
    case "$image" in
      spinup-dr-*) docker image rm -f "$image" >/dev/null 2>&1 ;;
    esac
  done
  if [[ $status -ne 0 ]]; then
    echo "run.sh: FAILED. Logs are in $logs" >&2
  elif [[ -z "${DR_KEEP:-}" ]]; then
    rm -rf "$work"
  else
    echo "run.sh: kept $work"
  fi
}
trap cleanup EXIT

now() { date +%s; }
step_name=""
step_start=0
step_end() {
  if [[ -n "$step_name" ]]; then
    printf '    took %ss\n' "$(($(now) - step_start))"
  fi
  step_name=""
}
step() {
  step_end
  step_name="$*"
  step_start="$(now)"
  printf '\n==> %s\n' "$*"
}
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

# pin <name> prints the image reference image:tag@digest of a pin in stackversions.yaml.
pin() {
  local versions="$repo/internal/stackversions/stackversions.yaml" line image tag digest
  line="$(grep -E "^[[:space:]]*$1:[[:space:]]*\{" "$versions" | head -n 1)"
  image="$(sed -nE 's/.*image:[[:space:]]*([^,[:space:]]+),.*/\1/p' <<<"$line")"
  tag="$(sed -nE 's/.*tag:[[:space:]]*"([^"]+)".*/\1/p' <<<"$line")"
  digest="$(sed -nE 's/.*digest:[[:space:]]*"([^"]+)".*/\1/p' <<<"$line")"
  if [[ -z "$image" || -z "$tag" || -z "$digest" ]]; then
    fail "cannot read the $1 pin from $versions"
  fi
  printf '%s:%s@%s\n' "$image" "$tag" "$digest"
}

# start_vm <container> starts a VM with new SSH host keys, waits for dockerd, and writes the
# SSH config for the host alias. HostKeyAlias makes known_hosts identify the VM by the alias,
# as for a real host name, so a replacement VM with a new key does not pass unnoticed.
start_vm() {
  vm=$1
  docker run -d --privileged --name "$vm" --network "$net" -p 127.0.0.1::22 -p "127.0.0.1:$http_port:80" \
    --entrypoint /bin/sh "$vm_image" \
    -c 'rm -f /etc/ssh/ssh_host_*; ssh-keygen -A >/dev/null; exec /usr/local/bin/spinup-dind-entrypoint.sh' >/dev/null
  ssh_port="$(docker port "$vm" 22/tcp | head -n 1 | sed 's/.*://')"
  echo "    $vm: ssh on 127.0.0.1:$ssh_port, http on 127.0.0.1:$http_port"
  for _ in $(seq 1 60); do
    if docker exec -u deploy "$vm" docker info >/dev/null 2>&1; then break; fi
    sleep 1
  done
  docker exec -u deploy "$vm" docker info >/dev/null 2>&1 ||
    fail "dockerd in $vm does not answer the deploy user" "Try: docker logs $vm"
  cat >"$work/ssh_config" <<EOF
Host $host_alias
  HostName 127.0.0.1
  Port $ssh_port
  HostKeyAlias $host_alias
  User deploy
  IdentityFile $work/id_ed25519
  IdentitiesOnly yes
  UserKnownHostsFile $work/known_hosts
  StrictHostKeyChecking accept-new
EOF
}

# wait_ssh waits until SSH to the current VM works.
wait_ssh() {
  for _ in $(seq 1 30); do
    if ssh -F "$work/ssh_config" -o BatchMode=yes -o ConnectTimeout=5 "$host_alias" true 2>/dev/null; then return 0; fi
    sleep 1
  done
  ssh -F "$work/ssh_config" -o BatchMode=yes "$host_alias" true || fail "SSH to $vm failed" "Try: docker logs $vm"
}

# host_key prints the ed25519 host key of the current VM.
host_key() { docker exec "$vm" cat /etc/ssh/ssh_host_ed25519_key.pub | awk '{print $2}'; }

# prepare_vm creates /opt/<app> the way server bootstrap would (see testdata/dind/run.sh).
prepare_vm() {
  docker exec -i "$vm" sh -s <<EOF
set -eu
for d in releases secrets backups data/caddy/data data/caddy/config caddy postgres; do
  install -d -o deploy -g deploy -m 0755 /opt/$app/\$d
done
chown deploy:deploy /opt/$app
chmod 0700 /opt/$app/secrets
EOF
  echo "    prepared /opt/$app on $vm"
}

# psql_vm <sql> runs SQL in the postgres container of the base project and prints the rows.
psql_vm() {
  docker exec "$vm" docker compose -p "$app" exec -T postgres \
    psql -U app -d app -X -q -t -A -v ON_ERROR_STOP=1 -c "$1"
}

# new_release builds the API image of HEAD and prints the release ID (as testdata/dind/run.sh).
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
  cp "$dind/fixtures/Dockerfile.api" "$work/img/Dockerfile"
  echo "$app-api:$rel" >>"$work/images"
  docker build -q --build-arg BASE="$base" -t "$app-api:$rel" "$work/img" >>"$logs/build-$sha.log" 2>&1 ||
    { show_log "build-$sha"; fail "docker build of release $rel failed"; }
  printf '%s\n' "$rel"
}

commit() { git -C "$work/$app" add -A && git -C "$work/$app" commit -q -m "$1"; }

# secret <name> <value> writes one secret of the staging environment.
secret() {
  printf '%s' "$2" >"$work/value"
  sp "secret-$1" secrets set --env staging "$1" <"$work/value" || { show_log "secret-$1"; fail "secrets set $1 failed"; }
  rm -f "$work/value"
}

# The query whose result must survive the disaster: the row count and a digest of the rows.
rows_query="SELECT count(*) || ' ' || md5(string_agg(id || ':' || note, ',' ORDER BY id)) FROM dr_drill"

# ---------------------------------------------------------------------------------------------
step "0. build the CLI, the VM image, and the app"
(cd "$repo" && "$GO" build -o "$work/spinup" ./cmd/spinup)
ssh-keygen -q -t ed25519 -N '' -C spinup-dr -f "$work/id_ed25519"
dind_ref="$(pin docker-dind)"
seaweed_ref="$(pin seaweedfs)"
mkdir -p "$work/vm"
sed "s#\[\[ image \"docker-dind\" \]\]#$dind_ref#" "$dind/vm/Dockerfile" >"$work/vm/Dockerfile"
cp "$dind/vm/sshd_config" "$dind/vm/entrypoint.sh" "$work/vm/"
chmod 0755 "$work/vm/entrypoint.sh"
echo "    VM FROM $dind_ref"
docker build -t "$vm_image" --build-arg DEPLOY_PUBKEY="$(cat "$work/id_ed25519.pub")" "$work/vm" \
  >"$logs/vm-build.log" 2>&1 || { show_log vm-build; fail "the VM image did not build"; }
# Both VMs publish HTTP on the same port, so public_url in spinup.toml stays the same.
http_port="$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')"
goarch="$(docker run --rm --entrypoint uname "$vm_image" -m)"
case "$goarch" in
  x86_64) goarch=amd64 ;;
  aarch64 | arm64) goarch=arm64 ;;
  *) fail "unsupported VM architecture $goarch" ;;
esac

export SPINUP_KEYS_DIR="$work/keys"
export SPINUP_SSH_CONFIG="$work/ssh_config"
(cd "$work" && SPINUP_KIT_REPLACE="$repo/kit" ./spinup new "$app" --api-only --module example.com/drapp \
  --modules ci,deploy,secrets,backups --no-git --non-interactive) >"$logs/new.log" 2>&1 ||
  { show_log new; fail "spinup new failed"; }
# The SSH port is in the SSH config, not in spinup.toml: a replacement VM changes only the
# SSH config, so the commit and the release stay the same.
cat >>"$work/$app/spinup.toml" <<EOF

[env.staging]
host = "$host_alias"
user = "deploy"
public_url = "http://localhost:$http_port"
transport = "ssh"
EOF
# A first build resolves the kit replace, so go.mod and go.sum do not change after the commit.
(cd "$work/$app/api" && GOWORK=off GOFLAGS=-mod=mod "$GO" build ./...) >"$logs/warm-build.log" 2>&1 ||
  { show_log warm-build; fail "the generated API does not build"; }
git -C "$work/$app" init -q
git -C "$work/$app" config user.email dr@example.com
git -C "$work/$app" config user.name "spinup dr"
sp secrets-init secrets init --env staging --no-recovery || { show_log secrets-init; fail "secrets init failed"; }
db_password="dr-pw-$run_id"
secret DATABASE_URL "postgres://app:$db_password@postgres:5432/app?sslmode=disable"
secret POSTGRES_PASSWORD "$db_password"

# ---------------------------------------------------------------------------------------------
step "1. start SeaweedFS (S3) and write the pgBackRest secrets"
docker network create "$net" >/dev/null
# A self-signed certificate for the S3 host name. pgBackRest speaks only HTTPS to S3, and it
# verifies the certificate against the CA file in the secrets.
docker run --rm --entrypoint /bin/sh "$vm_image" -c '
  openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=$1" -addext "subjectAltName=DNS:$1" \
    -keyout /tmp/key.pem -out /tmp/cert.pem >/dev/null 2>&1
  cat /tmp/key.pem /tmp/cert.pem' sh "$s3_host" >"$work/s3.pem" ||
  fail "the S3 certificate was not created"
sed -n '/BEGIN PRIVATE KEY/,/END PRIVATE KEY/p' "$work/s3.pem" >"$work/s3-key.pem"
sed -n '/BEGIN CERTIFICATE/,/END CERTIFICATE/p' "$work/s3.pem" >"$work/s3-cert.pem"
[[ -s "$work/s3-key.pem" && -s "$work/s3-cert.pem" ]] || fail "the S3 certificate or key is empty"
s3_key="spinupdr$run_id"
s3_secret="spinup-dr-secret-$run_id"
cat >"$work/s3.json" <<EOF
{"identities": [{"name": "spinup-dr", "credentials": [{"accessKey": "$s3_key", "secretKey": "$s3_secret"}],
  "actions": ["Admin", "Read", "List", "Tagging", "Write"]}]}
EOF
docker create --name "$s3" --network "$net" --network-alias "$s3_host" "$seaweed_ref" \
  server -dir=/data -ip=127.0.0.1 -ip.bind=0.0.0.0 -master.volumeSizeLimitMB=64 -volume.max=0 \
  -s3 -s3.port=8333 -s3.port.https=443 -s3.config=/etc/spinup-dr/s3.json \
  -s3.cert.file=/etc/spinup-dr/cert.pem -s3.key.file=/etc/spinup-dr/key.pem >/dev/null
mkdir -p "$work/s3conf"
cp "$work/s3.json" "$work/s3conf/s3.json"
cp "$work/s3-cert.pem" "$work/s3conf/cert.pem"
cp "$work/s3-key.pem" "$work/s3conf/key.pem"
docker cp "$work/s3conf/." "$s3:/etc/spinup-dr/" >/dev/null
docker start "$s3" >/dev/null
echo "    SeaweedFS $seaweed_ref as https://$s3_host"
ready=""
for _ in $(seq 1 60); do
  if echo "s3.bucket.create -name $bucket" | docker exec -i "$s3" weed shell -master=127.0.0.1:9333 >"$logs/bucket.log" 2>&1 &&
    echo "s3.bucket.list" | docker exec -i "$s3" weed shell -master=127.0.0.1:9333 2>/dev/null | grep -q "$bucket"; then
    ready=1
    break
  fi
  sleep 1
done
[[ -n "$ready" ]] || { show_log bucket; fail "the bucket $bucket was not created in SeaweedFS" "Try: docker logs $s3"; }
code=000
for _ in $(seq 1 30); do
  code="$(docker exec "$s3" curl -s -o /dev/null -w '%{http_code}' --cacert /etc/spinup-dr/cert.pem \
    --resolve "$s3_host:443:127.0.0.1" "https://$s3_host/" || true)"
  if [[ "$code" != 000 ]]; then break; fi
  sleep 1
done
[[ "$code" != 000 ]] || fail "SeaweedFS does not answer HTTPS on port 443" "Try: docker logs $s3"
echo "    bucket $bucket created, S3 answers HTTPS ($code without credentials)"
secret PGBACKREST_REPO1_S3_ENDPOINT "$s3_host"
secret PGBACKREST_REPO1_S3_BUCKET "$bucket"
secret PGBACKREST_REPO1_S3_REGION us-east-1
secret PGBACKREST_REPO1_S3_KEY "$s3_key"
secret PGBACKREST_REPO1_S3_KEY_SECRET "$s3_secret"
secret PGBACKREST_REPO1_CIPHER_PASS "spinup-dr-cipher-$run_id"
secret PGBACKREST_REPO1_S3_URI_STYLE path
secret PGBACKREST_REPO1_STORAGE_CA_FILE /run/secrets/DR_S3_CA
secret DR_S3_CA "$(cat "$work/s3-cert.pem")"
commit "initial app"

# ---------------------------------------------------------------------------------------------
step "2. VM 1: deploy, insert rows, and take a full backup"
start_vm "$vm1"
wait_ssh
key1="$(host_key)"
prepare_vm
rel="$(new_release)"
sp deploy-1 deploy --env staging --skip-build --tag "$rel" --yes || { show_log deploy-1 60; fail "the deploy to VM 1 failed"; }
curl -fsS -m 10 "http://localhost:$http_port/healthz" >/dev/null || fail "/healthz does not answer on VM 1"
psql_vm "CREATE TABLE dr_drill (id integer PRIMARY KEY, note text NOT NULL);
  INSERT INTO dr_drill SELECT g, 'dr-row-' || g || '-$run_id' FROM generate_series(1, 500) AS g;" >/dev/null ||
  fail "the rows were not inserted on VM 1"
want_rows="$(psql_vm "$rows_query")" || fail "the rows cannot be read on VM 1"
echo "    inserted rows: $want_rows (count, md5)"
sp backup-full backup now --env staging --type full || { show_log backup-full 60; fail "spinup backup now --type full failed"; }
sed 's/^/    | /' "$logs/backup-full.log" | tail -n 1
sp backup-list backup list --env staging || { show_log backup-list; fail "spinup backup list failed"; }
grep -qE '^[0-9]{8}-[0-9]{6}F ' "$logs/backup-list.log" || { show_log backup-list; fail "backup list shows no full backup"; }

# ---------------------------------------------------------------------------------------------
step "3. destroy VM 1"
rto_start="$(now)"
docker rm -f -v "$vm1" >/dev/null
if docker inspect "$vm1" >/dev/null 2>&1; then fail "VM 1 still exists"; fi
echo "    removed $vm1 and its volumes (the RTO clock starts)"

# ---------------------------------------------------------------------------------------------
step "4. VM 2: new host key, secrets push, deploy, restore"
start_vm "$vm2"
key2="$(host_key)"
[[ "$key1" != "$key2" ]] || fail "VM 2 has the host key of VM 1"
# known_hosts still holds the key of VM 1 for the alias. SSH must refuse VM 2 until the
# operator removes the old key, then it accepts the new one.
for _ in $(seq 1 30); do
  if docker exec "$vm" pgrep sshd >/dev/null 2>&1; then break; fi
  sleep 1
done
sleep 1
if ssh -F "$work/ssh_config" -o BatchMode=yes -o ConnectTimeout=5 "$host_alias" true 2>"$logs/hostkey.log"; then
  fail "SSH accepted the new host key of VM 2 with the old key in known_hosts"
fi
grep -qi "host identification has changed\|host key verification failed" "$logs/hostkey.log" ||
  { show_log hostkey; fail "SSH to VM 2 failed for a reason other than the changed host key"; }
ssh-keygen -R "$host_alias" -f "$work/known_hosts" >/dev/null 2>&1
echo "    SSH refused the changed host key; removed the old key of $host_alias from known_hosts"
wait_ssh
prepare_vm
sp secrets-push secrets push --env staging --yes || { show_log secrets-push; fail "secrets push to VM 2 failed"; }
sp deploy-2 deploy --env staging --skip-build --tag "$rel" --yes || { show_log deploy-2 60; fail "the deploy to VM 2 failed"; }
if psql_vm "SELECT 1 FROM dr_drill LIMIT 1" >/dev/null 2>&1; then
  fail "the new database on VM 2 already has the table dr_drill"
fi
echo "    VM 2 runs release $rel on an empty database"
# The new cluster has another system identifier than the stanza, so a safety backup of it
# cannot go into the same repository. It holds no data, so the drill skips it.
if ! sp restore backup restore --env staging --into in-place --i-know --yes --now --no-safety-backup; then
  show_log restore 80
  fail "spinup backup restore --into in-place failed on VM 2"
fi

# ---------------------------------------------------------------------------------------------
step "5. check the rows, /readyz, and the RTO"
got_rows="$(psql_vm "$rows_query")" || fail "the table dr_drill is missing after the restore"
[[ "$got_rows" == "$want_rows" ]] || fail "the restored rows differ" "want: $want_rows" "got:  $got_rows"
echo "    restored rows: $got_rows"
ready_code="$(curl -s -o /dev/null -m 10 -w '%{http_code}' "http://localhost:$http_port/readyz" || true)"
[[ "$ready_code" == 200 ]] || fail "/readyz answers $ready_code after the restore, want 200"
echo "    /readyz answers 200"
rto=$(($(now) - rto_start))
step_end
printf '\nRTO: %ss (%dm%02ds) from the destruction of VM 1 to a verified restore; budget %ss\n' \
  "$rto" $((rto / 60)) $((rto % 60)) "$budget"
if ((rto > budget)); then
  fail "the RTO of ${rto}s is over the budget of ${budget}s" "Try: read the step times above, or raise DR_RTO_BUDGET"
fi
printf 'run.sh: PASS (release %s, RTO %ss)\n' "$rel" "$rto"
