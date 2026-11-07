package modules

// APIKeysNextSteps returns the manual wiring steps for an existing app.
func APIKeysNextSteps() string {
	return `Next: declare plans in spinup.toml, for example:
 [apikeys]
 prefix = "jostar"
 default_plan = "free"
 max_keys = 10
 [apikeys.plans.free]
 per_minute = 60
 per_day = 1000
 [apikeys.plans.pro]
 per_minute = 600
 per_day = 100000
Next: run spinup g apikeys.
Next: in api/cmd/api/main.go, import:
 appapikeys "<module>/api/internal/apikeys"
 "github.com/stopyellingatme/spinup/kit/x/apikeys"
 "time"
Next: before auth.New, load APIKEYS_FLUSH and start the store:
 var apikeysEnv struct { Flush time.Duration ` + "`env:\"APIKEYS_FLUSH\" default:\"5s\"`" + ` }
 if err := config.Load(&apikeysEnv); err != nil { return err }
 keyStore, err := apikeys.NewStore(pool, appapikeys.Config)
 if err != nil { return err }
 if err := keyStore.Start(ctx, apikeysEnv.Flush); err != nil { return err }
Next: add auth.WithBearer(keyStore) to auth.New.
Next: add these options to app.New after the auth and flags middleware:
 app.WithMiddleware(keyStore.Middleware()),
 app.WithRoutes(keyStore.Routes(authn)),
 app.WithCommand("apikeys", keyStore.Command()),
 app.WithReadiness("apikeys", keyStore.Ready),
 app.WithShutdown(keyStore.Close),
Next: in the user menu, link every user to /settings/api-keys.
 Link the admin role from apikeys.gen.ts to /admin/apikeys.
Next: run spinup db migrate before starting the new binary.
Next: run spinup doctor and inspect the apikeys-drift and apikeys-wired checks.
`
}
