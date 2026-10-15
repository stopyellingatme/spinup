#!/bin/sh
# Managed by spinup. The VM does not need the spinup binary.
set -eu
export LC_ALL=C
app='todo'
base="/opt/$app/backups"
metrics="$base/metrics/backup.prom"
retention='4'
backend=pgbackrest
if [ -f "$base/backend" ]; then backend=$(cat "$base/backend"); fi
case "$backend" in pgbackrest|pgdump) ;; *) echo 'Unknown backup backend' >&2; exit 1 ;; esac
kind=${1-}
case "$kind" in full|diff|verify) ;; *) echo 'Usage: run.sh full|diff|verify' >&2; exit 1 ;; esac
umask 077
mkdir -p "$base"
# The current release contains the base project's Compose files and environment.
cd "/opt/$app/current"
COMPOSE_FILE=compose.edge.yml
if [ -f compose.backups.yml ]; then COMPOSE_FILE="$COMPOSE_FILE:compose.backups.yml"; fi
export COMPOSE_FILE
compose() { docker compose -p "$app" exec -T postgres "$@"; }
pgbackrest() {
    compose sh -c 'for f in /run/secrets/PGBACKREST_*; do [ -f "$f" ] && export "${f##*/}=$(cat "$f")"; done; exec pgbackrest "$@"' pgbackrest "--stanza=$app" "$@"
}

# Replace the series in this update. Preserve all other series and metadata.
write_metrics() (
    mkdir -p "$base/metrics" || exit 1
    if ! chmod 0755 "$base/metrics"; then
        # Docker can create a missing bind source as root.
        sudo -n chown --reference="$base" "$base/metrics" || exit 1
        chmod 0755 "$base/metrics" || exit 1
    fi
    tmp=$(mktemp "$metrics.tmp.XXXXXX") || exit 1
    trap 'rm -f "$tmp" "$tmp.old" "$tmp.new"' 0
    trap 'exit 1' HUP INT TERM
    if [ -e "$metrics" ]; then cat "$metrics" > "$tmp.old" || exit 1; else : > "$tmp.old"; fi
    if [ "$kind" = verify ]; then
        awk '$1 != "backup_last_verify_timestamp_seconds" && $1 != "backup_last_verify_ok" && $1 != "backup_last_verify_rto_seconds"' "$tmp.old" > "$tmp.new" || exit 1
        printf 'backup_last_verify_timestamp_seconds %s\nbackup_last_verify_ok %s\nbackup_last_verify_rto_seconds %s\n' "$finished" "$ok" "$elapsed" >> "$tmp.new" || exit 1
    else
        awk -v label="{kind=\"$kind\"}" '$1 != "backup_last_success_timestamp_seconds" label && $1 != "backup_last_size_bytes" label' "$tmp.old" > "$tmp.new" || exit 1
        printf 'backup_last_success_timestamp_seconds{kind="%s"} %s\nbackup_last_size_bytes{kind="%s"} %s\n' "$kind" "$finished" "$kind" "$size" >> "$tmp.new" || exit 1
    fi
    sort "$tmp.new" > "$tmp" || exit 1
    chmod 0644 "$tmp" || exit 1
    mv -f "$tmp" "$metrics" || exit 1
)

started=$(date +%s)
ok=1
if [ "$kind" = verify ]; then
    if [ "$backend" = pgbackrest ]; then
        if ! pgbackrest verify; then ok=0; fi
    else
        newest=
        for dump in "$base"/pgdump/*.dump; do [ ! -f "$dump" ] || newest=$dump; done
        if [ -z "$newest" ]; then
            echo 'No dump exists to verify' >&2
            ok=0
        elif ! compose pg_restore --list < "$newest" > /dev/null; then
            ok=0
        fi
    fi
elif [ "$backend" = pgbackrest ]; then
    pgbackrest stanza-create
    pgbackrest "--type=$kind" backup
    # Bootstrap installs Python. No secret enters these numeric results.
    info=$(pgbackrest --output=json info)
    values=$(printf '%s' "$info" | python3 -c 'import json,sys; b=json.load(sys.stdin)[0]["backup"][-1]; print(b["type"], int(b["info"]["size"]))')
    set -- $values
    kind=$1
    size=$2
    case "$kind" in full|diff|incr) ;; *) echo 'Invalid backup kind' >&2; exit 1 ;; esac
else
    kind=full
    mkdir -p "$base/pgdump"
    label=$(date -u +%Y%m%dT%H%M%SZ)F
    dump_tmp=$(mktemp "$base/pgdump/.dump.XXXXXX")
    trap 'rm -f "$dump_tmp"' 0
    trap 'exit 1' HUP INT TERM
    compose pg_dump -U app -d app -Fc > "$dump_tmp"
    chmod 0600 "$dump_tmp"
    mv "$dump_tmp" "$base/pgdump/$label.dump"
    size=$(wc -c < "$base/pgdump/$label.dump" | tr -d ' ')
    # UTC labels sort from oldest to newest. Never include incomplete dumps.
    set -- "$base"/pgdump/*.dump
    while [ "$#" -gt "$retention" ]; do rm -f "$1"; shift; done
fi
finished=$(date +%s)
elapsed=$((finished - started))
if ! write_metrics; then echo 'warning: backup metrics could not be written' >&2; fi
[ "$ok" -eq 1 ]
