#!/bin/sh
# Fixed host collector. Configuration is data, never shell code.
set +x
set -eu
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH
LC_ALL=C
TZ=UTC
export LC_ALL TZ
unset CDPATH
umask 077
exec 3>&2 2>/dev/null
fail() {
    printf '%s\n' "Cannot collect console facts: $1." 'Try: run spinup deploy --resume for this environment.' >&3
    exit 1
}
case "$#:$*" in 0:|1:--if-old) ;; *) fail 'expected no arguments or --if-old' ;; esac
console=$(cd -P -- "$(dirname -- "$0")" && pwd) || fail 'cannot locate the installation'
root=$(dirname -- "$console")
uid=$(id -u)
gid=$(id -g)
case "$uid:$gid" in *[!0-9:]*|0:*|:*|*:) fail 'the deploy UID must be nonzero' ;; esac
[ ! -L "$console/sample.lock" ] || fail 'the sample lock is unsafe'
exec 9> "$console/sample.lock"
chmod 600 "$console/sample.lock"
lock_code=0
flock -n -E 75 9 || lock_code=$?
case "$lock_code" in 0) ;; 75) exit 0 ;; *) fail 'cannot acquire the sample lock' ;; esac
# Keep the opened configuration inode alive as this sample's generation marker.
# Replacing probe.conf cannot reuse its inode while descriptor 7 remains open.
lock_config() {
    [ ! -L "$console/config.lock" ] || fail 'the configuration lock is unsafe'
    exec 8> "$console/config.lock"
    chmod 600 "$console/config.lock"
    flock -x -w 3 8 || fail 'cannot acquire the configuration lock within 3 seconds'
}
lock_config
[ -f "$console/probe.conf" ] && [ ! -L "$console/probe.conf" ] || fail 'probe.conf is missing or unsafe'
exec 7< "$console/probe.conf"
# Validate bytes before shell read, which could otherwise discard embedded NULs.
python3 -I - "$console/probe.conf" <<'PY' || fail 'probe.conf is invalid'
import re, sys, urllib.parse
with open(sys.argv[1], 'rb') as f:
    b = f.read(8193)
if len(b) > 8192 or b'\0' in b or b'\r' in b or not b.endswith(b'\n'):
    sys.exit(1)
try:
    app, env, profile, backend, jobs, image, origin = b[:-1].decode('ascii').split('\n')
except ValueError:
    sys.exit(1)
if not re.fullmatch(r'[a-z][a-z0-9-]{0,62}', app) or not re.fullmatch(r'[a-z][a-z0-9-]*', env):
    sys.exit(1)
if profile not in ('absent', 'none', 'lite', 'full') or backend not in ('none', 'pgbackrest', 'pgdump', 'walg') or jobs not in ('true', 'false'):
    sys.exit(1)
# Accept Docker repository paths with an optional registry port, tag, or SHA256 digest.
component = r'[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*'
registry = r'(?:[a-z0-9]+(?:[.-][a-z0-9]+)*(?::[0-9]{1,5})?/)?'
reference = registry + component + r'(?:/' + component + r')*(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(?:@sha256:[0-9a-f]{64})?'
if len(image) > 512 or not re.fullmatch(reference, image):
    sys.exit(1)
try:
    u = urllib.parse.urlsplit(origin)
    port = u.port
    if u.scheme not in ('http', 'https') or not u.hostname or u.username is not None or u.password is not None or u.path not in ('', '/') or u.query or u.fragment:
        sys.exit(1)
    if not re.fullmatch(r'https?://[A-Za-z0-9.\-\[\]:]+/?', origin):
        sys.exit(1)
except ValueError:
    sys.exit(1)
PY
{
    IFS= read -r app
    IFS= read -r env
    IFS= read -r profile
    IFS= read -r backend
    IFS= read -r jobs
    IFS= read -r image
    IFS= read -r origin
    if IFS= read -r extra || [ -n "$extra" ]; then fail 'probe.conf has extra lines'; fi
} <&7
# Release the short lock before collecting any sources.
exec 8>&-
now=$(date -u +%s)
if [ "${1-}" = --if-old ] && [ -f "$console/data/facts.json" ] && [ ! -L "$console/data/facts.json" ]; then
    if timeout -k 1 3 python3 -I -c '
import datetime, json, sys
try:
    with open(sys.argv[1], "rb") as f:
        data = f.read(393217)
    if len(data) > 393216:
        sys.exit(1)
    doc = json.loads(data)
    if doc["version"] != 1 or [doc["app"], doc["env"], doc["profile"]] != sys.argv[3:]:
        sys.exit(1)
    at = datetime.datetime.fromisoformat(doc["observed_at"].replace("Z", "+00:00"))
    if at.tzinfo is None:
        sys.exit(1)
    age = int(sys.argv[2]) - at.timestamp()
    sys.exit(0 if 0 <= age <= 30 else 1)
except (KeyError, ValueError, TypeError, OSError, AttributeError):
    sys.exit(1)
' "$console/data/facts.json" "$now" "$app" "$env" "$profile"; then exit 0; fi
fi
observed=$(date -u +%Y-%m-%dT%H:%M:%SZ)
work=$(mktemp -d "$console/.probe.XXXXXXXX") || fail 'cannot create private staging'
chmod 700 "$work"
stage=$work/input
mkdir -m 700 "$stage"
pids=
published=
projector=
cleanup() {
    trap '' HUP INT TERM
    for child in $pids; do kill -TERM "$child" 2>/dev/null || :; done
    for child in $pids; do wait "$child" 2>/dev/null || :; done
    if [ -n "$projector" ]; then timeout -k 1 3 docker rm -f -- "$projector" > /dev/null || :; fi
    rm -rf -- "$work"
    if [ -n "$published" ]; then rm -f -- "$published"; fi
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

# Capture a bounded stream and its producer status. The timeout owns the whole
# pipeline's process group, including grandchildren. Never retain partial output.
capture() (
    name=$1 duration=$2 limit=$3
    shift 3
    running=
    trap '[ -z "$running" ] || kill -TERM "$running" 2>/dev/null; [ -z "$running" ] || wait "$running" 2>/dev/null; exit 1' HUP INT TERM
    timeout -k 1 "$duration" sh -c '
        status=$1; limit=$2; shift 2
        (set +e; "$@"; result=$?; printf "%s\n" "$result" > "$status") | head -c "$limit"
    ' capture "$work/$name.status" "$((limit + 1))" "$@" > "$work/$name.partial" &
    running=$!
    code=0
    wait "$running" || code=$?
    running=
    reason=
    if [ "$(wc -c < "$work/$name.partial")" -gt "$limit" ]; then reason=invalid
    elif [ "$code" = 124 ] || [ "$code" = 137 ]; then reason=timeout
    elif [ "$code" != 0 ] || [ ! -f "$work/$name.status" ]; then reason=source_down
    elif [ "$(cat "$work/$name.status")" = 42 ]; then reason=missing
    elif [ "$(cat "$work/$name.status")" = 43 ]; then reason=invalid
    elif [ "$name" = jobs.json ] && [ "$(cat "$work/$name.status")" = 3 ] && grep -Eq "^ERROR: *42P01" "$work/$name.partial"; then reason=missing
    elif [ "$(cat "$work/$name.status")" != 0 ]; then reason=source_down
    fi
    rm -f -- "$work/$name.status"
    if [ -n "$reason" ]; then
        rm -f -- "$work/$name.partial"
        printf '%s\n' "$reason" > "$stage/$name.err"
    else
        mv -- "$work/$name.partial" "$stage/$name"
    fi
)
wait_probes() {
    for child in $pids; do wait "$child" || fail 'a probe could not finish'; done
    pids=
}
# Each group starts at most four independent sources.
start() {
    capture "$@" &
    pids="$pids $!"
}
local_file='file=$1; fallback=$2; if [ ! -e "$file" ]; then printf "%s" "$fallback"; elif [ -f "$file" ]; then cat -- "$file"; else exit 43; fi'
start release.json 3 2097152 sh -c "$local_file" read "$root/current/release.json" 'null'
start releases.json 3 2097152 sh -c "$local_file" read "$root/releases.json" '[]'
start upstream.caddy 3 2097152 sh -c "$local_file" read "$root/caddy/upstream.caddy" ''
start deploy.state 3 2097152 python3 -I -c '
import os, stat, sys
try:
    if not stat.S_ISREG(os.stat(sys.argv[1]).st_mode):
        sys.exit(43)
    with open(sys.argv[1], "rb") as f:
        size = f.seek(0, os.SEEK_END)
        f.seek(max(0, size - 1048577))
        data = f.read(1048577)
    if size > 1048576:
        preceding, data = data[:1], data[1:]
        if preceding != b"\n":
            data = data.partition(b"\n")[2]
    sys.stdout.buffer.write(data[:data.rfind(b"\n") + 1])
except FileNotFoundError:
    pass
' "$root/deploy.state"
wait_probes
# Validate IDs before constructing paths. Do not follow release-directory symlinks.
start snapshot-ids 3 2097152 python3 -I -c '
import heapq, os, re, sys
root = sys.argv[1]
if os.path.isdir(root) and not os.path.islink(root):
    with os.scandir(root) as entries:
        ids = (e.name for e in entries if re.fullmatch(r"[0-9]{8}T[0-9]{6}Z-[0-9a-f]{12}(-r[1-9][0-9]*)?", e.name) and e.is_dir(follow_symlinks=False))
        for name in heapq.nlargest(20, ids):
            print(name)
' "$root/releases"
bounded_ids='set -f
ids() {
    (docker ps "$@"; code=$?; printf "%s\n" "$code" > "$work/ps.status") | head -c 2097153 > "$work/ps.ids"
    [ "$(wc -c < "$work/ps.ids")" -le 2097152 ] || exit 43
    [ "$(cat "$work/ps.status")" = 0 ] || exit 1
    cat "$work/ps.ids"
}
'
export work
containers=$bounded_ids'app=$1
format=$2
for project in "$app" "$app-blue" "$app-green" "$app-monitoring" "$app-console"; do
    ids=$(ids -a --no-trunc --quiet --filter "label=com.docker.compose.project=$project") || exit $?
    for id in $ids; do
        case "$id" in *[!0-9a-f]*|"") exit 43 ;; esac
        [ "${#id}" -eq 64 ] || exit 43
        docker inspect --type container --format "$format" "$id" || exit 1
    done
done'
format='{"project":{{json (index .Config.Labels "com.docker.compose.project")}},"service":{{json (index .Config.Labels "com.docker.compose.service")}},"name":{{json (slice .Name 1)}},"state":{{json .State.Status}},"health":{{if .State.Health}}{{json .State.Health.Status}}{{else}}null{{end}}}'
start containers.ndjson 3 2097152 sh -c "$containers" containers "$app" "$format"
start disk.txt 3 2097152 df -Pk "$root"
start memory.txt 3 2097152 cat /proc/meminfo
wait_probes
mkdir "$stage/snapshots" "$work/snapshots"
if [ -f "$stage/snapshot-ids" ]; then
    while IFS= read -r release; do
        # Repeat validation at the path boundary, even for this private list.
        printf '%s\n' "$release" | grep -Eq '^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{12}(-r[1-9][0-9]*)?$' || continue
        snapshot=$root/releases/$release/release.json
        if [ -f "$snapshot" ] && [ ! -L "$root/releases" ] && [ ! -L "$root/releases/$release" ] && [ ! -L "$snapshot" ]; then
            capture "snapshots/$release.json" 3 2097152 cat -- "$snapshot"
        fi
    done < "$stage/snapshot-ids"
fi
rm -f -- "$stage/snapshot-ids" "$stage/snapshot-ids.err"
# Only enabled database sources require a container lookup.
if [ "$jobs" = true ] || [ "$backend" = pgbackrest ] || [ "$backend" = walg ]; then
    capture postgres-id 3 2097152 sh -c "$bounded_ids"'
        ids=$(ids --no-trunc --quiet --filter "label=com.docker.compose.project=$1" --filter label=com.docker.compose.service=postgres --filter status=running) || exit $?
        set -- $ids
        [ "$#" -ne 0 ] || exit 42
        [ "$#" -eq 1 ] || exit 43
        case "$1" in *[!0-9a-f]*|"") exit 43 ;; esac
        [ "${#1}" -eq 64 ] || exit 43
        printf "%s\n" "$1"
    ' postgres "$app"
fi
postgres=
if [ -f "$stage/postgres-id" ]; then postgres=$(cat "$stage/postgres-id"); fi
pgoptions='PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=2000'
jobs_sql=$(cat <<'SQL'
SELECT COALESCE(json_agg(json_build_object('queue', queue, 'state', state, 'count', count)
ORDER BY queue COLLATE "C", state COLLATE "C"), '[]'::json)
FROM (SELECT queue, state::text, count(*) FROM public.river_job
GROUP BY queue, state ORDER BY queue COLLATE "C", state::text COLLATE "C"
LIMIT 513) AS queues;
SQL
)
wal_sql=$(cat <<'SQL'
SELECT json_build_object('archive_mode', current_setting('archive_mode'),
'archived_at', last_archived_time, 'failed_at', last_failed_time)
FROM pg_catalog.pg_stat_archiver;
SQL
)
database_probe() {
    name=$1 duration=$2
    shift 2
    if [ -z "$postgres" ]; then
        if [ -f "$stage/postgres-id.err" ]; then cat "$stage/postgres-id.err" > "$stage/$name.err"; else printf 'missing\n' > "$stage/$name.err"; fi
    else
        start "$name" "$duration" 2097152 "$@"
    fi
}
if [ "$jobs" = true ]; then
    database_probe jobs.json 3 sh -c 'printf "%s\n" "$3" | docker exec -i --env "$2" "$1" psql -X -w -U app -d app -A -t -v ON_ERROR_STOP=1 -v VERBOSITY=sqlstate 2>&1' jobs "$postgres" "$pgoptions" "$jobs_sql"
fi
case "$backend" in
pgbackrest|walg)
    database_probe wal.json 3 sh -c 'printf "%s\n" "$3" | docker exec -i --env "$2" "$1" psql -X -w -U app -d app -A -t -v ON_ERROR_STOP=1' wal "$postgres" "$pgoptions" "$wal_sql"
    if [ "$backend" = pgbackrest ]; then
        database_probe backup.json 8 docker exec "$postgres" sh -c 'for f in /run/secrets/PGBACKREST_*; do [ -f "$f" ] && export "${f##*/}=$(cat "$f")"; done; exec pgbackrest "$@"' pgbackrest "--stanza=$app" --output=json info
    else
        database_probe backup.json 8 docker exec "$postgres" sh -c 'set -eu; for f in /run/secrets/WALG_* /run/secrets/AWS_*; do [ -f "$f" ] || continue; value=$(cat "$f"); export "${f##*/}=$value"; done; : "${WALG_S3_PREFIX:?WALG_S3_PREFIX is required}" "${WALG_LIBSODIUM_KEY:?WALG_LIBSODIUM_KEY is required}"; exec wal-g "$@"' wal-g backup-list --json --detail
    fi
    ;;
pgdump)
    start backup.json 8 2097152 python3 -I -c '
import datetime, json, os, re, sys
rows = []
size = 2
try:
    with os.scandir(sys.argv[1]) as entries:
        for f in entries:
            if not re.fullmatch(r"[0-9]{8}T[0-9]{6}ZF\.dump", f.name) or not f.is_file(follow_symlinks=False):
                continue
            datetime.datetime.strptime(f.name[:16], "%Y%m%dT%H%M%SZ")
            s = f.stat(follow_symlinks=False)
            row = dict(label=f.name[:-5], finished_at=datetime.datetime.fromtimestamp(s.st_mtime, datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"), size_bytes=s.st_size)
            size += len(json.dumps(row)) + 2
            if size > 2097152:
                sys.exit(43)
            rows.append(row)
except FileNotFoundError:
    pass
json.dump(sorted(rows, key=lambda row: (row["finished_at"], row["label"])), sys.stdout)
' "$root/backups/pgdump"
    ;;
esac
wait_probes
rm -f -- "$stage/postgres-id" "$stage/postgres-id.err"
[ ! -L "$console/data" ] || fail 'the data directory is unsafe'
mkdir -p -- "$console/data"
chmod 755 "$console/data"
published=$(mktemp "$console/data/.facts.XXXXXXXX") || fail 'cannot create a sibling facts file'
# The decoder alone sees raw inputs. It has one read-only mount and no network.
projector=$app-console-project-${work##*/}
capture projected 3 393216 docker run --rm --name "$projector" --pull never --network none --read-only --cap-drop ALL --security-opt no-new-privileges:true --pids-limit 32 --cpus 0.25 --memory 48m --log-driver none --user "$uid:$gid" --env GOMEMLIMIT=24MiB --env "CONSOLE_APP=$app" --env "CONSOLE_ENV=$env" --env "CONSOLE_PROFILE=$profile" --env "CONSOLE_BACKEND=$backend" --env "CONSOLE_JOBS=$jobs" --env "CONSOLE_ORIGIN=$origin" --env "CONSOLE_OBSERVED_AT=$observed" --mount "type=bind,src=$stage,dst=/input,readonly" --entrypoint /console "$image" project < /dev/null
[ -f "$stage/projected" ] && [ -s "$stage/projected" ] || fail 'the projector failed or exceeded its limit'
cat "$stage/projected" > "$published"
chmod 644 "$published"
lock_config
# Discard results if installation replaced the configuration, even with equal bytes.
if ! python3 -I - "$console/probe.conf" <<'PYTHON'
import os, sys
try:
    sys.exit(0 if os.path.samestat(os.fstat(7), os.stat(sys.argv[1])) else 1)
except OSError:
    sys.exit(1)
PYTHON
then
    exec 8>&-
    exit 0
fi
if ! mv -f -- "$published" "$console/data/facts.json"; then
    exec 8>&-
    fail 'cannot replace facts.json'
fi
exec 8>&-
published=
