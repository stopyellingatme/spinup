package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/stopyellingatme/spinup/kit/db"
	"github.com/stopyellingatme/spinup/kit/testkit"
)

func TestInTx(t *testing.T) {
	t.Parallel()
	errBoom := errors.New("boom")
	tests := []struct {
		name      string
		fn        func(pgx.Tx) error
		panics    bool
		wantErr   error
		wantCount int
	}{
		{
			name:      "commit on nil",
			fn:        func(pgx.Tx) error { return nil },
			wantCount: 1,
		},
		{
			name:    "rollback on error",
			fn:      func(pgx.Tx) error { return errBoom },
			wantErr: errBoom,
		},
		{
			name:   "rollback on panic",
			fn:     func(pgx.Tx) error { panic("kaboom") },
			panics: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := testkit.DB(t)
			if _, err := pool.Exec(ctx, "CREATE TABLE items (id int PRIMARY KEY)"); err != nil {
				t.Fatal(err)
			}
			run := func() (recovered any, err error) {
				defer func() { recovered = recover() }()
				err = db.InTx(ctx, pool, func(tx pgx.Tx) error {
					if _, err := tx.Exec(ctx, "INSERT INTO items VALUES (1)"); err != nil {
						return err
					}
					return tt.fn(tx)
				})
				return nil, err
			}
			recovered, err := run()
			if tt.panics && recovered != "kaboom" {
				t.Errorf("recovered %v, want the original panic value", recovered)
			}
			if !tt.panics && recovered != nil {
				t.Errorf("unexpected panic: %v", recovered)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("InTx error = %v, want %v", err, tt.wantErr)
			}
			var n int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM items").Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != tt.wantCount {
				t.Errorf("rows = %d, want %d", n, tt.wantCount)
			}
			// The pool must have no connection stuck in a transaction.
			if st := pool.Stat(); st.AcquiredConns() != 0 {
				t.Errorf("acquired connections = %d, want 0", st.AcquiredConns())
			}
		})
	}
}
