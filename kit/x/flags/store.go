package flags

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/audit"
	"github.com/stopyellingatme/spinup/kit/auth"
)

// Store validation errors can be checked with errors.Is.
var (
	// ErrUnknownKey means the key has no declaration.
	ErrUnknownKey = &storeError{message: "flags: the key is not declared.\nTry: declare the flag in spinup.toml and run spinup g flags."}
	// ErrUnknownRole means a role has no declaration.
	ErrUnknownRole = &storeError{message: "flags: the role is not declared.\nTry: use a declared role."}
	// ErrPercentage means the percentage is outside 0 to 100.
	ErrPercentage = &storeError{message: "flags: the percentage is not from 0 to 100.\nTry: use a whole number from 0 to 100."}
	// ErrUserNotFound means no account has the normalized email.
	ErrUserNotFound = &storeError{message: "flags: no account has this email.\nTry: run \"user list\" to see the accounts."}
)

type storeError struct {
	message string
	cause   error
}

func (e *storeError) Error() string { return e.message }
func (e *storeError) Unwrap() error { return e.cause }

func storeFailure(action string, err error) error {
	return &storeError{cause: err, message: fmt.Sprintf("flags: cannot %s: %v\nTry: check the database connection and run spinup db migrate up, then retry.", action, err)}
}

// Entry is one flag and its declarations, stored rules, and status.
type Entry struct {
	Key         string     `json:"key"`
	Description string     `json:"description"`
	Default     bool       `json:"default"`
	Public      bool       `json:"public"`
	Expires     string     `json:"expires,omitempty"`
	Enabled     bool       `json:"enabled"`
	Roles       []string   `json:"roles"`
	Percentage  int        `json:"percentage"`
	Users       []UserRule `json:"users"`
	Stale       bool       `json:"stale"`
	Expired     bool       `json:"expired"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// UserRule is an explicit on or off rule for one account.
type UserRule struct {
	UserID  int64  `json:"user_id"`
	Email   string `json:"email"`
	Enabled bool   `json:"enabled"`
}

// Store reads and changes the Postgres rules and refreshes a Flags provider.
// A Store is safe for concurrent use. Construct it with NewStore.
type Store struct {
	pool   *pgxpool.Pool
	flags  *Flags
	roles  auth.Roles
	logger *slog.Logger

	refreshMu     sync.Mutex
	refreshFailed bool
	mu            sync.Mutex
	lastErr       error
	started       bool
	synced        bool
	cancel        context.CancelFunc
	done          chan struct{}
}

type storeOptions struct{ logger *slog.Logger }

// StoreOption configures NewStore.
type StoreOption func(*storeOptions)

// WithStoreLogger sets the logger. The default is the provider logger.
func WithStoreLogger(l *slog.Logger) StoreOption {
	return func(o *storeOptions) { o.logger = l }
}

// NewStore returns a store without accessing the database or starting a runner.
// A nil pool or provider panics at construction.
func NewStore(pool *pgxpool.Pool, f *Flags, roles auth.Roles, opts ...StoreOption) *Store {
	if pool == nil || f == nil {
		panic("flags: NewStore needs a pool and a provider.\nTry: pass the database pool and the provider from flags.New.")
	}
	o := storeOptions{logger: f.Logger()}
	for _, opt := range opts {
		opt(&o)
	}
	if o.logger == nil {
		o.logger = f.Logger()
	}
	roles.Names = slices.Clone(roles.Names)
	return &Store{
		pool: pool, flags: f, roles: roles, logger: o.logger,
		lastErr: &storeError{message: "flags: the snapshot has not loaded.\nTry: run spinup db migrate up and refresh the flags."},
	}
}

// Sync inserts the missing declared keys with their defaults and never changes existing rows.
// It returns and warns about the stale keys, sorted.
func (s *Store) Sync(ctx context.Context) (stale []string, err error) {
	declared := s.flags.Declared()
	stale = []string{}
	err = audit.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, key := range declared.Keys() {
			if _, err := tx.Exec(ctx, `INSERT INTO feature_flags (key, enabled) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING`, key, declared[key].Default); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `SELECT key FROM feature_flags ORDER BY key`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				return err
			}
			if _, ok := declared[key]; !ok {
				stale = append(stale, key)
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, storeFailure("sync the declared flags", err)
	}
	for _, key := range stale {
		s.logger.WarnContext(ctx, fmt.Sprintf("flags: the row %q has no declaration. Try: run \"flags prune --yes\" to delete it, or declare [flags.%s] in spinup.toml.", key, key))
	}
	s.refreshAfterWrite(ctx)
	return stale, nil
}

// readEntries reads the rules and emails in one database snapshot.
func (s *Store) readEntries(ctx context.Context) (map[string]*Entry, time.Time, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, time.Time{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var loadedAt time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&loadedAt); err != nil {
		return nil, time.Time{}, err
	}
	entries := map[string]*Entry{}
	rows, err := tx.Query(ctx, `SELECT key, enabled, roles, percentage, updated_at FROM feature_flags ORDER BY key`)
	if err != nil {
		return nil, time.Time{}, err
	}
	for rows.Next() {
		e := &Entry{Users: []UserRule{}}
		if err := rows.Scan(&e.Key, &e.Enabled, &e.Roles, &e.Percentage, &e.UpdatedAt); err != nil {
			rows.Close()
			return nil, time.Time{}, err
		}
		e.Roles = append([]string{}, e.Roles...)
		slices.Sort(e.Roles)
		e.Roles = slices.Compact(e.Roles)
		e.UpdatedAt = e.UpdatedAt.UTC()
		entries[e.Key] = e
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	rows, err = tx.Query(ctx, `SELECT f.flag_key, f.user_id, u.email, f.enabled FROM feature_flag_users f JOIN users u ON u.id = f.user_id ORDER BY u.email, f.flag_key`)
	if err != nil {
		return nil, time.Time{}, err
	}
	for rows.Next() {
		var key string
		var u UserRule
		if err := rows.Scan(&key, &u.UserID, &u.Email, &u.Enabled); err != nil {
			rows.Close()
			return nil, time.Time{}, err
		}
		if e := entries[key]; e != nil {
			e.Users = append(e.Users, u)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, time.Time{}, err
	}
	return entries, loadedAt.UTC(), nil
}

// Load reads both tables in one REPEATABLE READ, READ ONLY transaction.
// LoadedAt is the transaction time from Postgres. Stale rules are included.
func (s *Store) Load(ctx context.Context) (*Snapshot, error) {
	entries, loadedAt, err := s.readEntries(ctx)
	if err != nil {
		return nil, storeFailure("load the snapshot", err)
	}
	rules := make(map[string]Rule, len(entries))
	for key, e := range entries {
		r := Rule{Enabled: e.Enabled, Roles: e.Roles, Percentage: e.Percentage, Users: make(map[int64]bool, len(e.Users))}
		for _, u := range e.Users {
			r.Users[u.UserID] = u.Enabled
		}
		rules[key] = r
	}
	return NewSnapshot(s.flags.Declared(), rules, loadedAt), nil
}

// Refresh loads and replaces the snapshot. A failed load keeps the last snapshot.
// Ready reports its error. A recovery logs once at info level.
func (s *Store) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	snap, err := s.Load(ctx)
	previousFailed := s.refreshFailed
	s.refreshFailed = err != nil
	if err == nil {
		s.flags.Replace(snap)
	}
	s.mu.Lock()
	s.lastErr = err
	s.mu.Unlock()
	if err != nil {
		s.logger.WarnContext(ctx, fmt.Sprintf("flags: cannot refresh the snapshot: %v. The last snapshot stays in use.", err))
		return err
	}
	if previousFailed {
		s.logger.InfoContext(ctx, "flags: the snapshot refresh succeeded.")
	}
	return nil
}

func (s *Store) refreshAfterWrite(ctx context.Context) { _ = s.Refresh(ctx) }

func (s *Store) checkKey(key string) error {
	declared := s.flags.Declared()
	if _, ok := declared[key]; ok {
		return nil
	}
	return &storeError{cause: ErrUnknownKey, message: fmt.Sprintf("flags: the flag %q is not declared. Declared flags: %s.\nTry: add [flags.%s] to spinup.toml and run spinup g flags.", key, strings.Join(declared.Keys(), ", "), key)}
}

func (s *Store) checkRoles(roles []string) ([]string, error) {
	roles = append([]string{}, roles...)
	slices.Sort(roles)
	roles = slices.Compact(roles)
	for _, role := range roles {
		if !s.roles.Valid(role) {
			names := slices.Sorted(slices.Values(s.roles.Names))
			return nil, &storeError{cause: ErrUnknownRole, message: fmt.Sprintf("flags: the role %q is not declared. Declared roles: %s.\nTry: use one of the declared roles, or add the role to [auth] in spinup.toml and run spinup g roles.", role, strings.Join(names, ", "))}
		}
	}
	return roles, nil
}

func checkPercentage(pct int) error {
	if pct < 0 || pct > 100 {
		return &storeError{cause: ErrPercentage, message: fmt.Sprintf("flags: the percentage %d is not from 0 to 100.\nTry: use a whole number from 0 to 100.", pct)}
	}
	return nil
}

// write commits a mutation with the actor, then attempts a local refresh.
func (s *Store) write(ctx context.Context, fn func(pgx.Tx) error) error {
	if err := audit.InTx(ctx, s.pool, fn); err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return err
		}
		return storeFailure("write the flag rules", err)
	}
	s.refreshAfterWrite(ctx)
	return nil
}

func (s *Store) update(ctx context.Context, key, sql string, args ...any) error {
	if err := s.checkKey(key); err != nil {
		return err
	}
	return s.write(ctx, func(tx pgx.Tx) error {
		if err := s.ensureRow(ctx, tx, key); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, sql, append([]any{key}, args...)...)
		return err
	})
}

// ensureRow inserts the declared row for key when it is missing, so a write
// never succeeds without effect after a failed Sync.
func (s *Store) ensureRow(ctx context.Context, tx pgx.Tx, key string) error {
	_, err := tx.Exec(ctx, `INSERT INTO feature_flags (key, enabled) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING`, key, s.flags.Declared()[key].Default)
	return err
}

// Enable turns on the global switch for key.
func (s *Store) Enable(ctx context.Context, key string) error {
	return s.update(ctx, key, `UPDATE feature_flags SET enabled = true, updated_at = now() WHERE key = $1`)
}

// Disable turns off the global switch for key.
func (s *Store) Disable(ctx context.Context, key string) error {
	return s.update(ctx, key, `UPDATE feature_flags SET enabled = false, updated_at = now() WHERE key = $1`)
}

// SetRoles replaces the allowed roles with sorted, unique declared roles.
func (s *Store) SetRoles(ctx context.Context, key string, roles []string) error {
	if err := s.checkKey(key); err != nil {
		return err
	}
	roles, err := s.checkRoles(roles)
	if err != nil {
		return err
	}
	return s.update(ctx, key, `UPDATE feature_flags SET roles = $2, updated_at = now() WHERE key = $1`, roles)
}

// SetPercentage replaces the signed-in percentage, from 0 to 100.
func (s *Store) SetPercentage(ctx context.Context, key string, percentage int) error {
	if err := s.checkKey(key); err != nil {
		return err
	}
	if err := checkPercentage(percentage); err != nil {
		return err
	}
	return s.update(ctx, key, `UPDATE feature_flags SET percentage = $2, updated_at = now() WHERE key = $1`, percentage)
}

// SetRule replaces the global switch, roles, and percentage. It ignores r.Users.
func (s *Store) SetRule(ctx context.Context, key string, r Rule) error {
	if err := s.checkKey(key); err != nil {
		return err
	}
	roles, err := s.checkRoles(r.Roles)
	if err != nil {
		return err
	}
	if err := checkPercentage(r.Percentage); err != nil {
		return err
	}
	return s.update(ctx, key, `UPDATE feature_flags SET enabled = $2, roles = $3, percentage = $4, updated_at = now() WHERE key = $1`, r.Enabled, roles, r.Percentage)
}

// SetUser creates or replaces the explicit rule for the account with email.
func (s *Store) SetUser(ctx context.Context, key, email string, enabled bool) error {
	return s.userWrite(ctx, key, email, func(tx pgx.Tx, id int64) error {
		_, err := tx.Exec(ctx, `INSERT INTO feature_flag_users (flag_key, user_id, enabled) VALUES ($1, $2, $3) ON CONFLICT (flag_key, user_id) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now()`, key, id, enabled)
		return err
	})
}

// ClearUser removes the explicit rule for the account with email.
// A missing rule succeeds. A missing account returns ErrUserNotFound.
func (s *Store) ClearUser(ctx context.Context, key, email string) error {
	return s.userWrite(ctx, key, email, func(tx pgx.Tx, id int64) error {
		_, err := tx.Exec(ctx, `DELETE FROM feature_flag_users WHERE flag_key = $1 AND user_id = $2`, key, id)
		return err
	})
}

func (s *Store) userWrite(ctx context.Context, key, email string, fn func(pgx.Tx, int64) error) error {
	if err := s.checkKey(key); err != nil {
		return err
	}
	email = strings.ToLower(strings.TrimSpace(email))
	return s.write(ctx, func(tx pgx.Tx) error {
		var id int64
		if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrUserNotFound
			}
			return err
		}
		if err := s.ensureRow(ctx, tx, key); err != nil {
			return err
		}
		return fn(tx, id)
	})
}

// Prune deletes stale flags and their user rules and returns the sorted keys.
func (s *Store) Prune(ctx context.Context) (deleted []string, err error) {
	deleted = []string{}
	err = s.write(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `DELETE FROM feature_flags WHERE NOT (key = ANY($1::text[])) RETURNING key`, s.flags.Declared().Keys())
		if err != nil {
			return err
		}
		deleted, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(deleted)
	return deleted, nil
}

// List reads the stored flags and includes missing declared flags with their defaults.
// It sorts flags by key and user rules by email. Roles and Users are never nil.
func (s *Store) List(ctx context.Context, now time.Time) ([]Entry, error) {
	entries, _, err := s.readEntries(ctx)
	if err != nil {
		return nil, storeFailure("list the flags", err)
	}
	declared := s.flags.Declared()
	for key, d := range declared {
		if entries[key] == nil {
			entries[key] = &Entry{Key: key, Enabled: d.Default, Roles: []string{}, Users: []UserRule{}}
		}
	}
	out := make([]Entry, 0, len(entries))
	for key, e := range entries {
		d, ok := declared[key]
		e.Description, e.Default, e.Public, e.Expires = d.Description, d.Default, d.Public, d.Expires
		e.Stale, e.Expired = !ok, d.Expired(now)
		out = append(out, *e)
	}
	slices.SortFunc(out, func(a, b Entry) int { return strings.Compare(a.Key, b.Key) })
	return out, nil
}

// Start syncs and refreshes once, then refreshes every interval until Close or cancellation.
// Database failures warn and do not fail Start. An interval below 1s or a second call fails.
func (s *Store) Start(ctx context.Context, interval time.Duration) error {
	if interval < time.Second {
		return &storeError{message: fmt.Sprintf("flags: the refresh interval %s is below 1s.\nTry: set FLAGS_REFRESH to 1s or more.", interval)}
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return &storeError{message: "flags: the refresh runner has already started.\nTry: call Start only once for each store."}
	}
	s.started = true
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel, s.done = cancel, make(chan struct{})
	s.mu.Unlock()
	timeout := min(interval, 5*time.Second)
	initialCtx, initialCancel := context.WithTimeout(runCtx, timeout)
	s.syncOnce(initialCtx)
	initialCancel()
	s.refreshWithTimeout(runCtx, timeout)
	go s.run(runCtx, interval, timeout)
	return nil
}

// syncOnce runs Sync and records success. The runner retries a failed Sync on
// each tick until it succeeds.
func (s *Store) syncOnce(ctx context.Context) {
	if _, err := s.Sync(ctx); err != nil {
		s.logger.WarnContext(ctx, "flags: cannot sync the declared flags", "error", err)
		return
	}
	s.mu.Lock()
	s.synced = true
	s.mu.Unlock()
}

func (s *Store) refreshWithTimeout(ctx context.Context, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_ = s.Refresh(ctx)
}

func (s *Store) run(ctx context.Context, interval, timeout time.Duration) {
	defer close(s.done)
	defer s.cancel()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			synced := s.synced
			s.mu.Unlock()
			if !synced {
				syncCtx, cancel := context.WithTimeout(ctx, timeout)
				s.syncOnce(syncCtx)
				cancel()
			}
			s.refreshWithTimeout(ctx, timeout)
		}
	}
}

// Close cancels the runner and waits for it to stop, subject to ctx.
func (s *Store) Close(ctx context.Context) error {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return &storeError{cause: ctx.Err(), message: fmt.Sprintf("flags: cannot stop the refresh runner: %v\nTry: allow more time for shutdown.", ctx.Err())}
	}
}

// Ready reports the last refresh error. It fails before the first successful load.
func (s *Store) Ready(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}
