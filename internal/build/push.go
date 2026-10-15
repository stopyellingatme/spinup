package build

import (
	"encoding/json"
	"os"

	"github.com/stopyellingatme/spinup/internal/ui"
)

func readDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", &ui.Error{What: "Cannot read image digest", Why: err.Error(), Try: "check Docker buildx metadata and run spinup build again", Err: err}
	}
	var metadata struct {
		Digest string `json:"containerimage.digest"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return "", &ui.Error{What: "Cannot read image digest", Why: err.Error(), Try: "check Docker buildx metadata and run spinup build again", Err: err}
	}
	if metadata.Digest == "" {
		return "", &ui.Error{What: "Cannot read image digest", Why: "Docker buildx did not report containerimage.digest", Try: "check Docker buildx output and run spinup build again"}
	}
	return metadata.Digest, nil
}
