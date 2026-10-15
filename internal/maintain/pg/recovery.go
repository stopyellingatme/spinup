package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/stopyellingatme/spinup/internal/backup"
	"github.com/stopyellingatme/spinup/internal/remote"
)

type upgradeState struct {
	OldVolume string `json:"old_volume"`
	NewVolume string `json:"new_volume"`
	Anchor    string `json:"anchor"`
	Complete  bool   `json:"complete"`
}

// ReleaseRecovery releases a strategy's recovery reservation after a safe recovery.
// The caller must first verify that the original cluster is intact and restart its services.
func (s *Session) ReleaseRecovery(ctx context.Context) error {
	u := s.u
	anchor := fmt.Sprintf("%s-pg-recovery-%d-%d", u.cfg.App.Name, u.current.major, u.current.minor)
	// Without -v: the anchor shares the live volume.
	if _, err := u.run(ctx, "docker", "rm", anchor); err != nil {
		return err
	}
	const release = `test ! -L "$1"
test -d "$1"
rm -rf -- "$1"`
	if _, err := u.run(ctx, "sh", "-ec", release, "pg-recovery", u.operationDir()); err != nil {
		return err
	}
	u.recovery = ""
	return nil
}

func shell(args ...string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = remote.Quote(arg)
	}
	return strings.Join(quoted, " ")
}
func (u *upgrade) recoveryCommand() string {
	args := []string{"ssh"}
	if config := os.Getenv("SPINUP_SSH_CONFIG"); config != "" {
		args = append(args, "-F", config)
	}
	if u.env.Port != 0 {
		args = append(args, "-p", strconv.Itoa(u.env.Port))
	}
	host := u.env.Host
	if u.env.User != "" {
		host = u.env.User + "@" + host
	}
	return shell(append(args, "--", host, shell("sh", u.operationDir()+"/rollback.sh"))...)
}
func (u *upgrade) saveRecovery(ctx context.Context, oldVolume string) error {
	fmt.Fprintln(u.o.Out, "Save the old image, Compose files, and recovery command")
	dir := u.operationDir()
	args := []string{"mkdir", "-m", "0700"}
	if !u.o.Major {
		args = append(args, "-p")
	}
	args = append(args, dir)
	if _, err := u.run(ctx, args...); err != nil {
		return failure("reserve recovery directory (inspect an earlier upgrade before retrying)", err)
	}
	var script strings.Builder
	script.WriteString("#!/bin/sh\nset -eu\n")
	lock := "/opt/" + u.cfg.App.Name + "/deploy.lock"
	script.WriteString(shell("mkdir", lock) + "\n")
	script.WriteString("trap " + remote.Quote(shell("rm", "-rf", "--", lock)) + " EXIT\n")
	script.WriteString("printf '{\"owner\":\"pg-recovery\",\"pid\":%s,\"host\":\"recovery\",\"acquired\":\"%s\",\"ttl_seconds\":86400,\"release\":\"pg-recovery\"}\\n' \"$$\" \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\" > " + remote.Quote(lock+"/owner.json") + "\n")
	if u.o.Major {
		script.WriteString(stopColorsScript(u.cfg.App.Name) + "\n")
	}
	script.WriteString(shell(u.base("stop", "postgres")...) + "\n")
	files := append(append([]string{}, u.files...), u.dir+"/Dockerfile.postgres", u.dir+"/.env")
	if u.o.Major && u.usesPGBackRest() {
		files = append(files, backup.RemoteConfigPath(u.cfg.App.Name))
	}
	for _, file := range files {
		saved := dir + "/" + filepath.Base(file)
		if _, err := u.run(ctx, "cp", "--", file, saved); err != nil {
			return err
		}
		script.WriteString(shell("cp", "--", saved, file) + "\n")
	}
	script.WriteString(shell("docker", "tag", u.live.Image, u.live.Config.Image) + "\n")
	script.WriteString(shell(u.base("up", "-d", "--no-deps", "--no-build", "--pull", "never", "--force-recreate", "--wait", "--wait-timeout", "120", "postgres")...) + "\n")
	if u.o.Major {
		script.WriteString(shell(u.color(append([]string{"up", "-d", "--no-deps", "--wait"}, u.services()...)...)...) + "\n")
	}
	if u.rollbackRefusal != "" {
		script.Reset()
		script.WriteString("#!/bin/sh\nset -eu\nprintf '%s\\n' " + remote.Quote(u.rollbackRefusal) + " >&2\nexit 1\n")
	}
	if err := u.vm.Upload(ctx, []byte(script.String()), dir+"/rollback.sh", 0700); err != nil {
		return failure("save rollback script", err)
	}
	u.recovery = u.recoveryCommand()
	fmt.Fprintf(u.o.Out, "Recovery command: %s\n", u.recovery)
	anchor := fmt.Sprintf("%s-pg-recovery-%d-%d", u.cfg.App.Name, u.current.major, u.current.minor)
	if !u.o.Major {
		// Without -v: a retry must retain every data volume.
		const removeAnchor = `if docker container inspect "$1" >/dev/null 2>&1; then docker rm -f "$1"; fi`
		if _, err := u.run(ctx, "sh", "-ec", removeAnchor, "pg-recovery", anchor); err != nil {
			return err
		}
	}
	args = []string{"docker", "create", "--name", anchor, "--entrypoint", "/bin/true"}
	if oldVolume != "" {
		args = append(args, "--mount", "type=volume,src="+oldVolume+",dst=/old,readonly")
	}
	args = append(args, u.live.Image)
	if _, err := u.run(ctx, args...); err != nil {
		return err
	}
	if u.o.Major {
		return u.writeState(ctx, upgradeState{OldVolume: oldVolume, NewVolume: fmt.Sprintf("%s_pgdata_%d", u.cfg.App.Name, u.o.To), Anchor: anchor})
	}
	return nil
}
func (u *upgrade) writeState(ctx context.Context, s upgradeState) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err = u.vm.Upload(ctx, data, u.operationDir()+"/state.json", 0600); err != nil {
		return failure("save upgrade state", err)
	}
	return nil
}
func (u *upgrade) cleanup(ctx context.Context) error {
	fmt.Fprintln(u.o.Out, "Check the completed upgrade and the active volume")
	data, err := u.run(ctx, "cat", u.operationDir()+"/state.json")
	if err != nil {
		return err
	}
	var s upgradeState
	if err = json.Unmarshal(data, &s); err != nil {
		return failure("read upgrade state", err)
	}
	prefix := u.cfg.App.Name + "_"
	if !s.Complete || !strings.HasPrefix(s.OldVolume, prefix) || s.OldVolume == s.NewVolume || !strings.HasPrefix(s.NewVolume, prefix+"pgdata_") || !strings.HasPrefix(s.Anchor, u.cfg.App.Name+"-pg-recovery-") || strings.ContainsAny(s.OldVolume+s.NewVolume+s.Anchor, "/\x00\n\r ") {
		return failure("remove old volume", errors.New("upgrade state is incomplete or invalid"))
	}
	if err = u.inspect(ctx); err != nil {
		return err
	}
	if u.volume() != s.NewVolume {
		return failure("remove old volume", errors.New("the active PostgreSQL volume does not match the completed upgrade"))
	}
	if err = u.checkRepositorySwap(s.NewVolume); err != nil {
		return err
	}
	fmt.Fprintf(u.o.Out, "Remove the retained old volume %s\n", s.OldVolume)
	// Without -v: removing the anchor must never remove a data volume.
	if _, err = u.run(ctx, "docker", "rm", s.Anchor); err != nil {
		return err
	}
	if _, err = u.run(ctx, "docker", "volume", "rm", "--", s.OldVolume); err != nil {
		return err
	}
	_, err = u.run(ctx, "rm", "-rf", "--", u.operationDir())
	return err
}
func (u *upgrade) volume() string {
	for _, m := range u.live.Mounts {
		if m.Type == "volume" && (m.Destination == "/var/lib/postgresql" || m.Destination == "/var/lib/postgresql/data") {
			return m.Name
		}
	}
	return ""
}
func stopColorsScript(app string) string {
	var b strings.Builder
	for _, color := range []string{"blue", "green"} {
		for _, service := range []string{"api", "web"} {
			b.WriteString("ids=$(" + shell("docker", "ps", "-q", "--filter", "label=com.docker.compose.project="+app+"-"+color, "--filter", "label=com.docker.compose.service="+service) + ")\n")
			b.WriteString("if [ -n \"$ids\" ]; then docker stop $ids; fi\n")
		}
	}
	return b.String()
}
