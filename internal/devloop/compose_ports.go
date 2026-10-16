package devloop

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strconv"

	"github.com/stopyellingatme/spinup/internal/exec"
)

// composeService is the part of a `docker compose ps --format json` entry that the
// dev loop reads.
type composeService struct {
	Service    string `json:"Service"`
	State      string `json:"State"`
	Publishers []struct {
		TargetPort    int `json:"TargetPort"`
		PublishedPort int `json:"PublishedPort"`
	} `json:"Publishers"`
}

// publishedPorts returns the host ports that running services of the app's dev
// Compose project already publish, keyed by "service:container-port". A previous
// `spinup dev` leaves its services running, and their ports are busy only because
// the app itself holds them. Reusing them keeps the ports stable and avoids
// recreating the containers. Any failure returns an empty map: allocation then
// probes the ports as usual.
func publishedPorts(ctx context.Context, runner exec.Runner, compose []string, root string) map[string]int {
	args := append(slices.Clone(compose), "ps", "--format", "json")
	result, err := runner.Run(ctx, exec.Cmd{Name: "docker", Args: args, Dir: root})
	if err != nil || result.ExitCode != 0 {
		return map[string]int{}
	}
	return parsePublishedPorts(result.Stdout)
}

// parsePublishedPorts reads both output shapes of `docker compose ps --format json`:
// one JSON object per line (Compose 2.21 and later) or one JSON array.
func parsePublishedPorts(data []byte) map[string]int {
	var services []composeService
	data = bytes.TrimSpace(data)
	if bytes.HasPrefix(data, []byte("[")) {
		if json.Unmarshal(data, &services) != nil {
			return map[string]int{}
		}
	} else {
		for line := range bytes.SplitSeq(data, []byte("\n")) {
			var service composeService
			if json.Unmarshal(bytes.TrimSpace(line), &service) == nil && service.Service != "" {
				services = append(services, service)
			}
		}
	}
	ports := map[string]int{}
	for _, service := range services {
		if service.State != "running" {
			continue
		}
		for _, p := range service.Publishers {
			if p.PublishedPort > 0 {
				ports[service.Service+":"+strconv.Itoa(p.TargetPort)] = p.PublishedPort
			}
		}
	}
	return ports
}
