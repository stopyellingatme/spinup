# Run the DigitalOcean live test

The script `scripts/do-live-test.sh` repeats the live test of `spinup deploy digital-ocean` on a real account.
ADR 0018 decision 7 defines the test.
The script is not part of `make` or CI, and no doctest runs it.

## Cost

The script creates one droplet, one firewall, and one tag, and it destroys them at the end.
The droplet size comes from the plan of the environment.
A `s-1vcpu-2gb` droplet costs a few cents for the short run.
If a step fails, the droplet can stay on the account and keep costing money.
The script then prints the destroy command. Run it.

If the app declares sidecar services, two colors run during a color switch.
Use the size `s-2vcpu-4gb` for such an app.

## Prerequisites

- A DigitalOcean account that holds no other droplets, firewalls, or tags.
  The last step fails when anything but the SSH key is left.
- A stored sign-in with a refresh token. Run `spinup deploy digital-ocean login` first.
- A spinup project with the deploy pack and a clean git tree.
  An app that `spinup new` made is enough.
- For the full test, use an app with the `jobs` module, one `rust` service, and one `python` service.
  Add the services with `spinup g service <name> --lang rust|python`.
  Without services, the service step does nothing.
- The tools `jq`, `curl`, `openssl`, `ssh`, `docker`, `sops`, and `age-keygen`.
- A terminal. The script reads the answers from `/dev/tty`. Without a terminal, set `SPINUP_DO_LIVE_YES=1` to answer yes. Set it only after a person approved the cost.
- `--yes` in the provision flags and `SPINUP_DO_DEPLOY_ARGS=--yes`, because the provision and deploy runs have no terminal.

## Run the test

The script refuses to run when `SPINUP_DO_LIVE` is not `1`.
It needs the app directory and the environment name.
Pass the provision flags after them.

<!-- doctest: skip -->
```sh
SPINUP_DO_LIVE=1 scripts/do-live-test.sh ~/code/todo live-test --ssh-key ~/.ssh/id_ed25519.pub
```

The script prints the plan and asks for `yes` before it creates anything.
It asks again before it destroys the environment.
Set `SPINUP_BIN` to test a spinup binary that is not in `PATH`.
Set `SPINUP_DO_DEPLOY_ARGS` for extra flags of the deploy runs.
Set `SPINUP_DO_VERIFY_CMD` for your own check after the service step (see step 7 below).
Set `SPINUP_DO_PUBLIC_COOKIE` to the session cookie for the checks of public routes with `auth = "session"`.

## What the script checks

1. A provision run with `--skip-deploy` gets `kill -9` after the line `Created the droplet`.
2. A second run says `Using the droplet`.
3. A third run, with the deploy, creates and deletes nothing.
4. The script moves the expiry of the stored token into the past.
   The next command must refresh the token and write a later expiry.
   The script never prints a token value.
5. A plain `spinup deploy` works with an empty `SPINUP_CREDENTIALS_DIR` and no `DIGITALOCEAN_*` variable.
6. `curl` reaches the public URL, and the certificate is valid.
7. The services and the hook. The script connects to the droplet over `ssh` with the host, the user, and the pinned host key of `spinup.toml`.
   The private key is the `--ssh-key` public key without `.pub`. The default is `~/.ssh/id_ed25519`.
   If `spinup.toml` declares `[[services]]`, the live color must pass these checks for each service:
   - Each service container runs and is healthy, and the old color runs no such container.
   - `GET <health>` answers through the `caddy` container.
   - The `api` container, and the `worker` container, have `SERVICE_<NAME>_URL`.

   For each service with `public` in `spinup.toml`, the script requests `https://<host><public><health>`:
   - With `auth = "none"`, the route must answer HTTP 200.
   - With `auth = "session"`, a request with no cookie must answer HTTP 401.
   - With `auth = "session"` and `SPINUP_DO_PUBLIC_COOKIE` set, a request with that cookie must answer HTTP 200.
     The value is the full `Cookie` header of a signed-in session. The script never prints it.
   - If no service is public, the script skips these checks.

   If the app has the `jobs` module, the `worker` container must run in the live color only.
   It must not restart during a wait of 10 seconds.
   If `SPINUP_DO_VERIFY_CMD` is set, the script runs it with `bash -c`.
   The script exports `SPINUP_DO_HOST` (the droplet IP) and `SPINUP_DO_SSH_KEY` (the private key file).
   If the command fails, the script stops and prints the destroy command.
8. `spinup deploy digital-ocean destroy --confirm-env <env> --i-know` deletes the environment.
9. The account holds no droplet, firewall, or tag. Only the SSH key stays.

The script lists the names of the droplets, firewalls, tags, and SSH keys that it finds, and it prints no other data.
