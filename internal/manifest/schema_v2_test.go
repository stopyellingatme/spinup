package manifest

import (
	"reflect"
	"testing"
)

func TestSchemaV1MigratesPacks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, lock string
		want       []string
	}{
		{"no modules", `{"schema":1}`, []string{"base", "api"}},
		{"modules", `{"schema":1,"answers":{"modules":["ci","web","ci"]}}`, []string{"base", "api", "ci"}},
		{"kept", `{"schema":1,"packs":["base"]}`, []string{"base"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, err := Parse([]byte(tc.lock))
			if err != nil {
				t.Fatal(err)
			}
			if l.Schema != 2 || !reflect.DeepEqual(l.Packs, tc.want) {
				t.Fatalf("schema = %d, packs = %v, want 2, %v", l.Schema, l.Packs, tc.want)
			}
		})
	}
}

func TestPacksValidation(t *testing.T) {
	t.Parallel()
	for _, packs := range [][]string{{""}, {"base", "base"}} {
		l := New()
		l.Packs = packs
		if err := l.Validate(); err == nil {
			t.Fatalf("Validate accepted packs %q", packs)
		}
	}
}
