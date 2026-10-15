#!/usr/bin/env bash
# Blue/green spike for ADR 0003 (unit M5.1).
#
# It measures failed requests while traffic moves to a new release:
#   1. blue/green: Compose projects bg-blue and bg-green behind one Caddy, and
#      a Caddy reload switches the upstream (critic resolution 9).
#   2. rollback: the same switch back to blue.
#   3. reload-xN (control): N Caddy reloads of an unchanged config, and no
#      other change. It shows how many failures the reload itself causes.
#   4. rolling: one Compose project, `up -d --scale app=4 --no-recreate`, then
#      stop the old containers (architecture.md §14 step 8). Caddy is not reloaded.
# A load container sends GET requests through Caddy during each switch.
#
# Needs: docker with compose v2, jq, perl, curl. The pinned images must be pulled.
# It creates only resources named bg-* and removes all of them on exit.
#
# Environment: BG_RUNS (default 3), BG_MIN_REQUESTS (default 1000),
# BG_WORKERS (load workers, default 8), BG_RELOADS (reloads in each control
# run, default 20, 0 skips the control), BG_REUSE=1 (the load keeps one
# keep-alive connection per worker; by default most requests open a new one),
# BG_PHASES (default "bluegreen reload rolling"; a subset skips phases).
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO=$(cd "$HERE/../.." && pwd)
SV="$REPO/internal/stackversions/stackversions.yaml"
RUNS=${BG_RUNS:-3}
MIN_REQ=${BG_MIN_REQUESTS:-1000}
WORKERS=${BG_WORKERS:-8}
RELOADS=${BG_RELOADS:-20}
PHASES=${BG_PHASES:-bluegreen reload rolling}
LOAD_ARGS=()
[ "${BG_REUSE:-0}" = 1 ] && LOAD_ARGS=(-reuse)

log() { printf '[bg %s] %s\n' "$(date +%H:%M:%S)" "$*" >&2; }
die() { log "error: $*"; exit 1; }
now_ms() { perl -MTime::HiRes=time -e 'printf "%d\n", time*1000'; }

for tool in docker jq perl curl; do
	command -v "$tool" >/dev/null || die "$tool is not on PATH"
done

# image_ref <key> prints name:tag@sha256:... for an images entry of stackversions.yaml.
image_ref() {
	local line
	line=$(grep -E "^  $1: " "$SV") || die "no image $1 in $SV"
	printf '%s' "$line" | sed -E 's/.*image: *([^ ,]+), *tag: *"([^"]+)", *digest: *"([^"]+)".*/\1:\2@\3/'
}
GOLANG_IMAGE=$(image_ref golang)
BASE_IMAGE=$(image_ref distroless-static)
CADDY_IMAGE=$(image_ref caddy)

WORK=$(mktemp -d "${TMPDIR:-/tmp}/bg-spike.XXXXXX")
mkdir -p "$WORK/caddy"
cp "$HERE/compose.yml" "$WORK/compose.yml"
cp "$HERE/Caddyfile" "$WORK/caddy/Caddyfile"

# compose <project> <tag> <alias> <args...>
compose() {
	local project=$1 tag=$2 alias=$3
	shift 3
	BG_TAG=$tag BG_ALIAS=$alias docker compose -p "$project" -f "$WORK/compose.yml" "$@"
}

remove_resources() {
	docker rm -f bg-load >/dev/null 2>&1 || true
	local p
	for p in bg-blue bg-green bg-roll; do
		compose "$p" blue x down -v --remove-orphans -t 2 >/dev/null 2>&1 || true
	done
	docker rm -fv bg-caddy >/dev/null 2>&1 || true
	docker network rm bg-net >/dev/null 2>&1 || true
}

cleanup() {
	local rc=$?
	trap - EXIT INT TERM
	log "cleanup"
	remove_resources
	docker rmi bg-app:blue bg-app:green >/dev/null 2>&1 || true
	rm -rf "$WORK"
	exit "$rc"
}
trap cleanup EXIT INT TERM

# write_upstream <alias> points Caddy at one network alias. The rename is atomic.
write_upstream() {
	cat >"$WORK/caddy/.upstream.tmp" <<EOF
reverse_proxy {
	dynamic a {
		name $1
		port 8080
		refresh 1s
	}
	lb_policy round_robin
	lb_try_duration 5s
	lb_try_interval 50ms
	transport http {
		dial_timeout 250ms
	}
}
EOF
	mv "$WORK/caddy/.upstream.tmp" "$WORK/caddy/upstream.caddy"
}

caddy_reload() {
	docker exec bg-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null 2>&1 \
		|| die "caddy reload failed"
}

# check_health <container ids...>: every container must pass /healthz.
check_health() {
	local id
	for id in "$@"; do
		docker exec "$id" /app -health || die "container $id fails /healthz"
	done
}

# ips_of <container ids...> prints the bg-net IP of each container.
ips_of() {
	local id
	for id in "$@"; do
		docker inspect -f '{{(index .NetworkSettings.Networks "bg-net").IPAddress}}' "$id"
	done
}

# drain_wait <ips...> waits until Caddy has no in-flight request to these IPs.
# It always waits at least 1 s and at most 10 s. It prints the wait in ms.
DRAIN_MIN_MS=1000
DRAIN_MAX_MS=10000
drain_wait() {
	local start t n ips
	start=$(now_ms)
	ips=$(printf '%s\n' "$@" | jq -R . | jq -sc .)
	while :; do
		t=$(now_ms)
		n=$(docker exec bg-caddy wget -qO- http://127.0.0.1:2019/reverse_proxy/upstreams 2>/dev/null \
			| jq --argjson ips "$ips" '[.[] | select((.address | split(":")[0]) as $a | $ips | index($a)) | .num_requests] | add // 0' \
			|| echo 0)
		if [ $((t - start)) -ge $DRAIN_MIN_MS ] && [ "$n" -eq 0 ]; then break; fi
		if [ $((t - start)) -ge $DRAIN_MAX_MS ]; then log "drain timeout with $n in-flight"; break; fi
		sleep 0.2
	done
	echo $(($(now_ms) - start))
}

live_color() { curl -fsS "http://127.0.0.1:$PORT/" | cut -d' ' -f1; }

# Load control. load_start runs the load container. load_window_start and
# load_window_end mark the switch window. load_stop prints the RESULT line.
load_start() {
	docker run -d --name bg-load --network bg-net --entrypoint /load \
		--read-only --cap-drop ALL bg-app:blue -url http://bg-caddy:8080/ -c "$WORKERS" \
		${LOAD_ARGS[@]+"${LOAD_ARGS[@]}"} >/dev/null
	sleep 1
}
load_window_start() { docker kill -s USR1 bg-load >/dev/null; }
load_window_end() { docker kill -s USR2 bg-load >/dev/null; }
load_stop() {
	sleep 1
	docker kill -s TERM bg-load >/dev/null
	docker wait bg-load >/dev/null
	local fails
	fails=$(docker logs bg-load 2>&1 | grep -c '^FAIL' || true)
	if [ "$fails" -gt 0 ]; then
		docker logs bg-load 2>&1 | grep '^FAIL' | head -5 | sed 's/^/    /' >&2
	fi
	docker logs bg-load 2>/dev/null | grep '^RESULT'
	docker rm bg-load >/dev/null
}

# field <name> <RESULT line>
field() { printf '%s\n' "$2" | tr ' ' '\n' | sed -n "s/^$1=//p"; }

# Result rows: strategy|run|from|to|win_req|total_req|failures|slow|max_ms|switch_ms|drain_ms
ROWS=()

# record <strategy> <run> <from> <to> <switch_ms> <drain_ms> <RESULT line>
record() {
	local s=$1 run=$2 from=$3 to=$4 sw=$5 dr=$6 r=$7
	local win total fail slow maxms other pre_wrong post_wrong post_right
	win=$(field win_total "$r")
	total=$(field total "$r")
	fail=$(($(field non200 "$r") + $(field connerr "$r")))
	slow=$(field slow "$r")
	maxms=$(field max_ms "$r")
	if [ "$from" = blue ]; then other=green; else other=blue; fi
	pre_wrong=0
	[ "$from" != "$to" ] && pre_wrong=$(field "pre_$other" "$r")
	if [ "$to" = blue ]; then other=green; else other=blue; fi
	post_wrong=$(field "post_$other" "$r")
	post_right=$(field "post_$to" "$r")
	log "$s run $run $from->$to: window=$win total=$total failures=$fail slow=$slow max=${maxms}ms switch=${sw}ms drain=${dr}ms" \
		"(window: blue=$(field win_blue "$r") green=$(field win_green "$r"); after: blue=$(field post_blue "$r") green=$(field post_green "$r"))"
	[ "$win" -ge "$MIN_REQ" ] || die "only $win requests in the switch window (need $MIN_REQ); raise BG_WORKERS"
	[ "$pre_wrong" -eq 0 ] || die "$pre_wrong requests before the switch were not answered by $from"
	[ "$post_wrong" -eq 0 ] || die "$post_wrong requests after the switch were not answered by $to"
	[ "$post_right" -gt 0 ] || log "warning: no request after the switch was answered by $to"
	ROWS+=("$s|$run|$from|$to|$win|$total|$fail|$slow|$maxms|$sw|$dr")
}

# bg_switch <from> <to> moves traffic from project bg-<from> to bg-<to>.
# It prints "switch_ms drain_ms".
bg_switch() {
	local from=$1 to=$2 t0 ids old drain
	t0=$(now_ms)
	compose "bg-$to" "$to" "app-$to" up -d --wait --scale app=2 >/dev/null 2>&1 \
		|| die "bg-$to did not become healthy"
	# shellcheck disable=SC2207
	ids=($(compose "bg-$to" "$to" "app-$to" ps -q app))
	[ "${#ids[@]}" -eq 2 ] || die "bg-$to has ${#ids[@]} replicas, expected 2"
	check_health "${ids[@]}"
	# shellcheck disable=SC2207,SC2046
	old=($(ips_of $(compose "bg-$from" "$from" "app-$from" ps -q app)))
	write_upstream "app-$to"
	caddy_reload
	drain=$(drain_wait "${old[@]}")
	compose "bg-$from" "$from" "app-$from" down -t 15 >/dev/null 2>&1
	echo "$(($(now_ms) - t0)) $drain"
}

# roll <to> replaces the replicas of project bg-roll with image bg-app:<to>.
# It prints "switch_ms drain_ms" (drain_ms is the stop time of the old containers).
roll() {
	local to=$1 t0 t1 old all new id keep
	t0=$(now_ms)
	# shellcheck disable=SC2207
	old=($(compose bg-roll "$to" app-roll ps -q app))
	compose bg-roll "$to" app-roll up -d --wait --no-recreate --scale app=4 >/dev/null 2>&1 \
		|| die "bg-roll scale-up did not become healthy"
	# shellcheck disable=SC2207
	all=($(compose bg-roll "$to" app-roll ps -q app))
	new=()
	for id in "${all[@]}"; do
		keep=1
		for o in "${old[@]}"; do [ "$id" = "$o" ] && keep=0; done
		[ $keep -eq 1 ] && new+=("$id")
	done
	[ "${#new[@]}" -eq 2 ] || die "bg-roll has ${#new[@]} new replicas, expected 2"
	check_health "${new[@]}"
	# Give Caddy one DNS refresh (1 s) to see the new replicas before the old ones go.
	sleep 1
	t1=$(now_ms)
	docker stop -t 15 "${old[@]}" >/dev/null
	docker rm "${old[@]}" >/dev/null
	echo "$(($(now_ms) - t0)) $(($(now_ms) - t1))"
}

# ---------------------------------------------------------------------------

log "images: $GOLANG_IMAGE | $BASE_IMAGE | $CADDY_IMAGE"
remove_resources

for c in blue green; do
	log "build bg-app:$c"
	docker build -q --pull=false \
		--build-arg GOLANG_IMAGE="$GOLANG_IMAGE" --build-arg BASE_IMAGE="$BASE_IMAGE" \
		--build-arg COLOR="$c" -t "bg-app:$c" "$HERE/app" >/dev/null
done

docker network create bg-net >/dev/null
log "start bg-blue and Caddy"
compose bg-blue blue app-blue up -d --wait --scale app=2 >/dev/null 2>&1 || die "bg-blue did not become healthy"
write_upstream app-blue
docker run -d --name bg-caddy --network bg-net -p 127.0.0.1::8080 \
	-v "$WORK/caddy:/etc/caddy:ro" "$CADDY_IMAGE" \
	caddy run --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null
PORT=$(docker port bg-caddy 8080/tcp | head -1 | sed 's/.*://')
for _ in $(seq 1 50); do
	[ "$(live_color 2>/dev/null || true)" = blue ] && break
	sleep 0.2
done
[ "$(live_color)" = blue ] || die "Caddy on 127.0.0.1:$PORT does not serve blue"
log "Caddy serves blue on 127.0.0.1:$PORT"

# measure <strategy> <run> <from> <to> <command...>
measure() {
	local s=$1 run=$2 from=$3 to=$4 out r
	shift 4
	load_start
	load_window_start
	out=$("$@")
	load_window_end
	r=$(load_stop)
	[ "$(live_color)" = "$to" ] || die "after $s run $run, the published port does not serve $to"
	record "$s" "$run" "$from" "$to" "${out% *}" "${out#* }" "$r"
}

has_phase() { case " $PHASES " in *" $1 "*) return 0 ;; esac; return 1; }

if has_phase bluegreen; then
	for run in $(seq 1 "$RUNS"); do
		measure bluegreen "$run" blue green bg_switch blue green
		measure rollback "$run" green blue bg_switch green blue
	done
fi

# Control: reload Caddy with an unchanged config, and change nothing else.
# Failures here come from the Caddy reload itself, not from the switch.
reload_only() {
	local t0
	t0=$(now_ms)
	for _ in $(seq 1 "$RELOADS"); do
		docker exec bg-caddy caddy reload --force --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null 2>&1 \
			|| die "caddy reload failed"
		sleep 0.3
	done
	echo "$(($(now_ms) - t0)) 0"
}
if has_phase reload && [ "$RELOADS" -gt 0 ]; then
	for run in $(seq 1 "$RUNS"); do
		measure "reload-x$RELOADS" "$run" blue blue reload_only
	done
fi

if has_phase rolling; then
	log "start bg-roll (blue) and move Caddy to app-roll"
	compose bg-roll blue app-roll up -d --wait --scale app=2 >/dev/null 2>&1 || die "bg-roll did not become healthy"
	write_upstream app-roll
	caddy_reload
	sleep 2
	compose bg-blue blue app-blue down -t 15 >/dev/null 2>&1
	from=blue
	for run in $(seq 1 "$RUNS"); do
		if [ "$from" = blue ]; then to=green; else to=blue; fi
		measure rolling "$run" "$from" "$to" roll "$to"
		from=$to
	done
fi

# ---------------------------------------------------------------------------

echo
echo "Per run. window = requests sent during the switch. failures = non-200 answers and"
echo "connection errors over the whole load run. slow = requests that took 1 s or more."
FMT='%-11s %3s %-12s %8s %8s %8s %5s %7s %9s %8s\n'
# shellcheck disable=SC2059
printf "$FMT" strategy run switch window total failures slow max_ms switch_ms drain_ms
for row in "${ROWS[@]}"; do
	IFS='|' read -r s run from to win total fail slow maxms sw dr <<<"$row"
	# shellcheck disable=SC2059
	printf "$FMT" "$s" "$run" "$from->$to" "$win" "$total" "$fail" "$slow" "$maxms" "$sw" "$dr"
done

echo
echo "Summary:"
SFMT='%-11s %4s %9s %9s %8s %5s %7s %14s %13s\n'
# shellcheck disable=SC2059
printf "$SFMT" strategy runs requests req_total failures slow max_ms switch_ms_mean switch_ms_max
VERDICT_BG=-1
VERDICT_ROLL=-1
for s in bluegreen rollback "reload-x$RELOADS" rolling; do
	n=0 win=0 total=0 fail=0 slow=0 maxms=0 swsum=0 swmax=0
	for row in "${ROWS[@]}"; do
		IFS='|' read -r rs _ _ _ rwin rtotal rfail rslow rmax rsw _ <<<"$row"
		[ "$rs" = "$s" ] || continue
		n=$((n + 1)) win=$((win + rwin)) total=$((total + rtotal)) fail=$((fail + rfail))
		slow=$((slow + rslow)) swsum=$((swsum + rsw))
		[ "$rmax" -gt "$maxms" ] && maxms=$rmax
		[ "$rsw" -gt "$swmax" ] && swmax=$rsw
	done
	[ "$n" -gt 0 ] || continue
	# shellcheck disable=SC2059
	printf "$SFMT" "$s" "$n" "$win" "$total" "$fail" "$slow" "$maxms" "$((swsum / n))" "$swmax"
	case $s in
	bluegreen | rollback)
		[ "$VERDICT_BG" -lt 0 ] && VERDICT_BG=0
		VERDICT_BG=$((VERDICT_BG + fail))
		;;
	rolling) VERDICT_ROLL=$fail ;;
	esac
done

echo
if [ "$VERDICT_BG" -lt 0 ] || [ "$VERDICT_ROLL" -lt 0 ]; then
	echo "verdict: not computed (BG_PHASES skipped bluegreen or rolling)"
elif [ "$VERDICT_BG" -eq 0 ]; then
	echo "blue/green: PASS (zero failed requests in all $RUNS switch and $RUNS rollback runs)"
	echo "strategy: bluegreen"
elif [ "$VERDICT_ROLL" -eq 0 ]; then
	echo "blue/green: FAIL ($VERDICT_BG failed requests); rolling: PASS"
	echo "strategy: rolling"
else
	echo "blue/green: FAIL ($VERDICT_BG failed requests); rolling: FAIL ($VERDICT_ROLL failed requests)"
	echo "strategy: none"
fi
