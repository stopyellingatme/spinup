#!/bin/sh
# Start sshd in the background, then run dockerd as the main process of the container.
set -eu
/usr/sbin/sshd -e
exec dockerd-entrypoint.sh "$@"
