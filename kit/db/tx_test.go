package db_test

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

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

func TestInTxGoexit(t *testing.T) {
	t.Parallel()
	for _, canceled := range []bool{false, true} {
		name := "active context"
		if canceled {
			name = "canceled context"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			pool := testkit.DB(t)
			if _, err := pool.Exec(ctx, "CREATE TABLE items (id int PRIMARY KEY)"); err != nil {
				t.Fatal(err)
			}
			transactions := make(chan pgx.Tx, 1)
			// Release a leaked transaction so a regression cannot block testkit cleanup.
			t.Cleanup(func() {
				select {
				case tx := <-transactions:
					cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cleanupCancel()
					_ = tx.Rollback(cleanupCtx)
				default:
				}
			})
			txCtx, txCancel := context.WithCancel(ctx)
			defer txCancel()
			exited := make(chan struct{})
			returned := make(chan error, 1)
			go func() {
				defer close(exited)
				returned <- db.InTx(txCtx, pool, func(tx pgx.Tx) error {
					transactions <- tx
					if _, err := tx.Exec(txCtx, "INSERT INTO items VALUES (1)"); err != nil {
						return err
					}
					if canceled {
						txCancel()
					}
					runtime.Goexit()
					return nil
				})
			}()
			select {
			case <-exited:
			case <-ctx.Done():
				t.Fatal("transaction goroutine did not exit")
			}
			select {
			case err := <-returned:
				t.Fatalf("InTx returned instead of exiting: %v", err)
			default:
			}
			connString := pool.Config().ConnString()
			closed := make(chan struct{})
			go func() { pool.Close(); close(closed) }()
			select {
			case <-closed:
			case <-time.After(3 * time.Second):
				t.Fatal("pool.Close blocked after runtime.Goexit")
			}
			conn, err := pgx.Connect(ctx, connString)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
			var count int
			if err := conn.QueryRow(ctx, "SELECT count(*) FROM items").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("rows after runtime.Goexit = %d, want 0", count)
			}
		})
	}
}
