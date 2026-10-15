package secrets

import (
	"fmt"
	"os"
	"strings"
)

// Load reads NAME_FILE before NAME and trims one trailing newline.
// A missing value or an unreadable file returns an error without the value.
func Load(name string) (string, error) {
	if path, ok := os.LookupEnv(name + "_FILE"); ok {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read secret %s: cannot read %s_FILE\nTry: check the secret file and its permissions", name, name)
		}
		return trimNewline(string(data)), nil
	}
	if value, ok := os.LookupEnv(name); ok {
		return trimNewline(value), nil
	}
	return "", fmt.Errorf("read secret %s: no file or environment value is set\nTry: set %s_FILE or %s", name, name, name)
}

// Must returns Load(name) or panics with an error that contains no secret value.
func Must(name string) string {
	value, err := Load(name)
	if err != nil {
		panic(err)
	}
	return value
}

func trimNewline(value string) string {
	if strings.HasSuffix(value, "\n") {
		return strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	}
	return value
}
