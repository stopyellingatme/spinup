# The test "VM" of the Docker-in-Docker deploy suite (docs/contributing/dind.md).
# It is the pinned docker:dind image plus sshd, sudo, and a deploy user.
# run.sh renders the image marker below from internal/stackversions/stackversions.yaml
# and builds this file once per run. The build needs the network for apk.
FROM [[ image "docker-dind" ]]

RUN apk add --no-cache bash curl openssh-server python3 sudo

# The deploy user has UID 1000 (the default of DEPLOY_UID) and is in the docker group.
# dockerd gives its socket to the docker group when the group exists.
# "*" in /etc/shadow disables the password but permits public-key login.
RUN (getent group docker >/dev/null || addgroup -S docker) \
 && adduser -D -u 1000 -s /bin/bash deploy \
 && addgroup deploy docker \
 && sed -i 's/^deploy:!:/deploy:*:/' /etc/shadow \
 && echo 'deploy ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/deploy \
 && chmod 0440 /etc/sudoers.d/deploy \
 && install -d -m 0700 -o deploy -g deploy /home/deploy/.ssh \
 && ssh-keygen -A

COPY sshd_config /etc/ssh/sshd_config.d/spinup-dind.conf
COPY entrypoint.sh /usr/local/bin/spinup-dind-entrypoint.sh

# Stand-ins for the droplet tools that spinup deploy digital-ocean calls (docs/contributing/dind.md).
# cloud-init answers "status: done". systemctl turns the reload of ssh.service into a HUP of sshd.
# ssh-keygen (used by the host key rotation) came with openssh-server, and visudo with sudo.
COPY cloud-init /usr/bin/cloud-init
COPY systemctl /usr/bin/systemctl
RUN chmod 0755 /usr/bin/cloud-init /usr/bin/systemctl

# Each run generates a new key pair, so only this layer changes between runs.
ARG DEPLOY_PUBKEY
RUN test -n "$DEPLOY_PUBKEY" \
 && printf '%s\n' "$DEPLOY_PUBKEY" > /home/deploy/.ssh/authorized_keys \
 && chown deploy:deploy /home/deploy/.ssh/authorized_keys \
 && chmod 0600 /home/deploy/.ssh/authorized_keys

EXPOSE 22 80
ENTRYPOINT ["/usr/local/bin/spinup-dind-entrypoint.sh"]
CMD []
