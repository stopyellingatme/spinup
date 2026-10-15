package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InTx runs fn in a transaction. It commits when fn returns nil. It rolls back
// when fn returns an error or panics. After a panic it rolls back and then
// panics again with the same value.
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db: begin transaction: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		rbErr := tx.Rollback(context.WithoutCancel(ctx))
		if rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return errors.Join(err, fmt.Errorf("db: roll back transaction: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: commit transaction: %w", err)
	}
	return nil
}
