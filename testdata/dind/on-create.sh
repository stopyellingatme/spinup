#!/usr/bin/env bash
# The on-create hook of the dofake binary (docs/contributing/dind.md). It runs while the fake
# handles a droplet create, and it makes the dind VM look like a droplet that cloud-init set up.
#
# Usage: on-create.sh <staging directory>
# The directory comes from testdata/dind/dofake. It holds the host key of the user data
# (ssh_host_ed25519_key and .pub), a manifest ("<mode> <path>" for each write_files entry),
# and root/<path> for each of these files.
# Environment: DIND_VM is the container name. DIND_WORK is the work directory of the run.
set -euo pipefail

dir="${1:?on-create.sh: the staging directory is missing}"
vm="${DIND_VM:?on-create.sh: DIND_VM is not set}"
work="${DIND_WORK:?on-create.sh: DIND_WORK is not set}"

[[ -f "$dir/root/usr/local/sbin/spinup-host-key" ]] ||
  { echo "on-create.sh: the user data has no /usr/local/sbin/spinup-host-key" >&2; exit 1; }
sudoers="$(find "$dir/root/run" -name 'spinup-*-hostkey-sudoers' 2>/dev/null | head -n 1)"
[[ -n "$sudoers" ]] || { echo "on-create.sh: the user data has no host key sudoers file" >&2; exit 1; }

# Record the key that the user data injects. The suite checks that rotation replaced it.
cp "$dir/ssh_host_ed25519_key.pub" "$work/injected_host_key.pub"

stage=/tmp/dofake-create
docker exec "$vm" rm -rf "$stage"
docker cp "$dir" "$vm:$stage" >/dev/null

docker exec -i "$vm" sh -s "$stage" "$(basename "$sudoers")" <<'VM'
set -eu
stage=$1
rule=$2
# The injected host key, as cloud-init installs it.
install -m 0600 -o root -g root "$stage/ssh_host_ed25519_key" /etc/ssh/ssh_host_ed25519_key
install -m 0644 -o root -g root "$stage/ssh_host_ed25519_key.pub" /etc/ssh/ssh_host_ed25519_key.pub
# The rotation script and its sudoers rule, as the write_files and runcmd entries install them.
install -d -m 0755 /usr/local/sbin /var/lib/spinup
install -m 0755 -o root -g root "$stage/root/usr/local/sbin/spinup-host-key" /usr/local/sbin/spinup-host-key
visudo -cf "$stage/root/run/$rule"
install -m 0440 -o root -g root "$stage/root/run/$rule" "/etc/sudoers.d/${rule%-sudoers}"
rm -f /var/lib/spinup/host-key-rotated /etc/ssh/ssh_host_ed25519_key.next /etc/ssh/ssh_host_ed25519_key.next.pub
# sshd serves only the ed25519 host key, so the pin holds.
if grep -qE '^[[:space:]]*Include[[:space:]]' /etc/ssh/sshd_config; then
  echo 'HostKey /etc/ssh/ssh_host_ed25519_key' >/etc/ssh/sshd_config.d/zz-spinup-hostkey.conf
else
  echo 'HostKey /etc/ssh/ssh_host_ed25519_key' >>/etc/ssh/sshd_config
fi
rm -rf "$stage"
# The entrypoint starts sshd, and sshd writes the pid file. A HUP makes it read the new key.
kill -HUP "$(cat /run/sshd.pid 2>/dev/null || cat /var/run/sshd.pid)"
VM
sleep 1
echo "on-create.sh: the VM now serves the injected host key"
