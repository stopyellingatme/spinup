package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Base contains the common settings for a spinup API.
type Base struct {
	// Env selects the application environment. The default is production.
	Env string `env:"SPINUP_ENV" default:"production"`
	// Port selects the HTTP listen port.
	Port int `env:"PORT" default:"8080"`
	// DatabaseURL contains the PostgreSQL connection string.
	DatabaseURL string `env:"DATABASE_URL"`
	// LogLevel selects the slog severity threshold.
	LogLevel string `env:"LOG_LEVEL" default:"info"`
	// ShutdownTimeout limits graceful shutdown.
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" default:"20s"`
	// BehaviorVersion selects the kit behavior defaults.
	BehaviorVersion int `env:"SPINUP_BEHAVIOR_VERSION" default:"1"`
}

// Behavior reports whether the app opted into behavior version v.
// Kit code changes a default only behind a behavior version.
// Apps opt in through behavior_version in spinup.toml.
func (b Base) Behavior(v int) bool { return b.BehaviorVersion >= v }

// Load fills dst from the environment. When SPINUP_ENV is empty or dev, it
// also reads ./.env, or ../.env when ./.env is absent. If the environment sets X or X_FILE,
// Load ignores both forms in .env. Load does not modify the process environment.
func Load(dst any) error {
	values := map[string]string{}
	if env := os.Getenv("SPINUP_ENV"); env == "" || env == "dev" {
		var err error
		values, err = loadDotenv(os.ReadFile)
		if err != nil {
			return configErrors([]error{err})
		}
	}
	return LoadFrom(dst, func(key string) (string, bool) {
		if value, ok := os.LookupEnv(key); ok {
			return value, true
		}
		// The environment takes precedence across both forms of a setting.
		other := key + "_FILE"
		if strings.HasSuffix(key, "_FILE") {
			other = strings.TrimSuffix(key, "_FILE")
		}
		if _, ok := os.LookupEnv(other); ok {
			return "", false
		}
		value, ok := values[key]
		return value, ok
	}, os.ReadFile)
}

// LoadFrom fills a struct pointer using lookup and readFile.
// It supports env, default, and required tags, including fields in embedded structs.
// It reports all field errors together without exposing configuration values.
func LoadFrom(dst any, lookup func(string) (string, bool), readFile func(string) ([]byte, error)) error {
	value := reflect.ValueOf(dst)
	if value.Kind() != reflect.Pointer || value.IsNil() || value.Elem().Kind() != reflect.Struct {
		return configErrors([]error{errors.New("configuration failed: destination must be a non-nil pointer to a struct")})
	}
	if lookup == nil || readFile == nil {
		return configErrors([]error{errors.New("configuration failed: lookup and readFile must be provided")})
	}
	var problems []error
	walk(value.Elem(), lookup, readFile, &problems, make(map[reflect.Type]bool))
	return configErrors(problems)
}

func configErrors(problems []error) error {
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%w\nTry: correct the named environment variables or secret files and restart", errors.Join(problems...))
}

func walk(value reflect.Value, lookup func(string) (string, bool), readFile func(string) ([]byte, error), problems *[]error, visiting map[reflect.Type]bool) {
	if visiting[value.Type()] {
		return
	}
	visiting[value.Type()] = true
	defer delete(visiting, value.Type())
	for i := range value.NumField() {
		field, meta := value.Field(i), value.Type().Field(i)
		name := meta.Tag.Get("env")
		if name == "" {
			if !meta.Anonymous {
				continue
			}
			if field.Kind() == reflect.Pointer && field.Type().Elem().Kind() == reflect.Struct {
				if visiting[field.Type().Elem()] {
					continue
				}
				if field.IsNil() {
					if !field.CanSet() {
						*problems = append(*problems, fmt.Errorf("%s: cannot initialize an unexported embedded pointer", meta.Name))
						continue
					}
					field.Set(reflect.New(field.Type().Elem()))
				}
				field = field.Elem()
			}
			if field.Kind() == reflect.Struct {
				walk(field, lookup, readFile, problems, visiting)
			}
			continue
		}
		if name == "-" {
			continue
		}
		if !field.CanSet() {
			*problems = append(*problems, fmt.Errorf("%s: field is not exported", name))
			continue
		}
		raw, set := lookup(name)
		path, fileSet := lookup(name + "_FILE")
		if set && fileSet {
			*problems = append(*problems, fmt.Errorf("%s: both %s and %s_FILE are set", name, name, name))
			continue
		}
		if fileSet {
			data, err := readFile(path)
			if err != nil {
				*problems = append(*problems, fmt.Errorf("%s: cannot read %s_FILE", name, name))
				continue
			}
			raw = strings.TrimSuffix(string(data), "\n")
			if len(data) > 0 && data[len(data)-1] == '\n' {
				raw = strings.TrimSuffix(raw, "\r")
			}
			set = true
		}
		if !set {
			raw, set = meta.Tag.Lookup("default")
		}
		if meta.Tag.Get("required") == "true" && (!set || raw == "") {
			*problems = append(*problems, fmt.Errorf("%s: required value is missing", name))
			continue
		}
		if !set {
			continue
		}
		if err := setValue(field, raw); err != nil {
			*problems = append(*problems, fmt.Errorf("%s: %w", name, err))
		}
	}
}

func setValue(field reflect.Value, raw string) error {
	if field.Type() == reflect.TypeFor[time.Duration]() {
		v, err := time.ParseDuration(raw)
		if err != nil {
			return errors.New("expected a duration")
		}
		field.SetInt(int64(v))
		return nil
	}
	switch field.Kind() {
	case reflect.String:
		field.SetString(raw)
	case reflect.Bool:
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return errors.New("expected a boolean")
		}
		field.SetBool(v)
	case reflect.Int, reflect.Int64:
		v, err := strconv.ParseInt(raw, 10, field.Type().Bits())
		if err != nil {
			return errors.New("expected an integer")
		}
		field.SetInt(v)
	case reflect.Float64:
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return errors.New("expected a number")
		}
		field.SetFloat(v)
	case reflect.Slice:
		if field.Type().Elem().Kind() != reflect.String {
			return errors.New("unsupported field type")
		}
		parts := []string{}
		if raw != "" {
			parts = strings.Split(raw, ",")
		}
		list := reflect.MakeSlice(field.Type(), len(parts), len(parts))
		for i, part := range parts {
			list.Index(i).SetString(strings.TrimSpace(part))
		}
		field.Set(list)
	default:
		return errors.New("unsupported field type")
	}
	return nil
}
