package pgupgrade

import (
	"fmt"
	"strconv"

	"github.com/stopyellingatme/spinup/internal/maintain/pg"
)

// The source binaries can require older ICU or SSL libraries. Copy their resolved
// libraries into an isolated directory, and use it only for the old executables.
func helperDockerfile(old, target string, major int) string {
	return fmt.Sprintf(`FROM %s AS old
RUN mkdir /old-libs && ldd /usr/lib/postgresql/%d/bin/* | awk '/=> \/|^[[:space:]]*\// { for (i=1; i<=NF; i++) if ($i ~ /^\//) print $i }' | sort -u | xargs -r cp -L -t /old-libs
FROM %s
COPY --from=old /usr/lib/postgresql/%d /usr/lib/postgresql/%d
COPY --from=old /usr/share/postgresql/%d /usr/share/postgresql/%d
COPY --from=old /old-libs /opt/old-libs
RUN for bin in postgres pg_ctl pg_controldata pg_resetwal; do mv /usr/lib/postgresql/%d/bin/$bin /usr/lib/postgresql/%d/bin/$bin.real; printf '#!/bin/sh\nset -eu\nfor loader in /opt/old-libs/ld-linux*.so*; do test -x "$loader" && break; done\nexec "$loader" --library-path /opt/old-libs /usr/lib/postgresql/%d/bin/%%s.real "$@"\n' "$bin" > /usr/lib/postgresql/%d/bin/$bin; chmod 755 /usr/lib/postgresql/%d/bin/$bin; done
`, old, major, target, major, major, major, major, major, major, major, major, major)
}

func helperArgs(d pg.Details, image, script string, args ...string) []string {
	argv := []string{"docker", "run", "--rm", "--network", "none", "--mount", "type=volume,src=" + d.Volume + ",dst=/pg", "--workdir", "/tmp", "--entrypoint", "sh", image, "-ec", userScript, "pg-upgrade", d.User, script}
	return append(argv, args...)
}

const userScript = `user=$1; shift
uid=${user%:*}; gid=${user#*:}
if ! getent passwd "$uid" >/dev/null; then printf 'spinup-pg:x:%s:%s::/tmp:/bin/sh\n' "$uid" "$gid" >> /etc/passwd; fi
script=$1; shift
exec sh -ec "$script" "$user" "$@"`

// Each script receives the runtime UID:GID as $0 from userScript.
const pathGuard = `check_path() {
 current=/pg
 saved_ifs=$IFS; IFS=/
 for part in $1; do current=$current/$part; test ! -L "$current"; done
 IFS=$saved_ifs
}
`

const initScript = pathGuard + `user=$0
old=$1; new=$2; shift 2
check_path "$old"
check_path "$new"
check_path "$new.spinup-upgrade"
test -f "/pg/$old/PG_VERSION"
test ! -e "/pg/$new"
mkdir -p "/pg/${new%/*}"
# Reserve ownership outside PGDATA, which initdb requires to be empty.
(set -C; : > "/pg/$new.spinup-upgrade")
mkdir -m 0700 "/pg/$new"
chown "$user" "/pg/${new%/*}" "/pg/$new"
exec gosu "$user" "$@"`
const execScript = `exec gosu "$0" "$@"`
const disableScript = pathGuard + `old=$1
check_path "$old"
if [ -f "/pg/$old/global/pg_control" ]; then mv "/pg/$old/global/pg_control" "/pg/$old/global/pg_control.spinup-disabled"; fi
test ! -f "/pg/$old/global/pg_control"`

func validUser(user string) bool {
	uid, gid := "", ""
	for i, c := range user {
		if c == ':' {
			uid, gid = user[:i], user[i+1:]
			break
		}
	}
	u, e1 := strconv.ParseUint(uid, 10, 32)
	_, e2 := strconv.ParseUint(gid, 10, 32)
	return e1 == nil && e2 == nil && u != 0
}
