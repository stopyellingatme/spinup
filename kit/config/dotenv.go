package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"
)

func loadDotenv(readFile func(string) ([]byte, error)) (map[string]string, error) {
	for _, path := range []string{".env", "../.env"} {
		data, err := readFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s: cannot read dotenv file", path)
		}
		values, err := parseDotenv(strings.NewReader(string(data)))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return values, nil
	}
	return nil, nil
}

func parseDotenv(r io.Reader) (map[string]string, error) {
	values := make(map[string]string)
	var problems []error
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		if strings.HasPrefix(text, "export ") || strings.HasPrefix(text, "export\t") {
			text = strings.TrimSpace(text[len("export"):])
		}
		key, raw, ok := strings.Cut(text, "=")
		key = strings.TrimSpace(key)
		if !ok || !validEnvKey(key) {
			problems = append(problems, fmt.Errorf("line %d: expected KEY=value", line))
			continue
		}
		value, err := dotenvValue(strings.TrimSpace(raw))
		if err != nil {
			problems = append(problems, fmt.Errorf("line %d (%s): %w", line, key, err))
			continue
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		problems = append(problems, errors.New("cannot read dotenv content"))
	}
	return values, errors.Join(problems...)
}
func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, c := range key {
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}
func dotenvValue(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if raw[0] != '\'' && raw[0] != '"' {
		for i, c := range raw {
			if c == '#' && (i == 0 || raw[i-1] == ' ' || raw[i-1] == '\t') {
				return strings.TrimSpace(raw[:i]), nil
			}
		}
		return raw, nil
	}
	quote := raw[0]
	end := -1
	for i := 1; i < len(raw); i++ {
		if quote == '"' && raw[i] == '\\' {
			i++
			continue
		}
		if raw[i] == quote {
			end = i
			break
		}
	}
	if end < 0 {
		return "", errors.New("unterminated quoted value")
	}
	tail := strings.TrimSpace(raw[end+1:])
	if tail != "" && !strings.HasPrefix(tail, "#") {
		return "", errors.New("unexpected text after quoted value")
	}
	if quote == '\'' {
		return raw[1:end], nil
	}
	value, err := strconv.Unquote(raw[:end+1])
	if err != nil {
		return "", errors.New("invalid quoted value")
	}
	return value, nil
}
