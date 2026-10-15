package server

import (
	"context"
	"strconv"
	"strings"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func init() {
	doctor.Register(doctor.Check{ID: "vm-memory", Title: "VM memory", Scope: doctor.Remote, Run: memoryCheck})
}

func memoryCheck(ctx context.Context, c *doctor.Context) doctor.Result {
	if c == nil {
		return doctor.Result{Status: doctor.Skip, Message: "No remote connection was supplied."}
	}
	r, ok := c.Deps["remote"].(remote.Runner)
	if !ok || r == nil {
		return doctor.Result{Status: doctor.Skip, Message: "No remote connection was supplied."}
	}
	result, err := r.Run(ctx, remote.Cmd{Argv: []string{"cat", "/proc/meminfo"}})
	if err != nil {
		return doctor.Result{Status: doctor.Fail, Message: "Cannot read VM RAM.", Try: "Check SSH access to /proc/meminfo."}
	}
	kib := uint64(0)
	for line := range strings.SplitSeq(string(result.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "MemTotal:" && fields[2] == "kB" {
			kib, _ = strconv.ParseUint(fields[1], 10, 64)
			break
		}
	}
	switch {
	case kib == 0:
		return doctor.Result{Status: doctor.Fail, Message: "Cannot determine VM RAM.", Try: "Check MemTotal in /proc/meminfo."}
	case kib < 2*1024*1024:
		return doctor.Result{Status: doctor.Warn, Message: "The VM has less than 2 GiB of RAM. Use the lite monitoring profile.", Try: "Increase VM RAM to at least 2 GiB. Set monitoring.profile to lite."}
	case kib < 4*1024*1024:
		return doctor.Result{Status: doctor.OK, Message: "The VM has less than 4 GiB of RAM. Use the lite monitoring profile.", Try: "Set monitoring.profile to lite in spinup.toml."}
	default:
		return doctor.Result{Status: doctor.OK, Message: "The VM has at least 4 GiB of RAM."}
	}
}
