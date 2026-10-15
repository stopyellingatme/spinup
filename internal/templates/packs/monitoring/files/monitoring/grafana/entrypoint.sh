#!/bin/sh
# Managed by spinup: `spinup upgrade` keeps this file current.
#
# The Grafana start script (compose.monitoring.yml). It reads the secret files, copies the
# managed provisioning to $GF_PATHS_PROVISIONING, and writes the notification settings from
# the environment. It starts Grafana and resets the admin password from the secret.
# The secret values never enter the Compose file, the image, or `docker inspect`.
#
# It writes alerting/notifications.yml: the contact points and the notification policy.
# Contact point "spinup": one integration for each channel that is set.
#   ALERT_EMAIL_TO       email (Grafana needs SMTP_HOST too)
#   SLACK_WEBHOOK_URL    Slack (a secret file)
#   ALERT_NTFY_URL       ntfy (a webhook with the ntfy grafana template)
# Contact point "heartbeat": a webhook to HEARTBEAT_URL, when it is set.
# Every alert goes to "spinup". The heartbeat alert goes to "heartbeat" every 5 minutes.
set -eu

secrets=/run/secrets

# secret NAME prints the value of the secret file NAME, or nothing when the file is missing.
secret() {
	if [ -s "$secrets/$1" ]; then
		tr -d '\r\n' <"$secrets/$1"
	fi
}

# quote VALUE prints VALUE as a single-quoted YAML scalar. Grafana expands $NAME in
# provisioning files, so a literal dollar sign becomes $$.
quote() {
	printf "'%s'" "$(printf '%s' "$1" | tr -d '\r\n' | sed -e "s/'/''/g" -e 's/\$/$$/g')"
}

GF_SECURITY_ADMIN_PASSWORD=$(secret GRAFANA_ADMIN_PASSWORD)
if [ -z "$GF_SECURITY_ADMIN_PASSWORD" ]; then
	echo "grafana: start failed: the secret GRAFANA_ADMIN_PASSWORD is missing or empty in $secrets." >&2
	echo "Try: spinup secrets set GRAFANA_ADMIN_PASSWORD, then spinup secrets push." >&2
	exit 1
fi
export GF_SECURITY_ADMIN_PASSWORD

smtp_password=$(secret SMTP_PASSWORD)
if [ -n "$smtp_password" ]; then
	GF_SMTP_PASSWORD=$smtp_password
	export GF_SMTP_PASSWORD
fi
slack_url=$(secret SLACK_WEBHOOK_URL)

provisioning=${GF_PATHS_PROVISIONING:?compose.monitoring.yml sets GF_PATHS_PROVISIONING}
rm -rf "$provisioning"
mkdir -p "$provisioning"
cp -R /etc/spinup/provisioning/. "$provisioning/"
mkdir -p "$provisioning/alerting" "$provisioning/plugins"

channels=""
if [ -n "${ALERT_EMAIL_TO:-}" ]; then channels="$channels email"; fi
if [ -n "$slack_url" ]; then channels="$channels slack"; fi
if [ -n "${ALERT_NTFY_URL:-}" ]; then channels="$channels ntfy"; fi

# Grafana drops a contact point without integrations, and a route needs a contact point that
# exists. Without a channel, "spinup" gets a placeholder integration, and the mute timing
# "always" mutes every route: alerts show in Grafana only, and nothing is sent.
{
	echo "# Written by /etc/spinup/entrypoint.sh at start. Do not edit."
	echo "apiVersion: 1"
	echo "contactPoints:"
	echo "  - orgId: 1"
	echo "    name: spinup"
	echo "    receivers:"
	if [ -z "$channels" ]; then
		echo "      - uid: spinup-muted"
		echo "        type: webhook"
		echo "        settings:"
		echo "          url: 'http://127.0.0.1:9/muted'"
	fi
	if [ -n "${ALERT_EMAIL_TO:-}" ]; then
		echo "      - uid: spinup-email"
		echo "        type: email"
		echo "        settings:"
		echo "          addresses: $(quote "$ALERT_EMAIL_TO")"
		echo "          singleEmail: true"
	fi
	if [ -n "$slack_url" ]; then
		echo "      - uid: spinup-slack"
		echo "        type: slack"
		echo "        settings:"
		echo "          url: $(quote "$slack_url")"
	fi
	if [ -n "${ALERT_NTFY_URL:-}" ]; then
		case $ALERT_NTFY_URL in
		*\?*) ntfy_url="$ALERT_NTFY_URL&template=grafana" ;;
		*) ntfy_url="$ALERT_NTFY_URL?template=grafana" ;;
		esac
		echo "      - uid: spinup-ntfy"
		echo "        type: webhook"
		echo "        settings:"
		echo "          url: $(quote "$ntfy_url")"
		echo "          httpMethod: POST"
	fi
	if [ -n "${HEARTBEAT_URL:-}" ]; then
		echo "  - orgId: 1"
		echo "    name: heartbeat"
		echo "    receivers:"
		echo "      - uid: spinup-heartbeat"
		echo "        type: webhook"
		echo "        disableResolveMessage: true"
		echo "        settings:"
		echo "          url: $(quote "$HEARTBEAT_URL")"
		echo "          httpMethod: POST"
	fi
	echo "muteTimes:"
	echo "  - orgId: 1"
	echo "    name: always"
	echo "    time_intervals: [{}]"
	echo "policies:"
	echo "  - orgId: 1"
	echo "    receiver: spinup"
	echo "    group_by: [prometheus_alertname, job]"
	echo "    group_wait: 30s"
	echo "    group_interval: 5m"
	echo "    repeat_interval: 4h"
	echo "    routes:"
	# The heartbeat alert (route=heartbeat) calls HEARTBEAT_URL every 5 minutes.
	echo "      - object_matchers: [[route, '=', heartbeat]]"
	echo "        group_by: [route]"
	echo "        group_wait: 0s"
	echo "        group_interval: 5m"
	echo "        repeat_interval: 5m"
	if [ -n "${HEARTBEAT_URL:-}" ]; then
		echo "        receiver: heartbeat"
	else
		echo "        receiver: spinup"
		echo "        mute_time_intervals: [always]"
	fi
	if [ -z "$channels" ]; then
		echo "      - object_matchers: [[route, '!=', heartbeat]]"
		echo "        receiver: spinup"
		echo "        mute_time_intervals: [always]"
	fi
} >"$provisioning/alerting/notifications.yml"

# Older CLI versions need a manual reset. Detect stdin support without exposing the secret.
if ! grafana cli admin reset-admin-password --help 2>/dev/null | grep -q -- '--password-from-stdin'; then
	echo "grafana: automatic password rotation unavailable: the CLI needs a manual reset." >&2
	echo "Try: sign in with the existing password and change it to match GRAFANA_ADMIN_PASSWORD." >&2
	exec /run.sh "$@"
fi

# Grafana creates the admin user on its first start. Wait for its database migrations before
# resetting the password. This also applies a rotated secret to an existing grafana_data volume.
/run.sh "$@" &
server_pid=$!
stop_server() {
	kill -TERM "$server_pid" 2>/dev/null || :
	wait "$server_pid" || :
}
trap 'stop_server; exit 143' TERM
trap 'stop_server; exit 130' INT

attempts=0
until wget -q -O /dev/null http://127.0.0.1:3000/api/health; do
	if ! kill -0 "$server_pid" 2>/dev/null || [ "$attempts" -ge 120 ]; then
		echo "grafana: password reset failed: the server did not become healthy." >&2
		echo "Try: inspect the Grafana logs, then restart the monitoring stack." >&2
		stop_server
		exit 1
	fi
	attempts=$((attempts + 1))
	sleep 1
done

# Use the same configuration and database as the server. Never put the password in an argument
# or interpolate it into shell code. The CLI reads the secret through standard input.
if ! printf '%s\n' "$GF_SECURITY_ADMIN_PASSWORD" | grafana cli \
	--homepath "${GF_PATHS_HOME:-/usr/share/grafana}" \
	--config "${GF_PATHS_CONFIG:-/etc/grafana/grafana.ini}" \
	--configOverrides "cfg:default.paths.data=${GF_PATHS_DATA:-/var/lib/grafana}" \
	admin reset-admin-password --password-from-stdin; then
	echo "grafana: password reset failed: the CLI could not update the admin user." >&2
	echo "Try: inspect the Grafana logs and the secret, then restart the monitoring stack." >&2
	stop_server
	exit 1
fi

# Preserve the server exit status and forward container shutdown signals.
set +e
wait "$server_pid"
status=$?
trap - TERM INT
exit "$status"
