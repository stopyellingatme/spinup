package testkit

import (
	"strings"
	"testing"
)

func TestDBName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		test string
		pid  int
		n    int64
		want string
	}{
		{"TestTodos", 42, 1, "t_testtodos_42_1"},
		{"TestTodos/create_one", 42, 7, "t_testtodos_create_one_42_7"},
		{"Test--Weird  Chars!!", 1, 1, "t_test_weird_chars_1_1"},
		{"/", 9, 3, "t__9_3"},
		{strings.Repeat("X", 200), 4194304, 999999999, "t_" + strings.Repeat("x", 32) + "_4194304_999999999"},
	}
	for _, tt := range tests {
		got := dbName(tt.test, tt.pid, tt.n)
		if got != tt.want {
			t.Errorf("dbName(%q) = %q, want %q", tt.test, got, tt.want)
		}
		if len(got) > maxIdentLen {
			t.Errorf("dbName(%q) is %d bytes", tt.test, len(got))
		}
	}
}

func TestWithDatabase(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"postgres://u:p@h:5432/app?sslmode=disable", "postgres://u:p@h:5432/t_x?sslmode=disable"},
		{"postgresql://u@h/app", "postgresql://u@h/t_x"},
		{"host=h user=u dbname=app", "host=h user=u dbname=app dbname=t_x"},
	}
	for _, tt := range tests {
		got, err := withDatabase(tt.in, "t_x")
		if err != nil || got != tt.want {
			t.Errorf("withDatabase(%q) = %q, %v, want %q", tt.in, got, err, tt.want)
		}
	}
}
