package apikeys

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/metric"

	"github.com/stopyellingatme/spinup/kit/audit"
	"github.com/stopyellingatme/spinup/kit/auth"
)

var (
	// ErrUnknownPlan means the requested plan has no declaration.
	ErrUnknownPlan = &storeError{message: "apikeys: the plan is not declared.\nTry: use a declared plan."}
	// ErrKeyNotFound means the key is missing, revoked, or owned by another user.
	ErrKeyNotFound = &storeError{message: "apikeys: the key was not found.\nTry: list the active keys and use their ID."}
	// ErrUserNotFound means no user has the requested ID or email.
	ErrUserNotFound = &storeError{message: "apikeys: the user was not found.\nTry: run user list and use an existing account."}
	// ErrTooManyKeys means the user already has MaxKeys active keys.
	ErrTooManyKeys = &storeError{message: "apikeys: the user has the maximum number of keys.\nTry: revoke a key first."}
	// ErrInvalidKey means the key name, scope, or expiry is invalid.
	ErrInvalidKey = &storeError{message: "apikeys: the key input is invalid.\nTry: correct the key name, scope, or expiry."}
)

type storeError struct {
	message string
	cause   error
}

func (e *storeError) Error() string { return e.message }
func (e *storeError) Unwrap() error { return e.cause }
func storeFailure(action string, err error) error {
	for _, sentinel := range []error{ErrUnknownPlan, ErrKeyNotFound, ErrUserNotFound, ErrTooManyKeys, ErrInvalidKey} {
		if errors.Is(err, sentinel) {
			return err
		}
	}
	return &storeError{cause: err, message: fmt.Sprintf("apikeys: cannot %s: %v\nTry: check the database connection and run spinup db migrate up, then retry.", action, err)}
}

// Store manages keys, plans, quotas, and pending usage. It is safe for concurrent use.
type Store struct {
	pool         *pgxpool.Pool
	cfg          Config
	logger       *slog.Logger
	now          func() time.Time
	rpcPath      string
	counter      metric.Int64Counter
	mu           sync.Mutex
	warned       map[string]bool
	lastErr      error
	started      bool
	closed       bool
	closeFlushed bool
	cancel       context.CancelFunc
	done         chan struct{}
	closeMu      sync.Mutex
	flushMu      sync.Mutex
	quotaMu      sync.Mutex
	minute       map[int64]int64
	days         map[userDay]*dayUsage
	pending      map[keyDay]int64
}

var _ auth.Bearer = (*Store)(nil)

type storeOptions struct {
	logger *slog.Logger
	now    func() time.Time
	rpc    string
}

// StoreOption configures NewStore.
type StoreOption func(*storeOptions)

// WithLogger selects the store logger. The default is slog.Default().
func WithLogger(l *slog.Logger) StoreOption { return func(o *storeOptions) { o.logger = l } }

// WithClock selects the clock used for expiry and quota calculations.
func WithClock(now func() time.Time) StoreOption { return func(o *storeOptions) { o.now = now } }

// WithRPCPath lets a read key send POST to path (an exact match). The handler
// at path must enforce the read scope itself. The quotas still apply.
func WithRPCPath(path string) StoreOption { return func(o *storeOptions) { o.rpc = path } }

// NewStore validates cfg without accessing the database or starting a loop.
func NewStore(pool *pgxpool.Pool, cfg Config, opts ...StoreOption) (*Store, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if pool == nil {
		return nil, &storeError{message: "apikeys: cannot create the store without a database pool.\nTry: pass the application database pool."}
	}
	o := storeOptions{logger: slog.Default(), now: time.Now}
	for _, opt := range opts {
		opt(&o)
	}
	if o.logger == nil {
		o.logger = slog.Default()
	}
	if o.now == nil {
		o.now = time.Now
	}
	cfg.Plans = maps.Clone(cfg.Plans)
	s := &Store{pool: pool, cfg: cfg, logger: o.logger, now: o.now, rpcPath: o.rpc, warned: map[string]bool{}, minute: map[int64]int64{}, days: map[userDay]*dayUsage{}, pending: map[keyDay]int64{}, lastErr: &storeError{message: "apikeys: usage has not been flushed.\nTry: start the store and check the database connection."}}
	s.initCounter()
	return s, nil
}

func (s *Store) resolvePlan(ctx context.Context, name string) (string, Plan) {
	if p, ok := s.cfg.Plans[name]; ok {
		return name, p
	}
	if name != "" {
		s.mu.Lock()
		first := !s.warned[name]
		s.warned[name] = true
		s.mu.Unlock()
		if first {
			s.logger.WarnContext(ctx, "apikeys: a stored plan has no declaration. Try: declare the plan or assign a declared plan.", "plan", name)
		}
	}
	return s.cfg.DefaultPlan, s.cfg.Plans[s.cfg.DefaultPlan]
}

const keyColumns = `id, user_id, name, scope, display, created_at, last_used_at, expires_at, revoked_at`

func scanKey(row pgx.Row) (k Key, err error) {
	err = row.Scan(&k.ID, &k.UserID, &k.Name, &k.Scope, &k.Display, &k.CreatedAt, &k.LastUsedAt, &k.ExpiresAt, &k.RevokedAt)
	return k, err
}

// AuthenticateBearer reads the key and its user in one query, without a token cache.
func (s *Store) AuthenticateBearer(ctx context.Context, token string) (context.Context, auth.User, error) {
	secret, ok := strings.CutPrefix(token, s.cfg.Prefix+"_")
	if !ok || len(secret) != 43 {
		return ctx, auth.User{}, auth.ErrBearerInvalid
	}
	hash := sha256.Sum256([]byte(token))
	var k Key
	var u auth.User
	var name string
	err := s.pool.QueryRow(ctx, `SELECT k.id,k.user_id,k.name,k.scope,k.display,k.created_at,k.last_used_at,k.expires_at,k.revoked_at,u.email,u.role,COALESCE(p.plan,'')
 FROM api_keys k JOIN users u ON u.id=k.user_id LEFT JOIN api_key_plans p ON p.user_id=u.id
 WHERE k.token_hash=$1 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>$2)`, hash[:], s.now()).Scan(&k.ID, &k.UserID, &k.Name, &k.Scope, &k.Display, &k.CreatedAt, &k.LastUsedAt, &k.ExpiresAt, &k.RevokedAt, &u.Email, &u.Role, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return ctx, auth.User{}, auth.ErrBearerInvalid
	}
	if err != nil {
		return ctx, auth.User{}, storeFailure("authenticate the key", err)
	}
	u.ID = k.UserID
	name, plan := s.resolvePlan(ctx, name)
	return context.WithValue(ctx, requestKey{}, Request{Key: k, PlanName: name, Plan: plan}), u, nil
}

// Create stores a new key and returns its token once. The user lock serializes the key limit.
func (s *Store) Create(ctx context.Context, userID int64, in CreateInput) (Key, string, error) {
	if err := validateCreate(in); err != nil {
		return Key{}, "", err
	}
	token, hash, display, err := newToken(s.cfg.Prefix)
	if err != nil {
		return Key{}, "", err
	}
	var k Key
	err = audit.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id int64
		if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrUserNotFound
			}
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE user_id=$1 AND revoked_at IS NULL`, userID).Scan(&count); err != nil {
			return err
		}
		if count >= s.cfg.MaxKeys {
			return ErrTooManyKeys
		}
		var expires *time.Time
		if in.ExpiresIn > 0 {
			t := s.now().Add(in.ExpiresIn)
			expires = &t
		}
		var err error
		k, err = scanKey(tx.QueryRow(ctx, `INSERT INTO api_keys(user_id,name,scope,token_hash,display,expires_at) VALUES($1,$2,$3,$4,$5,$6) RETURNING `+keyColumns, userID, in.Name, in.Scope, hash[:], display, expires))
		return err
	})
	if err != nil {
		return Key{}, "", storeFailure("create the key", err)
	}
	return k, token, nil
}

// List returns the user's keys that are not revoked, ordered by ID.
func (s *Store) List(ctx context.Context, userID int64) ([]Key, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+keyColumns+` FROM api_keys WHERE user_id=$1 AND revoked_at IS NULL ORDER BY id`, userID)
	if err != nil {
		return nil, storeFailure("list the keys", err)
	}
	defer rows.Close()
	out := []Key{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, storeFailure("read a key", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, storeFailure("list the keys", err)
	}
	return out, nil
}

// Revoke marks a key revoked. A userID of zero permits a key of any user.
func (s *Store) Revoke(ctx context.Context, userID, keyID int64) error {
	err := audit.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE api_keys SET revoked_at=$3 WHERE id=$1 AND ($2::bigint=0 OR user_id=$2) AND revoked_at IS NULL`, keyID, userID, s.now())
		if err == nil && tag.RowsAffected() == 0 {
			return ErrKeyNotFound
		}
		return err
	})
	if err != nil {
		return storeFailure("revoke the key", err)
	}
	return nil
}

// PlanOf returns the effective plan of an existing user.
func (s *Store) PlanOf(ctx context.Context, userID int64) (string, Plan, error) {
	var name string
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(p.plan,'') FROM users u LEFT JOIN api_key_plans p ON p.user_id=u.id WHERE u.id=$1`, userID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", Plan{}, ErrUserNotFound
	}
	if err != nil {
		return "", Plan{}, storeFailure("read the plan", err)
	}
	name, p := s.resolvePlan(ctx, name)
	return name, p, nil
}

// SetPlan assigns a declared plan to the user with the normalized email.
func (s *Store) SetPlan(ctx context.Context, email, plan string) error {
	if _, ok := s.cfg.Plans[plan]; !ok {
		return &storeError{cause: ErrUnknownPlan, message: fmt.Sprintf("apikeys: the plan %q is not declared.\nTry: use one of %s.", plan, strings.Join(s.cfg.PlanNames(), ", "))}
	}
	err := audit.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO api_key_plans(user_id,plan) SELECT id,$2 FROM users WHERE email=$1 ON CONFLICT(user_id) DO UPDATE SET plan=excluded.plan,updated_at=now()`, strings.ToLower(strings.TrimSpace(email)), plan)
		if err == nil && tag.RowsAffected() == 0 {
			return ErrUserNotFound
		}
		return err
	})
	if err != nil {
		return storeFailure("assign the plan", err)
	}
	return nil
}

// Usage returns stored counts for the most recent days, including today, ordered by day and key.
func (s *Store) Usage(ctx context.Context, userID int64, days int) ([]UsageDay, error) {
	if days < 1 {
		return nil, &storeError{message: "apikeys: cannot read usage for fewer than one day.\nTry: use a positive number of days."}
	}
	cutoff := utcDay(s.now()).AddDate(0, 0, -(days - 1))
	rows, err := s.pool.Query(ctx, `SELECT g.key_id,to_char(g.day,'YYYY-MM-DD'),g.count FROM api_key_usage g JOIN api_keys k ON k.id=g.key_id WHERE k.user_id=$1 AND g.day >= $2::date AND g.day <= $3::date ORDER BY g.day,g.key_id`, userID, cutoff, utcDay(s.now()))
	if err != nil {
		return nil, storeFailure("read the usage", err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[UsageDay])
	if err != nil {
		return nil, storeFailure("read the usage", err)
	}
	return out, nil
}

// Accounts returns users with a key or a plan row, ordered by email and ID.
func (s *Store) Accounts(ctx context.Context) ([]Account, error) {
	rows, err := s.pool.Query(ctx, `SELECT u.id,u.email,u.role,COALESCE(p.plan,''),COALESCE((SELECT sum(g.count) FROM api_key_usage g JOIN api_keys k ON k.id=g.key_id WHERE k.user_id=u.id AND g.day=$1::date),0)::bigint FROM users u LEFT JOIN api_key_plans p ON p.user_id=u.id WHERE p.user_id IS NOT NULL OR EXISTS(SELECT 1 FROM api_keys k WHERE k.user_id=u.id) ORDER BY u.email,u.id`, utcDay(s.now()))
	if err != nil {
		return nil, storeFailure("list the accounts", err)
	}
	out := []Account{}
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.UserID, &a.Email, &a.Role, &a.Plan, &a.Today); err != nil {
			rows.Close()
			return nil, storeFailure("read an account", err)
		}
		out = append(out, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, storeFailure("list the accounts", err)
	}
	ids := make([]int64, 0, len(out))
	indexes := make(map[int64]int, len(out))
	for i := range out {
		a := &out[i]
		a.Plan, _ = s.resolvePlan(ctx, a.Plan)
		a.Keys = []Key{}
		ids = append(ids, a.UserID)
		indexes[a.UserID] = i
	}
	keys, err := s.pool.Query(ctx, `SELECT `+keyColumns+` FROM api_keys WHERE user_id=ANY($1::bigint[]) AND revoked_at IS NULL ORDER BY id`, ids)
	if err != nil {
		return nil, storeFailure("list the account keys", err)
	}
	defer keys.Close()
	for keys.Next() {
		k, err := scanKey(keys)
		if err != nil {
			return nil, storeFailure("read an account key", err)
		}
		i := indexes[k.UserID]
		out[i].Keys = append(out[i].Keys, k)
	}
	if err := keys.Err(); err != nil {
		return nil, storeFailure("list the account keys", err)
	}
	return out, nil
}

// Prune deletes usage before the cutoff day and keys revoked before the cutoff time.
func (s *Store) Prune(ctx context.Context, olderThan time.Duration) (int64, error) {
	if olderThan <= 0 {
		return 0, &storeError{message: "apikeys: cannot prune with a nonpositive retention period.\nTry: use a positive retention period."}
	}
	cutoff := s.now().Add(-olderThan)
	var count int64
	err := audit.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM api_key_usage WHERE day<$1::date`, utcDay(cutoff))
		if err != nil {
			return err
		}
		count = tag.RowsAffected()
		tag, err = tx.Exec(ctx, `DELETE FROM api_keys WHERE revoked_at<$1`, cutoff)
		count += tag.RowsAffected()
		return err
	})
	if err != nil {
		return 0, storeFailure("prune the keys and usage", err)
	}
	return count, nil
}
