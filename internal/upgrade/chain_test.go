package upgrade

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stopyellingatme/spinup/internal/templates"
)

type chainSource map[string]*templates.Version

func (s chainSource) Versions(context.Context) ([]string, error) {
	var out []string
	for v := range s {
		out = append(out, v)
	}
	return out, nil
}
func (s chainSource) Load(_ context.Context, v string) (*templates.Version, error) {
	if item, ok := s[v]; ok {
		return item, nil
	}
	return nil, templates.ErrVersionUnavailable
}

func TestChain(t *testing.T) {
	t.Parallel()
	source := chainSource{}
	for _, v := range []string{"0.1.0", "0.2.0", "0.2.7", "0.3.2", "0.4.0", "1.0.0"} {
		source[v] = &templates.Version{Version: v}
	}
	for _, tc := range []struct {
		name, from, to string
		versions, want []string
		fail           bool
	}{
		{name: "minor patches", from: "0.1.0", to: "0.4.0", want: []string{"0.2.7", "0.3.2", "0.4.0"}},
		{name: "target patch", from: "0.1.0", to: "0.2.0", want: []string{"0.2.0"}},
		{name: "same", from: "0.1.0", to: "0.1.0"},
		{name: "downgrade", from: "0.2.0", to: "0.1.0", fail: true},
		{name: "default", from: "0.4.0", want: []string{"1.0.0"}},
		{name: "missing target", from: "0.1.0", to: "0.5.0", fail: true},
		{name: "bad from", from: "bad", to: "0.2.0", fail: true},
		{name: "default loadable", from: "0.1.0", versions: []string{"0.9.0", "0.2.0"}, want: []string{"0.2.0"}},
		{name: "invalid metadata", from: "0.1.0", to: "0.2.0+bad$", fail: true},
		{name: "provided versions", from: "0.1.0", to: "0.4.0", versions: []string{"0.3.2", "0.2.0"}, want: []string{"0.2.0", "0.3.2", "0.4.0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Chain(source, tc.from, tc.to, tc.versions)
			if (err != nil) != tc.fail {
				t.Fatalf("Chain error=%v", err)
			}
			if !tc.fail && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestChainUnavailable(t *testing.T) {
	t.Parallel()
	_, err := Chain(chainSource{}, "0.1.0", "0.2.0", nil)
	if !errors.Is(err, templates.ErrVersionUnavailable) {
		t.Fatalf("got %v", err)
	}
}

func TestChainAlreadyCurrentWithoutSource(t *testing.T) {
	t.Parallel()
	got, err := Chain(nil, "0.2.0", "0.2.0", nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("chain=%v error=%v", got, err)
	}
}
