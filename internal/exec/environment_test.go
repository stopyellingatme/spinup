package exec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvironmentChild(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "spinup-environment-child" {
		return
	}
	if err := json.NewEncoder(os.Stdout).Encode(os.Environ()); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestReplaceEnvLookup(t *testing.T) {
	t.Parallel()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		env   []string
		found bool
	}{
		{"no PATH", nil, false},
		{"explicit PATH", []string{"PATH=" + filepath.Dir(binary)}, true},
		{"empty PATH", []string{"PATH="}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := lookPath(Cmd{Name: filepath.Base(binary), Env: tc.env, ReplaceEnv: true})
			if (err == nil) != tc.found {
				t.Fatalf("lookup error = %v, want found=%v", err, tc.found)
			}
		})
	}
}

func TestOSRunnerEnvironment(t *testing.T) {
	t.Setenv("SPINUP_PARENT_SECRET", "parent-only")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		replace bool
		env     []string
	}{
		{"inherit", false, []string{"SPINUP_CHILD_VALUE=explicit"}},
		{"replace", true, []string{"SPINUP_CHILD_VALUE=explicit"}},
		{"empty", true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var env []string
			err := RunJSON(t.Context(), OSRunner{}, Cmd{
				Name: binary,
				Args: []string{"-test.run=^TestEnvironmentChild$", "spinup-environment-child"},
				Env:  tc.env, ReplaceEnv: tc.replace,
			}, &env)
			if err != nil {
				t.Fatal(err)
			}
			values := strings.Join(env, "\n")
			inherited := strings.Contains(values, "SPINUP_PARENT_SECRET=")
			if inherited == tc.replace {
				t.Fatalf("replacement=%v inherited parent secret=%v", tc.replace, inherited)
			}
			if strings.Contains(values, "SPINUP_CHILD_VALUE=explicit") != (len(tc.env) > 0) {
				t.Fatal("child environment differs from explicit values")
			}
		})
	}
}
