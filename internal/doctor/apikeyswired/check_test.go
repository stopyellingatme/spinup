package apikeyswired

import (
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
)

func TestWiring(t *testing.T) {
	source := `package main
 func main() {
 keyStore, err := apikeys.NewStore(pool, cfg)
 keyStore.Start(ctx, flush)
 authn, err := auth.New(auth.WithBearer(keyStore))
 app.New(app.WithMiddleware(authn.Middleware()), app.WithMiddleware(keyStore.Middleware()), app.WithRoutes(keyStore.Routes(authn)), app.WithCommand("apikeys", keyStore.Command()), app.WithShutdown(keyStore.Close))
 }`
	if got := checkWiring([]byte(source)); got.Status != doctor.OK {
		t.Fatalf("%+v", got)
	}
	for _, part := range []string{"auth.WithBearer(keyStore)", "keyStore.Start(ctx, flush)", "app.WithMiddleware(keyStore.Middleware())", "app.WithRoutes(keyStore.Routes(authn))", `app.WithCommand("apikeys", keyStore.Command())`, "app.WithShutdown(keyStore.Close)"} {
		t.Run(part, func(t *testing.T) {
			if got := checkWiring([]byte(strings.Replace(source, part, "nil", 1))); got.Status != doctor.Warn {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestMiddlewareOrder(t *testing.T) {
	source := `package main
 func main() {
 provider, _ := flags.New()
 keyStore, _ := apikeys.NewStore(pool, cfg)
 keyStore.Start(ctx, flush)
 authn, _ := auth.New(auth.WithBearer(keyStore))
 app.New(app.WithMiddleware(authn.Middleware()), app.WithMiddleware(keyStore.Middleware()), app.WithMiddleware(provider.Middleware()))
 }`
	if got := checkWiring([]byte(source)); got.Status != doctor.Warn || !strings.Contains(got.Try, "after the auth and flags") {
		t.Fatal(got)
	}
}

func TestMissingTriggers(t *testing.T) {
	got := checkTriggers(t.TempDir())
	if got.Status != doctor.Warn || !strings.Contains(got.Try, "api_keys_audit_truncate") || !strings.Contains(got.Try, "api_key_plans_audit") {
		t.Fatal(got)
	}
}
