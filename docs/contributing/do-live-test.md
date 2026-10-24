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

## Prerequisites

- A DigitalOcean account that holds no other droplets, firewalls, or tags.
  The last step fails when anything but the SSH key is left.
- A stored sign-in with a refresh token. Run `spinup deploy digital-ocean login` first.
- A spinup project with the deploy pack and a clean git tree.
  An app that `spinup new` made is enough.
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

## What the script checks

1. A provision run with `--skip-deploy` gets `kill -9` after the line `Created the droplet`.
2. A second run says `Using the droplet`.
3. A third run, with the deploy, creates and deletes nothing.
4. The script moves the expiry of the stored token into the past.
   The next command must refresh the token and write a later expiry.
   The script never prints a token value.
5. A plain `spinup deploy` works with an empty `SPINUP_CREDENTIALS_DIR` and no `DIGITALOCEAN_*` variable.
6. `curl` reaches the public URL, and the certificate is valid.
7. `spinup deploy digital-ocean destroy --confirm-env <env> --i-know` deletes the environment.
8. The account holds no droplet, firewall, or tag. Only the SSH key stays.

The script lists the names of the droplets, firewalls, tags, and SSH keys that it finds, and it prints no other data.
