# Disaster-recovery drill

The drill proves the backup promise of milestone M7.
It replaces a destroyed VM and restores the database from object storage inside a time budget, the recovery time objective (RTO).
The test storage is described in ADR 0011 §8. The test VM is described in ADR 0009 §7.

## What the drill does

The script `testdata/dind/dr/run.sh` reuses the test VM of the DinD suite (`testdata/dind/vm/`, see [dind.md](dind.md)).
It runs SeaweedFS as the S3 repository of pgBackRest. MinIO is archived.

| Step | What happens | Check |
| --- | --- | --- |
| 0 | Build `spinup` and the VM image. Generate the API-only app `spinup-dr-app` with `--modules ci,deploy,secrets,backups` and the local kit. | `spinup new` and the API build succeed. |
| 1 | Start SeaweedFS (`server -s3`, the pinned image) with a self-signed TLS certificate. Create the bucket. Write the `PGBACKREST_*` secrets. | The bucket exists. S3 answers HTTPS. |
| 2 | Start VM 1 and prepare `/opt/<app>`. Deploy. Insert 500 rows with `psql` in the `postgres` container. Run `spinup backup now --type full`. | `/healthz` answers. `backup list` shows a full backup. |
| 3 | Destroy VM 1: remove the container and its volumes. The RTO clock starts. | The container is gone. |
| 4 | Start VM 2 with new SSH host keys. Remove the old key from `known_hosts`. Prepare `/opt/<app>`, run `secrets push`, and deploy the same release. Run `spinup backup restore --into in-place`. | SSH refuses the changed key until the old key is removed. The new database is empty before the restore. |
| 5 | Read the rows and `/readyz`. Stop the RTO clock. | The row count and digest match step 2. `/readyz` answers 200. The RTO is not more than `DR_RTO_BUDGET`. |

The script prints the time of each step and the RTO at the end.

### The S3 repository

pgBackRest speaks only HTTPS to S3, and it verifies the certificate.
The script makes a self-signed certificate for the host name `s3.spinup-dr.test` with `openssl` in the VM image.
SeaweedFS serves S3 on port 443 of the Docker network `spinup-dr-net-<pid>`.
Both VMs join this network, and the `postgres` container in a VM reaches SeaweedFS through the `<app>-backup` network.
The host name must have a dot. The Docker DNS in the VM does not forward a single-label name to the Docker DNS of the host.

The app gets these secrets for the `staging` environment:

| Secret | Value |
| --- | --- |
| `PGBACKREST_REPO1_S3_ENDPOINT` | `s3.spinup-dr.test` |
| `PGBACKREST_REPO1_S3_BUCKET` | `spinup-dr-repo` |
| `PGBACKREST_REPO1_S3_REGION` | `us-east-1` |
| `PGBACKREST_REPO1_S3_KEY` and `PGBACKREST_REPO1_S3_KEY_SECRET` | the credentials in the SeaweedFS `-s3.config` file |
| `PGBACKREST_REPO1_CIPHER_PASS` | a passphrase for each run |
| `PGBACKREST_REPO1_S3_URI_STYLE` | `path` |
| `PGBACKREST_REPO1_STORAGE_CA_FILE` | `/run/secrets/DR_S3_CA` |
| `DR_S3_CA` | the self-signed certificate |

### The replacement VM

The SSH port is in the SSH config of the script, not in `spinup.toml`.
Both VMs publish HTTP on the same local port, so `public_url` does not change.
The commit and the release ID stay the same, and the restore smoke check finds the release in `/version`.

`HostKeyAlias` in the SSH config makes `known_hosts` identify each VM by the alias `spinup-dr-vm`, as for a real host name.
VM 2 has new host keys. The script first checks that SSH refuses VM 2. Then it removes the old key with `ssh-keygen -R`, as an operator does.

### The restore flags

The drill runs `spinup backup restore --env staging --into in-place --i-know --yes --now --no-safety-backup`.

- `--i-know` and `--yes` confirm the replacement of the live database without a prompt.
- `--now` ignores the maintenance window.
- `--no-safety-backup` skips the safety backup. The new database on VM 2 is empty. Its system identifier is different from the stanza in the repository, so pgBackRest cannot back it up into the same stanza.

### Configuration on the replacement VM

Each pgBackRest backend uploads `pgbackrest.conf` before its first pgBackRest command.
The upload applies the configured retention and uses mode 0600.
VM 2 needs no prior backup or manual configuration upload before restoration.
Configuration upload does not create a stanza.

Without `--no-safety-backup`, pgBackRest rejects the new cluster with error 051 during the safety backup.
The command explains that the repository does not know the new cluster and suggests `--no-safety-backup`.
It stops before any service shutdown.
If a pgBackRest command fails, spinup includes its redacted `ERROR` and `WARN` lines from stdout and stderr.

## Run the drill

Docker must run. The VM must have network access to pull images and build the Postgres image with pgBackRest.

```
bash testdata/dind/dr/run.sh
```

| Option | Effect |
| --- | --- |
| `DR_RTO_BUDGET=<seconds>` | The RTO budget. The default is 900 (15 minutes). |
| `DR_KEEP=1` | Keep the work directory and its logs. The trap still removes the containers. |
| `GO=<path>` | Use this `go` binary. |

The script also needs `git`, `ssh`, `ssh-keygen`, `sops`, `age-keygen`, `curl`, and `python3`.
Run `spinup tools install` to get `sops` and `age`.

The RTO includes the start of VM 2, the build of the Postgres image in VM 2, the image transport, the deploy, and the restore.

## Clean up

Every container, network, volume, and image of a run has a name that starts with `spinup-dr-`.
A trap removes them when the script stops, also after a failure.
The script uses no fixed host port. It never touches other containers.

If a run fails, read the logs in the work directory that the script prints.
`postgres.log` holds the log of the `postgres` container of the last VM, and `seaweedfs.log` holds the log of SeaweedFS.

## CI

`.github/workflows/dr.yml` runs the drill each week and on manual dispatch.
A manual run can set the RTO budget in seconds.
