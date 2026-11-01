package resource

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/gen/fields"
	"github.com/stopyellingatme/spinup/internal/project"
)

func TestHistoryRolesFromGet(t *testing.T) {
	t.Parallel()
	name, err := fields.ParseName("note")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, get string
		want      []string
		bad       bool
	}{
		{name: "unrestricted", get: `return nil,nil`},
		{name: "declared constants", get: `if !user.HasRole(roles.Manager, roles.Staff) {return nil,nil}; return nil,nil`, want: []string{"manager", "staff"}},
		{name: "comments do not grant access", get: `/* user.HasRole(roles.Manager) */ return nil,nil`},
		{name: "unknown constant", get: `if !user.HasRole(roles.Unknown) {return nil,nil}; return nil,nil`, bad: true},
		{name: "literal requires manual review", get: `if !user.HasRole("manager") {return nil,nil}; return nil,nil`, bad: true},
		{name: "positive guard", get: `if user.HasRole(roles.Manager) {return nil,nil}; return nil,nil`, bad: true},
		{name: "combined guards", get: `if !user.HasRole(roles.Manager) {return nil,nil}; if !user.HasRole(roles.Staff) {return nil,nil}; return nil,nil`, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			source := []byte("package handlers\nfunc (s *Server) GetNote() (any,error) {" + tc.get + "}\nfunc (s *Server) ListNotes() {user.HasRole(roles.Staff)}")
			got, err := getHistoryRoles(source, Spec{Name: name}, &project.Config{})
			if tc.bad {
				if err == nil || !strings.Contains(err.Error(), "Try:") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("roles = %v, error = %v, want %v", got, err, tc.want)
			}
		})
	}
}
