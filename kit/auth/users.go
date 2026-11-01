package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/stopyellingatme/spinup/kit/db"
)

var (
	// ErrUnknownRole indicates that the requested role is not declared.
	ErrUnknownRole error = &userError{message: "the role is not declared.\nTry: use a declared role, or update [auth] and run spinup g roles."}
	// ErrUserNotFound indicates that no account has the requested email.
	ErrUserNotFound error = &userError{message: "no account has this email.\nTry: run \"user list\" to see the accounts."}
	// ErrLastAdmin indicates that a change would remove the last administrator.
	ErrLastAdmin error = &userError{message: "the change would remove the last administrator.\nTry: give the administrator role to another account first."}
)

// userError preserves sentinel identity while allowing configured role names.
type userError struct {
	cause   error
	message string
}

func (e *userError) Error() string { return e.message }
func (e *userError) Unwrap() error { return e.cause }

func (a *Auth) checkRole(role string) error {
	if a.roles.Valid(role) {
		return nil
	}
	return &userError{cause: ErrUnknownRole, message: fmt.Sprintf("role %q is not declared. Declared roles: %s.\nTry: use one of the declared roles, or add the role to [auth] in spinup.toml and run spinup g roles.", role, strings.Join(a.roles.Names, ", "))}
}

// lockUsers serializes account creation and administrator changes across instances.
func lockUsers(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('spinup.auth.users'))`)
	return err
}

// CreateUser creates an account for trusted code with an explicit declared role.
// It bypasses the registration policy. It never reveals a duplicate email.
func (a *Auth) CreateUser(ctx context.Context, email, password, role string) (User, error) {
	if err := a.checkRole(role); err != nil {
		return User{}, err
	}
	email, valid := normalizeEmail(email)
	if !valid {
		return User{}, errors.New("create user: the email address is invalid.\nTry: supply a valid email address")
	}
	if n := passwordLength(password); n < MinPasswordLength || n > MaxPasswordLength {
		return User{}, fmt.Errorf("create user: the password must have %d to %d characters.\nTry: supply a password within these limits", MinPasswordLength, MaxPasswordLength)
	}
	hash, err := a.hash(ctx, password)
	if err != nil {
		return User{}, fmt.Errorf("create user: cannot hash the password: %w\nTry: retry when a password hash slot is available", err)
	}
	tx, err := a.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return User{}, fmt.Errorf("create user: cannot start the transaction: %w\nTry: check the database connection", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if a.registration == RegistrationFirstUser {
		// Trusted code supplies its role and can create accounts after the first.
		if _, err := firstUser(ctx, tx); err != nil {
			return User{}, fmt.Errorf("create user: cannot lock and count the accounts: %w\nTry: check the database connection and retry", err)
		}
	}
	if err := a.rowEvent(ctx, tx, withOperatorOf(ctx, Event{Name: EventUserCreate, User: User{Email: email, Role: role}})); err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}
	var user User
	err = tx.QueryRow(ctx, `INSERT INTO users (email, password_hash, role) VALUES ($1, $2, $3) ON CONFLICT (email) DO NOTHING RETURNING id, email, role`, email, hash, role).Scan(&user.ID, &user.Email, &user.Role)
	if err != nil {
		// Neither the unique key nor a raw database error may reveal an email match.
		return User{}, errors.New("create user: the account could not be created.\nTry: check the account details and database availability, then retry")
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, errors.New("create user: the transaction could not be committed.\nTry: check the database connection and account details before retrying")
	}
	user.Role = a.effectiveRole(user.Role)
	return user, nil
}

// SetRole changes the role of the account with email. It preserves one administrator.
func (a *Auth) SetRole(ctx context.Context, email, role string) error {
	if err := a.checkRole(role); err != nil {
		return err
	}
	email, valid := normalizeEmail(email)
	if !valid {
		return errors.New("set role: the email address is invalid.\nTry: supply a valid email address")
	}
	tx, err := a.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("set role: cannot start the transaction: %w\nTry: check the database connection", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := lockUsers(ctx, tx); err != nil {
		return fmt.Errorf("set role: cannot lock the accounts: %w\nTry: check the database connection and retry", err)
	}
	var user User
	err = tx.QueryRow(ctx, `SELECT id, email, role FROM users WHERE email = $1 FOR UPDATE`, email).Scan(&user.ID, &user.Email, &user.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("set role: cannot read the account: %w\nTry: check the database schema and connection", err)
	}
	if user.Role == a.roles.Admin && role != a.roles.Admin {
		var count int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE role = $1`, a.roles.Admin).Scan(&count); err != nil {
			return fmt.Errorf("set role: cannot count the administrators: %w\nTry: check the database schema and connection", err)
		}
		if count == 1 {
			return &userError{cause: ErrLastAdmin, message: fmt.Sprintf("the change would remove the last account with role %q.\nTry: give the role %q to another account first.", a.roles.Admin, a.roles.Admin)}
		}
	}
	if err := a.rowEvent(ctx, tx, withOperatorOf(ctx, Event{Name: EventRoleChange, User: user})); err != nil {
		return fmt.Errorf("set role: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET role = $2, updated_at = now() WHERE id = $1`, user.ID, role); err != nil {
		return fmt.Errorf("set role: cannot update the account: %w\nTry: check the database schema and connection", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("set role: cannot commit the transaction: %w\nTry: check the database connection and retry", err)
	}
	return nil
}

// ListUsers returns accounts ordered by email, with their stored roles.
// Empty roles remain empty so operators can identify accounts that need backfill.
func (a *Auth) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := a.pool.Query(ctx, `SELECT `+userColumns+` FROM users ORDER BY email, id`)
	if err != nil {
		return nil, fmt.Errorf("list users: cannot read the accounts: %w\nTry: check the database schema and connection", err)
	}
	users, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (User, error) {
		var user User
		err := row.Scan(&user.ID, &user.Email, &user.Role)
		return user, err
	})
	if err != nil {
		return nil, fmt.Errorf("list users: cannot read the account fields: %w\nTry: check the database schema and connection", err)
	}
	return users, nil
}

// BackfillRoles assigns the default role to accounts whose stored role is empty.
// It refuses to assign the administrator role implicitly.
func (a *Auth) BackfillRoles(ctx context.Context) (int64, error) {
	if a.roles.Default == a.roles.Admin {
		return 0, &userError{message: "default_role equals admin_role, so empty roles cannot be filled safely.\nTry: run \"user set-role\" for each account."}
	}
	var count int64
	err := db.InTx(ctx, a.pool, func(tx pgx.Tx) error {
		// One role_change event binds the actor for every account of the backfill.
		if err := a.rowEvent(ctx, tx, withOperatorOf(ctx, Event{Name: EventRoleChange, Reason: "backfill"})); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE users SET role = $1, updated_at = now() WHERE role = ''`, a.roles.Default)
		count = tag.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("backfill roles: cannot update the accounts: %w\nTry: check the database schema and connection", err)
	}
	return count, nil
}
