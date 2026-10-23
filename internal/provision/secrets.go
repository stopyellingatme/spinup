package provision

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/url"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/deploy"
	"github.com/stopyellingatme/spinup/internal/secrets"
)

const (
	secretPostgresPassword = "POSTGRES_PASSWORD"
	secretDatabaseURL      = "DATABASE_URL"
	secretGrafanaPassword  = "GRAFANA_ADMIN_PASSWORD"
)

func randomSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", failWrap("make a random secret", err, "check the system random source")
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func databaseURL(password string) string {
	return "postgres://app:" + password + "@postgres:5432/app?sslmode=disable"
}

// passwordFromURL reads the password of a DATABASE_URL that uses the user app.
func passwordFromURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return "", fail("read DATABASE_URL", "it is not a URL with a user and a password", "set POSTGRES_PASSWORD with spinup secrets set, or correct DATABASE_URL")
	}
	pw, ok := u.User.Password()
	if u.User.Username() != "app" || !ok || pw == "" {
		return "", fail("read DATABASE_URL", "the user is not app, or the URL has no password", "set POSTGRES_PASSWORD with spinup secrets set, or correct DATABASE_URL")
	}
	return pw, nil
}

// missingSecrets returns the names the deploy needs and the environment does not have yet.
func missingSecrets(need []string, have map[string]string) []string {
	var out []string
	for _, n := range need {
		if have[n] == "" {
			out = append(out, n)
		}
	}
	return out
}

// makeValues makes the values for the missing names. It never replaces a value.
func makeValues(missing []string, have map[string]string) (map[string]string, error) {
	out := map[string]string{}
	pw := have[secretPostgresPassword]
	if pw == "" && have[secretDatabaseURL] != "" {
		var err error
		if pw, err = passwordFromURL(have[secretDatabaseURL]); err != nil {
			return nil, err
		}
	}
	for _, name := range missing {
		switch name {
		case secretPostgresPassword, secretDatabaseURL:
			if pw == "" {
				var err error
				if pw, err = randomSecret(); err != nil {
					return nil, err
				}
			}
			if name == secretPostgresPassword {
				out[name] = pw
			} else {
				out[name] = databaseURL(pw)
			}
		case secretGrafanaPassword:
			v, err := randomSecret()
			if err != nil {
				return nil, err
			}
			out[name] = v
		}
	}
	return out, nil
}

// ensureSecrets creates the secrets file and the missing values (ADR 0017 §9).
// It prints names, never values.
func (r *run) ensureSecrets(ctx context.Context) error {
	so := secrets.Options{Dir: r.root, Env: r.env, RecoveryRecipient: r.o.RecoveryRecipient, NoRecovery: r.o.NoRecovery, Out: r.o.Out, ErrOut: r.o.ErrOut}
	if !r.d.Secrets.Exists(r.root, r.env) {
		if r.o.RecoveryRecipient == "" && !r.o.NoRecovery {
			return fail("create the secrets", "a recovery recipient is required", "pass --recovery-recipient age1... or --no-recovery")
		}
		if err := r.d.Secrets.Init(ctx, so); err != nil {
			return failWrap("create the secrets file", err, "check sops and age-keygen, or run spinup doctor")
		}
		r.say("Created secrets/%s.enc.yaml.", r.env)
	}
	have, err := r.d.Secrets.Decrypt(ctx, r.root, r.env)
	if err != nil {
		return failWrap("read the secrets", err, "check the environment key, or run spinup secrets get")
	}
	need, err := deploy.RequiredSecrets(r.root, r.cfg)
	if err != nil {
		return failWrap("list the required secrets", err, "check spinup.toml and the monitoring settings")
	}
	missing := missingSecrets(need, have)
	if len(missing) == 0 {
		r.say("The secrets are complete.")
		return nil
	}
	values, err := makeValues(missing, have)
	if err != nil {
		return err
	}
	var made []string
	for _, name := range missing { // The order of the needed names is stable.
		v, ok := values[name]
		if !ok {
			continue
		}
		if err := r.d.Secrets.Set(ctx, so, name, v); err != nil {
			return failWrap("store the secret "+name, err, "run the command again")
		}
		made = append(made, name)
	}
	r.say("Made the secrets: %s", strings.Join(made, ", "))
	if left := slices.DeleteFunc(slices.Clone(missing), func(n string) bool { _, ok := values[n]; return ok }); len(left) > 0 {
		r.warn("Warning: spinup does not make these secrets: %s", strings.Join(left, ", "))
	}
	return nil
}
